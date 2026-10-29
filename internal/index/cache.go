package index

import (
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"time"

	bolt "go.etcd.io/bbolt"
)

// Bucket names carry a version so a changed extractor can invalidate old
// results by bumping it.
var (
	filesBucket = []byte("files.v1") // abs path -> size, mtime, content hash
	textsBucket = []byte("texts.v1") // content hash -> extracted text
)

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
	db, err := bolt.Open(path, 0o600, &bolt.Options{Timeout: 500 * time.Millisecond})
	if errors.Is(err, bolt.ErrTimeout) {
		return nil, ErrLocked
	}
	if err != nil {
		return nil, err
	}
	err = db.Update(func(tx *bolt.Tx) error {
		if _, err := tx.CreateBucketIfNotExists(filesBucket); err != nil {
			return err
		}
		_, err := tx.CreateBucketIfNotExists(textsBucket)
		return err
	})
	if err != nil {
		db.Close()
		return nil, err
	}
	return &Cache{db: db}, nil
}

func (c *Cache) Close() error {
	if c == nil {
		return nil
	}
	return c.db.Close()
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
			text, ok = string(t), true
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
			text, ok = string(t), true
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
	return c.db.Batch(func(tx *bolt.Tx) error {
		if text != nil {
			if err := tx.Bucket(textsBucket).Put(hashKey(hash), []byte(*text)); err != nil {
				return err
			}
		}
		return tx.Bucket(filesBucket).Put([]byte(path), fileMeta{size, mtime, hash}.encode())
	})
}
