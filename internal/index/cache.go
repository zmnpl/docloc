package index

import (
	"bytes"
	"compress/flate"
	"encoding/binary"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	bolt "go.etcd.io/bbolt"
)

// Bucket names carry a version so a changed extractor can invalidate old
// results by bumping it.
var (
	filesBucket   = []byte("files.v1") // abs path -> size, mtime, content hash
	textsBucket   = []byte("texts.v2") // content hash -> deflate compressed text
	textsBucketV1 = []byte("texts.v1") // uncompressed predecessor, migrated on open
)

// Compaction rewrites the database file when at least this much of it, and
// at least half of it, is unused. bbolt never shrinks files by itself.
const compactMinWaste = 4 << 20

var ErrLocked = errors.New("cache is in use by another docloc instance")

// Cache persists extracted text in a bolt database. Text is keyed by a hash
// of the file content, so moved or copied files are not extracted again. A
// second bucket remembers size and mtime per path to avoid rehashing files
// that did not change. A nil *Cache is valid and caches nothing.
type Cache struct {
	db *bolt.DB
}

// DefaultCachePath returns the cache location below the user cache dir.
func DefaultCachePath() (string, error) {
	dir, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "docloc", "cache.db"), nil
}

func OpenCache(path string) (*Cache, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	db, err := openDB(path)
	if err != nil {
		return nil, err
	}
	err = db.Update(func(tx *bolt.Tx) error {
		if _, err := tx.CreateBucketIfNotExists(filesBucket); err != nil {
			return err
		}
		texts, err := tx.CreateBucketIfNotExists(textsBucket)
		if err != nil {
			return err
		}
		return migrateV1(tx, texts)
	})
	if err != nil {
		db.Close()
		return nil, err
	}
	if db, err = compactIfWasteful(db, path); err != nil {
		return nil, err
	}
	return &Cache{db: db}, nil
}

func openDB(path string) (*bolt.DB, error) {
	db, err := bolt.Open(path, 0o600, &bolt.Options{Timeout: 500 * time.Millisecond})
	if errors.Is(err, bolt.ErrTimeout) {
		return nil, ErrLocked
	}
	return db, err
}

// migrateV1 compresses texts from the old uncompressed bucket.
func migrateV1(tx *bolt.Tx, texts *bolt.Bucket) error {
	old := tx.Bucket(textsBucketV1)
	if old == nil {
		return nil
	}
	err := old.ForEach(func(k, v []byte) error {
		return texts.Put(k, compress(v))
	})
	if err != nil {
		return err
	}
	return tx.DeleteBucket(textsBucketV1)
}

// compactIfWasteful rewrites the database into a fresh file when most of it
// is free pages, e.g. after a migration or after pruning many entries.
func compactIfWasteful(db *bolt.DB, path string) (*bolt.DB, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return db, nil
	}
	// Free list stats miss the unused tail of a grown file, so compare the
	// pages the buckets occupy with the file size instead.
	var live int64
	db.View(func(tx *bolt.Tx) error {
		return tx.ForEach(func(_ []byte, b *bolt.Bucket) error {
			st := b.Stats()
			live += int64(st.BranchAlloc + st.LeafAlloc)
			return nil
		})
	})
	waste := fi.Size() - live
	if waste < compactMinWaste || waste*2 < fi.Size() {
		return db, nil
	}
	tmp := path + ".compact"
	os.Remove(tmp)
	dst, err := bolt.Open(tmp, 0o600, nil)
	if err != nil {
		return db, nil // keep working with the uncompacted file
	}
	err = bolt.Compact(dst, db, 0)
	dst.Close()
	if err != nil {
		os.Remove(tmp)
		return db, nil
	}
	db.Close()
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
	}
	return openDB(path)
}

func (c *Cache) Close() error {
	if c == nil {
		return nil
	}
	return c.db.Close()
}

func compress(text []byte) []byte {
	var b bytes.Buffer
	w, _ := flate.NewWriter(&b, flate.DefaultCompression)
	w.Write(text)
	w.Close()
	return b.Bytes()
}

