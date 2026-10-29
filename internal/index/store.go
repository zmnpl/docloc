// Package index holds the searchable documents, the persistent text cache
// and the background indexer that fills them.
package index

import (
	"path/filepath"
	"strings"
	"sync"
	"unicode"
)

// Doc is one indexed file. It is immutable once added to a Store.
type Doc struct {
	Rel  string // path relative to the search root, used for display
	Abs  string
	Text string
	Note string // why no text is available, if extraction failed

	lowRel, lowName, lowText string
}

func NewDoc(rel, abs, text, note string) *Doc {
	d := &Doc{Rel: rel, Abs: abs, Text: text, Note: note}
	d.lowRel = strings.ToLower(filepath.ToSlash(rel))
	d.lowName = strings.ToLower(filepath.Base(rel))
	d.lowText = strings.ToLower(text)
	return d
}

// Store is an append-only, concurrency safe collection of documents with an
// inverted word index for fuzzy search.
type Store struct {
	mu    sync.RWMutex
	docs  []*Doc
	vocab map[string][]int32
}

func NewStore() *Store {
	return &Store{vocab: map[string][]int32{}}
}

func (s *Store) Add(d *Doc) {
	words := uniqueWords(d.lowText)
	s.mu.Lock()
	defer s.mu.Unlock()
	id := int32(len(s.docs))
	s.docs = append(s.docs, d)
	for _, w := range words {
		s.vocab[w] = append(s.vocab[w], id)
	}
}

func (s *Store) Len() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.docs)
}

func (s *Store) Doc(id int32) *Doc {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if id < 0 || int(id) >= len(s.docs) {
		return nil
	}
	return s.docs[id]
}

const (
	minWordLen = 2
	maxWordLen = 40
)

func isWordRune(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsDigit(r)
}

func uniqueWords(text string) []string {
	seen := map[string]struct{}{}
	var words []string
	for _, w := range strings.FieldsFunc(text, func(r rune) bool { return !isWordRune(r) }) {
		if len(w) < minWordLen || len(w) > maxWordLen {
			continue
		}
		if _, ok := seen[w]; !ok {
			seen[w] = struct{}{}
			words = append(words, w)
		}
	}
	return words
}
