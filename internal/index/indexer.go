package index

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/cespare/xxhash/v2"

	"github.com/simonpaul/docloc/internal/extract"
)

// ExtractTimeout bounds the time spent on a single document.
const ExtractTimeout = 60 * time.Second

// Indexer walks a directory tree and feeds every supported document into a
// Store, using the Cache to skip documents it has seen before.
type Indexer struct {
	Root   string
	Hidden bool // descend into hidden directories
	Cache  *Cache
	Store  *Store

	found, done atomic.Int64
	walking     atomic.Bool
	finished    atomic.Bool

	// Pruned reports how many stale cache entries (files, texts) the run
	// removed; valid once Progress().Finished is set.
	PrunedFiles, PrunedTexts int
}

type Progress struct {
	Found, Done int64
	Walking     bool // still discovering files
	Finished    bool
}

func (ix *Indexer) Progress() Progress {
	return Progress{
		Found:    ix.found.Load(),
		Done:     ix.done.Load(),
		Walking:  ix.walking.Load(),
		Finished: ix.finished.Load(),
	}
}

type job struct {
	abs, rel    string
	size, mtime int64
}

// Run indexes the tree. Like fast-p it first serves everything known from
// the cache and only then extracts new or changed documents, so known
// documents become searchable right away.
func (ix *Indexer) Run(ctx context.Context) {
	defer ix.finished.Store(true)
	ix.walking.Store(true)
	var pending []job
	seen := map[string]bool{}
	ignore := loadIgnore(ix.Root)
	filepath.WalkDir(ix.Root, func(p string, d fs.DirEntry, err error) error {
		if ctx.Err() != nil {
			return filepath.SkipAll
		}
		if err != nil {
			return nil // unreadable entry: skip it, keep walking
		}
		name := d.Name()
		rel, err := filepath.Rel(ix.Root, p)
		if err != nil {
			rel = p
		}
		if d.IsDir() {
			if p == ix.Root {
				return nil
			}
			if strings.HasPrefix(name, ".") && !ix.Hidden || ignore.match(filepath.ToSlash(rel), true) {
				return filepath.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() || !extract.Supported(name) ||
			strings.HasPrefix(name, "~$") || strings.HasPrefix(name, ".~lock") ||
			ignore.match(filepath.ToSlash(rel), false) {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		abs, err := filepath.Abs(p)
		if err != nil {
			abs = p
		}
		ix.found.Add(1)
		seen[abs] = true
		j := job{abs, rel, info.Size(), info.ModTime().UnixNano()}
		if text, _, ok := ix.Cache.Lookup(abs, j.size, j.mtime); ok {
			ix.Store.Add(NewDoc(rel, abs, j.mtime, text, ""))
			ix.done.Add(1)
			return nil
		}
		pending = append(pending, j)
		return nil
	})
	ix.walking.Store(false)

	jobs := make(chan job)
	var wg sync.WaitGroup
	for range max(2, runtime.NumCPU()*3/4) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range jobs {
				ix.process(ctx, j)
				ix.done.Add(1)
			}
		}()
	}
	for _, j := range pending {
		select {
		case jobs <- j:
		case <-ctx.Done():
		}
		if ctx.Err() != nil {
			break
		}
	}
	close(jobs)
	wg.Wait()

	// Only a complete run knows which files are gone.
	if ctx.Err() == nil {
		ix.prune(seen)
	}
}

// prune removes cache entries for files below the root that no longer exist.
func (ix *Indexer) prune(seen map[string]bool) {
	root, err := filepath.Abs(ix.Root)
	if err != nil {
		return
	}
	keep := func(p string) bool {
		if ix.Hidden {
			return false
		}
		// Files in hidden directories were skipped, not deleted.
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return true
		}
		dirs := strings.Split(filepath.Dir(rel), string(filepath.Separator))
		for _, d := range dirs {
			if strings.HasPrefix(d, ".") && d != "." {
				return true
			}
		}
		return false
	}
	ix.PrunedFiles, ix.PrunedTexts, _ = ix.Cache.Prune(root, seen, keep)
}

func (ix *Indexer) process(ctx context.Context, j job) {
	hash, err := hashFile(j.abs)
	if err != nil {
		ix.Store.Add(NewDoc(j.rel, j.abs, j.mtime, "", err.Error()))
		return
	}
	if text, ok := ix.Cache.TextByHash(hash); ok {
		ix.Cache.Store(j.abs, j.size, j.mtime, hash, nil)
		ix.Store.Add(NewDoc(j.rel, j.abs, j.mtime, text, ""))
		return
	}
	text, err := extractWithTimeout(ctx, j.abs)
	if ctx.Err() != nil {
		return
	}
	note := ""
	if err != nil {
		note = err.Error()
	}
	// Failures are cached too (as empty text) so broken files are not
	// retried on every start; clear the cache to retry.
	ix.Cache.Store(j.abs, j.size, j.mtime, hash, &text)
	ix.Store.Add(NewDoc(j.rel, j.abs, j.mtime, text, note))
}

func hashFile(p string) (uint64, error) {
	f, err := os.Open(p)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	h := xxhash.New()
	if _, err := io.Copy(h, f); err != nil {
		return 0, err
	}
	return h.Sum64(), nil
}

var errTimeout = errors.New("text extraction timed out")

// extractWithTimeout runs the extractor in its own goroutine. Parsers offer
// no cancellation, so on timeout the goroutine is abandoned.
func extractWithTimeout(ctx context.Context, p string) (string, error) {
	type result struct {
		text string
		err  error
	}
	ch := make(chan result, 1)
	go func() {
		text, err := extract.File(p)
		ch <- result{text, err}
	}()
	select {
	case r := <-ch:
		return r.text, r.err
	case <-time.After(ExtractTimeout):
		return "", errTimeout
	case <-ctx.Done():
		return "", ctx.Err()
	}
}