func decompress(data []byte) (string, bool) {
	text, err := io.ReadAll(flate.NewReader(bytes.NewReader(data)))
	return string(text), err == nil
}

type fileMeta struct {
	size, mtime int64
	hash        uint64
}

func (m fileMeta) encode() []byte {
	b := make([]byte, 24)
	binary.LittleEndian.PutUint64(b, uint64(m.size))
	binary.LittleEndian.PutUint64(b[8:], uint64(m.mtime))
	binary.LittleEndian.PutUint64(b[16:], m.hash)
	return b
}

func hashKey(h uint64) []byte {
	return binary.BigEndian.AppendUint64(nil, h)
}

// Lookup returns the cached text for path when size and mtime still match.
func (c *Cache) Lookup(path string, size, mtime int64) (text string, hash uint64, ok bool) {
	if c == nil {
		return "", 0, false
	}
	c.db.View(func(tx *bolt.Tx) error {
		v := tx.Bucket(filesBucket).Get([]byte(path))
		if len(v) != 24 ||
			int64(binary.LittleEndian.Uint64(v)) != size ||
			int64(binary.LittleEndian.Uint64(v[8:])) != mtime {
			return nil
		}
		hash = binary.LittleEndian.Uint64(v[16:])
		if t := tx.Bucket(textsBucket).Get(hashKey(hash)); t != nil {
			text, ok = decompress(t)
		}
		return nil
	})
	return text, hash, ok
}

// TextByHash returns cached text for content with the given hash.
func (c *Cache) TextByHash(hash uint64) (string, bool) {
	if c == nil {
		return "", false
	}
	var text string
	var ok bool
	c.db.View(func(tx *bolt.Tx) error {
		if t := tx.Bucket(textsBucket).Get(hashKey(hash)); t != nil {
			text, ok = decompress(t)
		}
		return nil
	})
	return text, ok
}

// Store records the file metadata and, when text is non-nil, the text for
// its content hash. Concurrent calls are coalesced into few transactions.
func (c *Cache) Store(path string, size, mtime int64, hash uint64, text *string) error {
	if c == nil {
		return nil
	}
	var compressed []byte
	if text != nil {
		compressed = compress([]byte(*text))
	}
	return c.db.Batch(func(tx *bolt.Tx) error {
		if compressed != nil {
			if err := tx.Bucket(textsBucket).Put(hashKey(hash), compressed); err != nil {
				return err
			}
		}
		return tx.Bucket(filesBucket).Put([]byte(path), fileMeta{size, mtime, hash}.encode())
	})
}

// Prune forgets files below root that are not in seen, unless keep reports
// that they were deliberately not visited (e.g. hidden directories), and
// then drops texts no file refers to anymore. Entries outside root belong
// to other search roots and are left alone.
func (c *Cache) Prune(root string, seen map[string]bool, keep func(path string) bool) (files, texts int, err error) {
	if c == nil {
		return 0, 0, nil
	}
	prefix := root
	if !strings.HasSuffix(prefix, string(filepath.Separator)) {
		prefix += string(filepath.Separator)
	}
	err = c.db.Update(func(tx *bolt.Tx) error {
		fb, tb := tx.Bucket(filesBucket), tx.Bucket(textsBucket)
		var gone [][]byte
		used := map[uint64]bool{}
		fb.ForEach(func(k, v []byte) error {
			p := string(k)
			if strings.HasPrefix(p, prefix) && !seen[p] && !keep(p) {
				gone = append(gone, bytes.Clone(k))
			} else if len(v) == 24 {
				used[binary.LittleEndian.Uint64(v[16:])] = true
			}
			return nil
		})
		for _, k := range gone {
			if err := fb.Delete(k); err != nil {
				return err
			}
		}
		var orphans [][]byte
		tb.ForEach(func(k, _ []byte) error {
			if len(k) != 8 || !used[binary.BigEndian.Uint64(k)] {
				orphans = append(orphans, bytes.Clone(k))
			}
			return nil
		})
		for _, k := range orphans {
			if err := tb.Delete(k); err != nil {
				return err
			}
		}
		files, texts = len(gone), len(orphans)
		return nil
	})
	return files, texts, err
}
