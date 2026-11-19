// Package digest computes the canonical content digest of a skill snapshot.
//
// The digest identifies a skill by its file contents alone. It covers every
// regular file below the skill root, its slash-separated relative path and
// whether it is executable. Directories, timestamps, ownership and other
// metadata do not contribute, so the same tree always yields the same digest
// regardless of where or when it was extracted.
//
// The canonical form is the SHA-256 of a summary that starts with a version
// line and lists one line per file, sorted by path:
//
//	aska-skill-digest-v1
//	100644 <sha256 of contents, hex> <path>
//	100755 <sha256 of contents, hex> <path>
package digest

import (
	"cmp"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"slices"
	"strings"
)

// summaryHeader versions the canonical summary format. Changing the format
// requires a new header so old and new digests can never collide.
const summaryHeader = "aska-skill-digest-v1\n"

// ErrInvalidDigest reports text that is not a hex-encoded SHA-256 digest.
var ErrInvalidDigest = errors.New("invalid digest")

// ErrUnsupportedFile reports a directory entry that is neither a regular file
// nor a directory, such as a symbolic link or device node.
var ErrUnsupportedFile = errors.New("unsupported file type")

// Digest is the SHA-256 content digest of a skill snapshot.
type Digest struct {
	sum   [sha256.Size]byte
	valid bool
}

// Parse decodes a lowercase hex-encoded SHA-256 digest.
func Parse(text string) (Digest, error) {
	if len(text) != hex.EncodedLen(sha256.Size) || strings.ToLower(text) != text {
		return Digest{}, fmt.Errorf("%w %q: want %d lowercase hex characters",
			ErrInvalidDigest, text, hex.EncodedLen(sha256.Size))
	}

	var digest Digest
	if _, err := hex.Decode(digest.sum[:], []byte(text)); err != nil {
		return Digest{}, fmt.Errorf("%w %q: %w", ErrInvalidDigest, text, err)
	}
	digest.valid = true

	return digest, nil
}

// String returns the lowercase hex encoding, or "" for the zero value.
func (d Digest) String() string {
	if !d.valid {
		return ""
	}
	return hex.EncodeToString(d.sum[:])
}

// Short returns an abbreviated form for display.
func (d Digest) Short() string {
	text := d.String()
	return text[:min(len(text), 12)]
}

// File describes one regular file of a snapshot.
type File struct {
	// Path is the slash-separated path relative to the skill root.
	Path string
	// Executable reports whether any execute permission bit is set.
	Executable bool
	// Sum is the SHA-256 of the file contents.
	Sum [sha256.Size]byte
}

// Of returns the digest of the given files. The order of files does not
// matter; the caller must ensure that paths are unique.
func Of(files []File) Digest {
	sorted := slices.SortedFunc(slices.Values(files), func(a, b File) int {
		return cmp.Compare(a.Path, b.Path)
	})

	summary := sha256.New()
	_, _ = io.WriteString(summary, summaryHeader)
	for _, file := range sorted {
		_, _ = fmt.Fprintf(summary, "%s %x %s\n", fileMode(file.Executable), file.Sum, file.Path)
	}

	var digest Digest
	summary.Sum(digest.sum[:0])
	digest.valid = true
	return digest
}

func fileMode(executable bool) string {
	if executable {
		return "100755"
	}
	return "100644"
}

// OfDir computes the digest of the directory tree rooted at dir. It fails
// with [ErrUnsupportedFile] when the tree contains anything other than
// regular files and directories, and never follows symbolic links.
func OfDir(dir string) (Digest, error) {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return Digest{}, fmt.Errorf("open snapshot: %w", err)
	}
	defer root.Close()

	files, err := collectFiles(root)
	if err != nil {
		return Digest{}, err
	}

	return Of(files), nil
}

func collectFiles(root *os.Root) ([]File, error) {
	var files []File

	walk := func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		if !entry.Type().IsRegular() {
			return fmt.Errorf("%w: %q is %s", ErrUnsupportedFile, name, entry.Type())
		}

		file, err := hashFile(root, name)
		if err != nil {
			return err
		}
		files = append(files, file)
		return nil
	}

	if err := fs.WalkDir(root.FS(), ".", walk); err != nil {
		return nil, fmt.Errorf("walk snapshot: %w", err)
	}

	return files, nil
}

func hashFile(root *os.Root, name string) (File, error) {
	handle, err := root.Open(name)
	if err != nil {
		return File{}, fmt.Errorf("open %q: %w", name, err)
	}
	defer handle.Close()

	info, err := handle.Stat()
	if err != nil {
		return File{}, fmt.Errorf("stat %q: %w", name, err)
	}

	hash := sha256.New()
	if _, err := io.Copy(hash, handle); err != nil {
		return File{}, fmt.Errorf("read %q: %w", name, err)
	}

	file := File{Path: name, Executable: info.Mode().Perm()&0o111 != 0}
	hash.Sum(file.Sum[:0])
	return file, nil
}
