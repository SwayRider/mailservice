// templatecache.go implements a small mtime-invalidated cache for parsed
// email templates, so a high-volume SendTemplate flow doesn't pay disk I/O
// and template-parse cost on every request. Cache entries are invalidated by
// comparing the template file's mtime on each access, so editing a template
// on disk is still picked up live -- matching the read-per-request behavior
// this replaces -- without repeatedly re-parsing an unchanged file.

package server

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// templateAccessError wraps a filesystem error (stat or read) encountered
// while loading a template. Callers use errors.As to distinguish this from
// a template parse error, since the two map to different gRPC codes
// (NotFound vs Internal).
type templateAccessError struct {
	err error
}

func (e *templateAccessError) Error() string {
	return fmt.Sprintf("template access error: %v", e.err)
}

func (e *templateAccessError) Unwrap() error {
	return e.err
}

// cachedTemplate pairs a parsed template with the mtime of the file it was
// parsed from, so a cache hit can be validated with a single stat.
type cachedTemplate[T any] struct {
	modTime time.Time
	tmpl    T
}

// templateCache caches parsed templates read from dir, keyed by name and
// invalidated when the file's mtime changes.
type templateCache[T any] struct {
	dir   string
	parse func([]byte) (T, error)

	mu      sync.Mutex
	entries map[string]cachedTemplate[T]
}

// newTemplateCache creates a templateCache that reads templates from dir,
// parsing file contents with parse.
func newTemplateCache[T any](dir string, parse func([]byte) (T, error)) *templateCache[T] {
	return &templateCache[T]{
		dir:     dir,
		parse:   parse,
		entries: make(map[string]cachedTemplate[T]),
	}
}

// get returns the parsed template named name, reusing a cached parse result
// when the file's mtime matches the cached entry's, and re-reading/parsing
// otherwise. A filesystem error from the stat or read is wrapped in
// *templateAccessError; a parse error is returned unwrapped.
func (c *templateCache[T]) get(name string) (T, error) {
	var zero T

	path := filepath.Join(c.dir, name)
	info, err := os.Stat(path)
	if err != nil {
		return zero, &templateAccessError{err}
	}

	c.mu.Lock()
	entry, ok := c.entries[name]
	c.mu.Unlock()
	if ok && entry.modTime.Equal(info.ModTime()) {
		return entry.tmpl, nil
	}

	content, err := os.ReadFile(path)
	if err != nil {
		return zero, &templateAccessError{err}
	}

	tmpl, err := c.parse(content)
	if err != nil {
		return zero, err
	}

	c.mu.Lock()
	c.entries[name] = cachedTemplate[T]{modTime: info.ModTime(), tmpl: tmpl}
	c.mu.Unlock()

	return tmpl, nil
}
