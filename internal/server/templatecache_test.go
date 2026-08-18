package server

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// For these tests the "parsed template" type is just a string, so parse
// failures/successes can be asserted directly without pulling in
// html/template or text/template.

func writeTestFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("writeTestFile: %v", err)
	}
	return path
}

// countingParse returns a parse func that records how many times it was
// called and echoes the input content back as the "parsed" value.
func countingParse() (parse func([]byte) (string, error), calls *int) {
	n := 0
	return func(b []byte) (string, error) {
		n++
		return string(b), nil
	}, &n
}

func TestTemplateCache_ParsesOnFirstAccess(t *testing.T) {
	dir := t.TempDir()
	writeTestFile(t, dir, "greeting.txt", "hello")

	parse, calls := countingParse()
	c := newTemplateCache(dir, parse)

	got, err := c.get("greeting.txt")
	if err != nil {
		t.Fatalf("get returned error: %v", err)
	}
	if got != "hello" {
		t.Errorf("got %q, want %q", got, "hello")
	}
	if *calls != 1 {
		t.Errorf("parse called %d times, want 1", *calls)
	}
}

func TestTemplateCache_ReusesCachedParseWhenUnchanged(t *testing.T) {
	dir := t.TempDir()
	writeTestFile(t, dir, "greeting.txt", "hello")

	parse, calls := countingParse()
	c := newTemplateCache(dir, parse)

	if _, err := c.get("greeting.txt"); err != nil {
		t.Fatalf("first get returned error: %v", err)
	}
	if _, err := c.get("greeting.txt"); err != nil {
		t.Fatalf("second get returned error: %v", err)
	}

	if *calls != 1 {
		t.Errorf("parse called %d times, want 1 (second get should have used the cache)", *calls)
	}
}

func TestTemplateCache_ReparsesWhenFileChanges(t *testing.T) {
	dir := t.TempDir()
	path := writeTestFile(t, dir, "greeting.txt", "hello")

	parse, calls := countingParse()
	c := newTemplateCache(dir, parse)

	if _, err := c.get("greeting.txt"); err != nil {
		t.Fatalf("first get returned error: %v", err)
	}

	// Overwrite, then force the mtime forward explicitly rather than
	// relying on wall-clock resolution between the two writes.
	writeTestFile(t, dir, "greeting.txt", "goodbye")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	later := info.ModTime().Add(time.Second)
	if err := os.Chtimes(path, later, later); err != nil {
		t.Fatalf("chtimes: %v", err)
	}

	got, err := c.get("greeting.txt")
	if err != nil {
		t.Fatalf("second get returned error: %v", err)
	}
	if got != "goodbye" {
		t.Errorf("got %q, want %q", got, "goodbye")
	}
	if *calls != 2 {
		t.Errorf("parse called %d times, want 2 (file change should force a reparse)", *calls)
	}
}

func TestTemplateCache_ReturnsAccessErrorWhenMissing(t *testing.T) {
	dir := t.TempDir()
	parse, _ := countingParse()
	c := newTemplateCache(dir, parse)

	_, err := c.get("missing.txt")
	if err == nil {
		t.Fatal("expected error for missing file, got nil")
	}

	if _, ok := errors.AsType[*templateAccessError](err); !ok {
		t.Errorf("expected a *templateAccessError, got: %v (%T)", err, err)
	}
}

func TestTemplateCache_ReturnsParseErrorUnwrapped(t *testing.T) {
	dir := t.TempDir()
	writeTestFile(t, dir, "bad.txt", "content")

	parseErr := errors.New("boom")
	c := newTemplateCache(dir, func(b []byte) (string, error) {
		return "", parseErr
	})

	_, err := c.get("bad.txt")
	if err == nil {
		t.Fatal("expected error from parse, got nil")
	}
	if !errors.Is(err, parseErr) {
		t.Errorf("expected the raw parse error, got: %v", err)
	}

	if _, ok := errors.AsType[*templateAccessError](err); ok {
		t.Errorf("parse error should not be wrapped as a templateAccessError: %v", err)
	}
}

func TestTemplateCache_ConcurrentAccess(t *testing.T) {
	dir := t.TempDir()
	writeTestFile(t, dir, "greeting.txt", "hello")

	parse, _ := countingParse()
	c := newTemplateCache(dir, parse)

	var wg sync.WaitGroup
	for range 50 {
		wg.Go(func() {
			if _, err := c.get("greeting.txt"); err != nil {
				t.Errorf("get returned error: %v", err)
			}
		})
	}
	wg.Wait()
}
