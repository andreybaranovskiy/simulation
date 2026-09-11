// Package blobstore stores uploaded files on disk, addressed by content hash.
//
// Content addressing means a file uploaded twice occupies one copy on disk,
// which matters when the files in question are hundred-megabyte GLB models and
// multi-gigabyte animation exports. The database rows are what give a blob a
// name, a project and a kind; the blob itself is anonymous.
package blobstore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// ErrTooLarge is returned when an upload exceeds the configured limit. The
// partial file is removed before it is returned.
var ErrTooLarge = errors.New("file is larger than the allowed maximum")

// Store writes blobs under Root/blobs/aa/bb/<full-hash>, sharding by the first
// two hash bytes so no directory grows past a few thousand entries.
type Store struct {
	root string
}

func New(dataDir string) (*Store, error) {
	root := filepath.Join(dataDir, "blobs")
	if err := os.MkdirAll(root, 0o750); err != nil {
		return nil, fmt.Errorf("create blob directory: %w", err)
	}
	return &Store{root: root}, nil
}

// Result describes a stored blob.
type Result struct {
	SHA256 string
	// RelPath is the storage path recorded in the database, always using
	// forward slashes so it is portable between the server and any tooling.
	RelPath string
	Size    int64
	// Deduplicated is true when identical content was already on disk.
	Deduplicated bool
}

// Put streams src to disk, hashing as it goes, and moves the finished file into
// its content-addressed location. maxBytes of zero means no limit.
//
// The hash is not known until the data has been read, so the write goes to a
// temporary file first and is renamed once the destination is known. A failed
// or oversized upload therefore never leaves a partial blob in the store.
func (s *Store) Put(ctx context.Context, src io.Reader, maxBytes int64) (Result, error) {
	tmp, err := os.CreateTemp(s.root, ".upload-*")
	if err != nil {
		return Result{}, fmt.Errorf("create temporary file: %w", err)
	}
	tmpName := tmp.Name()

	// Clean up unless the happy path renames the file out from under us.
	committed := false
	defer func() {
		tmp.Close()
		if !committed {
			os.Remove(tmpName)
		}
	}()

	reader := src
	if maxBytes > 0 {
		// Read one byte past the limit so exceeding it is detectable.
		reader = io.LimitReader(src, maxBytes+1)
	}

	hasher := sha256.New()
	written, err := io.Copy(io.MultiWriter(tmp, hasher), &contextReader{ctx: ctx, r: reader})
	if err != nil {
		return Result{}, fmt.Errorf("write upload: %w", err)
	}
	if maxBytes > 0 && written > maxBytes {
		return Result{}, ErrTooLarge
	}
	if err := tmp.Sync(); err != nil {
		return Result{}, fmt.Errorf("flush upload: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return Result{}, fmt.Errorf("close upload: %w", err)
	}

	sum := hex.EncodeToString(hasher.Sum(nil))
	rel := relPathFor(sum)
	abs := filepath.Join(s.root, filepath.FromSlash(rel))

	if err := os.MkdirAll(filepath.Dir(abs), 0o750); err != nil {
		return Result{}, fmt.Errorf("create blob directory: %w", err)
	}

	// An existing blob with this hash has identical content by definition, so
	// keep it and discard the upload rather than rewriting the same bytes.
	if _, err := os.Stat(abs); err == nil {
		return Result{SHA256: sum, RelPath: rel, Size: written, Deduplicated: true}, nil
	}

	if err := os.Rename(tmpName, abs); err != nil {
		return Result{}, fmt.Errorf("store blob: %w", err)
	}
	committed = true

	return Result{SHA256: sum, RelPath: rel, Size: written}, nil
}

// Open returns a readable handle to a stored blob.
func (s *Store) Open(relPath string) (*os.File, error) {
	abs, err := s.Resolve(relPath)
	if err != nil {
		return nil, err
	}
	return os.Open(abs)
}

// Resolve turns a stored relative path into an absolute one, refusing anything
// that would escape the blob root.
//
// Three separate checks, because no single one covers Windows: a rooted path
// such as "/etc/passwd" is not IsAbs on Windows (it has no volume), a path with
// a volume such as "C:\x" is absolute, and "../x" escapes while being neither.
func (s *Store) Resolve(relPath string) (string, error) {
	clean := filepath.Clean(filepath.FromSlash(relPath))

	rooted := strings.HasPrefix(clean, string(os.PathSeparator)) || strings.HasPrefix(clean, "/")
	escapes := clean == ".." || strings.HasPrefix(clean, ".."+string(os.PathSeparator))

	if filepath.IsAbs(clean) || filepath.VolumeName(clean) != "" || rooted || escapes {
		return "", fmt.Errorf("invalid blob path %q", relPath)
	}

	abs := filepath.Join(s.root, clean)
	if !strings.HasPrefix(abs, s.root+string(os.PathSeparator)) {
		return "", fmt.Errorf("invalid blob path %q", relPath)
	}
	return abs, nil
}

// Remove deletes a blob. Callers must first confirm no asset row still refers
// to the same hash, since blobs are shared between rows.
func (s *Store) Remove(relPath string) error {
	abs, err := s.Resolve(relPath)
	if err != nil {
		return err
	}
	if err := os.Remove(abs); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove blob: %w", err)
	}
	return nil
}

// Size reports a stored blob's size in bytes.
func (s *Store) Size(relPath string) (int64, error) {
	abs, err := s.Resolve(relPath)
	if err != nil {
		return 0, err
	}
	info, err := os.Stat(abs)
	if err != nil {
		return 0, err
	}
	return info.Size(), nil
}

func relPathFor(sum string) string {
	return sum[0:2] + "/" + sum[2:4] + "/" + sum
}

// contextReader aborts a long upload when the client disconnects, so a stalled
// multi-gigabyte transfer does not hold a file handle open indefinitely.
type contextReader struct {
	ctx context.Context
	r   io.Reader
}

func (c *contextReader) Read(p []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	return c.r.Read(p)
}
