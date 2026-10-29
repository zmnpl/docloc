package index

import (
	"context"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"testing"

	bolt "go.etcd.io/bbolt"
)

func testStore() *Store {
	s := NewStore()
	s.Add(NewDoc("steuer/2023/telekom_rechnung.pdf", "/x/1", "Telekom Deutschland GmbH\nIhre Rechnung für Oktober 2023", ""))
	s.Add(NewDoc("steuer/2022/strom.pdf", "/x/2", "Stadtwerke Abrechnung Strom 2022", ""))
	s.Add(NewDoc("privat/brief.docx", "/x/3", "Sehr geehrte Damen und Herren, anbei die Lohnsteuerbescheinigung.", ""))
	s.Add(NewDoc("privat/notizen.md", "/x/4", "Telekom Hotline anrufen wegen Router", ""))
	return s
}

func rels(s *Store, r *Result) []string {
	var out []string
	for _, h := range r.Hits {
		out = append(out, s.Doc(h.Doc).Rel)
	}
	return out
}

func TestExactSearch(t *testing.T) {
	s := testStore()
	tests := []struct {
		q    string
		want []string
	}{
		{"", []string{"privat/brief.docx", "privat/notizen.md", "steuer/2022/strom.pdf", "steuer/2023/telekom_rechnung.pdf"}},
		// name match ranks before a content-only match
		{"telekom", []string{"steuer/2023/telekom_rechnung.pdf", "privat/notizen.md"}},
		{"TELEKOM rechnung", []string{"steuer/2023/telekom_rechnung.pdf"}},
		{"telekom !rechnung", []string{"privat/notizen.md"}},
		{"steuer 2022", []string{"steuer/2022/strom.pdf"}},
		{"lohnsteuer", []string{"privat/brief.docx"}},
		{"nothing-like-this", nil},
	}
	for _, tt := range tests {
		r, err := s.Search(context.Background(), tt.q, Exact, nil)
		if err != nil {
			t.Fatal(err)
		}
		if got := rels(s, r); !equal(got, tt.want) {
			t.Errorf("Search(%q) = %v, want %v", tt.q, got, tt.want)
		}
	}
}

func TestNarrowingIncludesNewDocs(t *testing.T) {
	s := testStore()
	prev, _ := s.Search(context.Background(), "tele", Exact, nil)
	s.Add(NewDoc("neu/telefon.txt", "/x/5", "Telefonnummer", ""))
	r, _ := s.Search(context.Background(), "telef", Exact, prev)
	if got := rels(s, r); !equal(got, []string{"neu/telefon.txt"}) {
		t.Errorf("got %v", got)
	}
	// Not a refinement: must not be restricted to prev's hits.
	r, _ = s.Search(context.Background(), "strom", Exact, prev)
	if got := rels(s, r); !equal(got, []string{"steuer/2022/strom.pdf"}) {
		t.Errorf("got %v", got)
	}
}

func TestFuzzySearch(t *testing.T) {
	s := testStore()
	tests := []struct {
		q    string
		want string
	}{
		{"rechnug", "steuer/2023/telekom_rechnung.pdf"},  // typo
		{"stadtwrke", "steuer/2022/strom.pdf"},           // missing letter
		{"lohnsteuerbescheinigng", "privat/brief.docx"},  // typo in a long word
		{"hotlien router", "privat/notizen.md"},          // transposition
		{"tlkrchng", "steuer/2023/telekom_rechnung.pdf"}, // subsequence of the name
	}
	for _, tt := range tests {
		r, err := s.Search(context.Background(), tt.q, Fuzzy, nil)
		if err != nil {
			t.Fatal(err)
		}
		got := rels(s, r)
		if len(got) == 0 || got[0] != tt.want {
			t.Errorf("Search(%q) = %v, want %s first", tt.q, got, tt.want)
		}
	}
}

func TestCancelledSearch(t *testing.T) {
	s := testStore()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.Search(ctx, "telekom", Exact, nil); err == nil {
		t.Error("expected an error for a cancelled context")
	}
}

func TestEditDistance(t *testing.T) {
	tests := []struct {
		a, b string
		want int
	}{
		{"rechnung", "rechnung", 0},
		{"rechnug", "rechnung", 1},
		{"hotlien", "hotline", 1},
		{"abc", "xyz", 3},
	}
	for _, tt := range tests {
		if got := editDistance(tt.a, tt.b, 5); got != tt.want {
			t.Errorf("editDistance(%q, %q) = %d, want %d", tt.a, tt.b, got, tt.want)
		}
	}
}

