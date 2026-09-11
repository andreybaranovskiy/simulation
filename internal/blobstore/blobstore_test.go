package blobstore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := New(t.TempDir())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return s
}

func TestPutStoresContentByHash(t *testing.T) {
	s := newTestStore(t)
	const content = "objects, transitions, keyframes"

	res, err := s.Put(context.Background(), strings.NewReader(content), 0)
	if err != nil {
		t.Fatalf("Put: %v", err)
	}

	want := sha256.Sum256([]byte(content))
	if res.SHA256 != hex.EncodeToString(want[:]) {
		t.Errorf("SHA256 = %q, want %q", res.SHA256, hex.EncodeToString(want[:]))
	}
	if res.Size != int64(len(content)) {
		t.Errorf("Size = %d, want %d", res.Size, len(content))
	}
	if res.Deduplicated {
		t.Error("the first write of a blob should not be reported as deduplicated")
	}

	f, err := s.Open(res.RelPath)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer f.Close()

	got, err := io.ReadAll(f)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if string(got) != content {
		t.Errorf("read back %q, want %q", got, content)
	}
}

// Identical content must occupy one file: this is what keeps a re-uploaded
// GLB from doubling disk use.
func TestPutDeduplicates(t *testing.T) {
	s := newTestStore(t)
	const content = "the same model uploaded twice"

	first, err := s.Put(context.Background(), strings.NewReader(content), 0)
	if err != nil {
		t.Fatalf("first Put: %v", err)
	}
	second, err := s.Put(context.Background(), strings.NewReader(content), 0)
	if err != nil {
		t.Fatalf("second Put: %v", err)
	}

	if second.RelPath != first.RelPath {
		t.Errorf("the same content landed at two paths: %q and %q", first.RelPath, second.RelPath)
	}
	if !second.Deduplicated {
		t.Error("the second write should be reported as deduplicated")
	}
}

func TestPutRejectsOversizedContent(t *testing.T) {
	s := newTestStore(t)

	_, err := s.Put(context.Background(), strings.NewReader(strings.Repeat("x", 100)), 50)
	if !errors.Is(err, ErrTooLarge) {
		t.Fatalf("Put over the limit = %v, want ErrTooLarge", err)
	}

	// The rejected upload must leave nothing behind.
	if n := countFiles(t, s.root); n != 0 {
		t.Errorf("an oversized upload left %d files in the store", n)
	}
}

func TestPutAcceptsContentExactlyAtTheLimit(t *testing.T) {
	s := newTestStore(t)

	res, err := s.Put(context.Background(), strings.NewReader(strings.Repeat("x", 50)), 50)
	if err != nil {
		t.Fatalf("Put at exactly the limit: %v", err)
	}
	if res.Size != 50 {
		t.Errorf("Size = %d, want 50", res.Size)
	}
}

// A stored path comes from the database, so a corrupted or tampered row must
// not be able to read or delete files outside the blob root.
func TestResolveRejectsPathTraversal(t *testing.T) {
	s := newTestStore(t)

	bad := []string{
		"../outside",
		"../../etc/passwd",
		"ab/../../escape",
		"/absolute/path",
		`..\windows\system32`,
	}

	for _, p := range bad {
		if _, err := s.Resolve(p); err == nil {
			t.Errorf("Resolve(%q) returned no error, want a rejection", p)
		}
	}
}

func TestRemoveIsIdempotent(t *testing.T) {
	s := newTestStore(t)

	res, err := s.Put(context.Background(), strings.NewReader("temporary"), 0)
	if err != nil {
		t.Fatalf("Put: %v", err)
	}

	if err := s.Remove(res.RelPath); err != nil {
		t.Fatalf("first Remove: %v", err)
	}
	// Removing again must not fail: cleanup paths call it without checking.
	if err := s.Remove(res.RelPath); err != nil {
		t.Fatalf("second Remove: %v", err)
	}
}

// A cancelled upload must abort rather than stream to completion.
func TestPutHonoursContextCancellation(t *testing.T) {
	s := newTestStore(t)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := s.Put(ctx, strings.NewReader(strings.Repeat("x", 1024)), 0)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Put with a cancelled context = %v, want context.Canceled", err)
	}
	if n := countFiles(t, s.root); n != 0 {
		t.Errorf("a cancelled upload left %d files in the store", n)
	}
}

func countFiles(t *testing.T, root string) int {
	t.Helper()
	count := 0
	err := filepath.Walk(root, func(_ string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() {
			count++
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
	return count
}
