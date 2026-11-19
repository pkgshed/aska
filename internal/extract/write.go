package extract

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path"

	"github.com/pkgshed/aska/internal/digest"
)

// BlobReader streams the contents of entries from their source.
type BlobReader interface {
	// ReadBlobs calls visit once per id, in order, with a reader over that
	// object's contents. The reader is only valid during the call.
	ReadBlobs(ctx context.Context, ids []string, visit func(index int, content io.Reader) error) error
}

// Write validates entries with [Check] and writes them below dir, which must
// already exist and be empty. It returns the content digest of the written
// tree, computed from the bytes as they were written.
func Write(ctx context.Context, dir string, entries []Entry, blobs BlobReader, limits Limits) (digest.Digest, error) {
	if err := Check(entries, limits); err != nil {
		return digest.Digest{}, err
	}

	root, err := os.OpenRoot(dir)
	if err != nil {
		return digest.Digest{}, fmt.Errorf("open destination: %w", err)
	}
	defer root.Close()

	ids := make([]string, len(entries))
	for index, entry := range entries {
		ids[index] = entry.ID
	}

	files := make([]digest.File, len(entries))
	visit := func(index int, content io.Reader) error {
		file, err := writeEntry(root, entries[index], content)
		if err != nil {
			return entryError(entries[index].Path, err)
		}
		files[index] = file
		return nil
	}

	if err := blobs.ReadBlobs(ctx, ids, visit); err != nil {
		return digest.Digest{}, err
	}

	return digest.Of(files), nil
}

// writeEntry creates one file exclusively and copies exactly the announced
// number of bytes into it.
func writeEntry(root *os.Root, entry Entry, content io.Reader) (digest.File, error) {
	if parent := path.Dir(entry.Path); parent != "." {
		if err := root.MkdirAll(parent, 0o755); err != nil {
			return digest.File{}, fmt.Errorf("create directory: %w", err)
		}
	}

	handle, err := root.OpenFile(entry.Path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, permissions(entry.Kind))
	if err != nil {
		return digest.File{}, fmt.Errorf("create file: %w", err)
	}

	hash := sha256.New()
	copyErr := copyExactly(io.MultiWriter(handle, hash), content, entry.Size)
	closeErr := handle.Close()
	if err := errors.Join(copyErr, closeErr); err != nil {
		return digest.File{}, err
	}

	file := digest.File{Path: entry.Path, Executable: entry.Kind == KindExecutable}
	hash.Sum(file.Sum[:0])
	return file, nil
}

// copyExactly copies size bytes and fails if the source is shorter or longer.
func copyExactly(dst io.Writer, src io.Reader, size int64) error {
	written, err := io.CopyN(dst, src, size)
	switch {
	case errors.Is(err, io.EOF):
		return fmt.Errorf("%w: got %d of %d bytes", ErrSizeMismatch, written, size)
	case err != nil:
		return fmt.Errorf("write content: %w", err)
	}

	var probe [1]byte
	if n, _ := io.ReadFull(src, probe[:]); n > 0 {
		return fmt.Errorf("%w: content is longer than %d bytes", ErrSizeMismatch, size)
	}

	return nil
}

func permissions(kind Kind) os.FileMode {
	if kind == KindExecutable {
		return 0o755
	}
	return 0o644
}