func TestIndexerUsesCache(t *testing.T) {
	root := t.TempDir()
	write := func(rel, content string) {
		p := filepath.Join(root, rel)
		os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("a/notes.txt", "alpha beta")
	write("b/readme.md", "gamma")
	write(".hidden/secret.txt", "hidden")
	write("image.png", "not indexed")

	dbPath := filepath.Join(t.TempDir(), "cache.db")
	run := func() *Store {
		c, err := OpenCache(dbPath)
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()
		s := NewStore()
		ix := &Indexer{Root: root, Cache: c, Store: s}
		ix.Run(context.Background())
		if p := ix.Progress(); !p.Finished || p.Found != p.Done {
			t.Errorf("progress %+v", p)
		}
		return s
	}

	s := run()
	if s.Len() != 2 {
		t.Fatalf("indexed %d docs, want 2", s.Len())
	}

	// Second run is served from the cache: changing the extracted text in the
	// cache proves the file was not read again.
	c, _ := OpenCache(dbPath)
	p := filepath.Join(root, "a/notes.txt")
	fi, _ := os.Stat(p)
	abs, _ := filepath.Abs(p)
	_, hash, ok := c.Lookup(abs, fi.Size(), fi.ModTime().UnixNano())
	if !ok {
		t.Fatal("expected a cache entry")
	}
	marker := "from cache"
	c.Store(abs, fi.Size(), fi.ModTime().UnixNano(), hash, &marker)
	c.Close()

	s = run()
	r, _ := s.Search(context.Background(), "from cache", Exact, nil)
	if len(r.Hits) != 1 {
		t.Errorf("expected the cached text to be used, got %v", rels(s, r))
	}
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestCacheMigratesUncompressedTexts(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cache.db")
	db, err := bolt.Open(path, 0o600, nil)
	if err != nil {
		t.Fatal(err)
	}
	db.Update(func(tx *bolt.Tx) error {
		b, _ := tx.CreateBucketIfNotExists(textsBucketV1)
		return b.Put(hashKey(42), []byte("old plain text"))
	})
	db.Close()

	c, err := OpenCache(path)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if text, ok := c.TextByHash(42); !ok || text != "old plain text" {
		t.Errorf("TextByHash = %q, %v", text, ok)
	}
	c.db.View(func(tx *bolt.Tx) error {
		if tx.Bucket(textsBucketV1) != nil {
			t.Error("old bucket still present")
		}
		return nil
	})
}

func TestPrune(t *testing.T) {
	root := t.TempDir()
	write := func(rel string) {
		p := filepath.Join(root, rel)
		os.MkdirAll(filepath.Dir(p), 0o755)
		os.WriteFile(p, []byte("content of "+rel), 0o644)
	}
	write("keep.txt")
	write("gone.txt")
	write(".hidden/skipped.txt")
	write("dup1.txt")

	c, err := OpenCache(filepath.Join(t.TempDir(), "cache.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	// An entry from another search root must survive.
	other := "/elsewhere/doc.txt"
	otherText := "other root"
	c.Store(other, 1, 1, 7, &otherText)

	(&Indexer{Root: root, Hidden: true, Cache: c, Store: NewStore()}).Run(context.Background())
	os.Remove(filepath.Join(root, "gone.txt"))

	ix := &Indexer{Root: root, Cache: c, Store: NewStore()}
	ix.Run(context.Background())
	if ix.PrunedFiles != 1 || ix.PrunedTexts != 1 {
		t.Errorf("pruned %d files, %d texts; want 1, 1", ix.PrunedFiles, ix.PrunedTexts)
	}
	if _, ok := c.TextByHash(7); !ok {
		t.Error("text of another root was pruned")
	}
	abs, _ := filepath.Abs(filepath.Join(root, ".hidden/skipped.txt"))
	fi, _ := os.Stat(abs)
	if _, _, ok := c.Lookup(abs, fi.Size(), fi.ModTime().UnixNano()); !ok {
		t.Error("file in a skipped hidden directory was pruned")
	}
}

func TestCompaction(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cache.db")
	c, err := OpenCache(path)
	if err != nil {
		t.Fatal(err)
	}
	// Random text does not compress, so the file really grows.
	rng := rand.New(rand.NewPCG(1, 2))
	big := make([]byte, 64<<10)
	for i := range big {
		big[i] = byte('a' + rng.IntN(26))
	}
	text := string(big)
	for i := range 200 {
		c.Store(fmt.Sprintf("/root/f%d", i), 1, 1, uint64(i), &text)
	}
	c.Prune("/root", map[string]bool{}, func(string) bool { return false })
	c.Close()
	before, _ := os.Stat(path)

	c, err = OpenCache(path)
	if err != nil {
		t.Fatal(err)
	}
	c.Close()
	after, _ := os.Stat(path)
	if after.Size() >= before.Size()/2 {
		t.Errorf("not compacted: %d -> %d bytes", before.Size(), after.Size())
	}
}
