// Package extract validates untrusted skill trees and writes them to disk.
//
// Entries come from a remote repository and are treated as hostile. Before
// any byte reaches the filesystem, [Check] rejects unsafe paths, links,
// submodules, case-insensitive collisions and trees that exceed the
// configured limits. [Write] then materializes the files below a directory
// through an [os.Root], which independently prevents writes that escape it.
package extract

import (
	"errors"
	"fmt"
	"path"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/pkgshed/aska/internal/skill"
)

var (
	// ErrUnsafePath reports an entry path that could escape the destination
	// or is not portable.
	ErrUnsafePath = errors.New("unsafe path")

	// ErrUnsupportedEntry reports a symbolic link, submodule or other
	// entry that is not a regular file.
	ErrUnsupportedEntry = errors.New("unsupported entry")

	// ErrPathConflict reports two entries that would occupy the same place
	// on disk, including names that differ only in letter case.
	ErrPathConflict = errors.New("conflicting paths")

	// ErrLimitExceeded reports a tree that is larger than the limits allow.
	ErrLimitExceeded = errors.New("limit exceeded")

	// ErrMissingManifest reports a tree without a top-level SKILL.md file.
	ErrMissingManifest = errors.New("missing " + skill.ManifestName)

	// ErrSizeMismatch reports content whose length differs from the size
	// announced by its entry.
	ErrSizeMismatch = errors.New("content size mismatch")
)

// Kind classifies an entry of a skill tree.
type Kind int

const (
	// KindFile is a regular, non-executable file.
	KindFile Kind = iota
	// KindExecutable is a regular file with execute permission.
	KindExecutable
	// KindSymlink is a symbolic link.
	KindSymlink
	// KindSubmodule is a nested repository reference.
	KindSubmodule
	// KindUnsupported is any other entry type.
	KindUnsupported
)

func (k Kind) String() string {
	switch k {
	case KindFile:
		return "file"
	case KindExecutable:
		return "executable file"
	case KindSymlink:
		return "symbolic link"
	case KindSubmodule:
		return "submodule"
	default:
		return "unsupported entry"
	}
}

func (k Kind) isRegular() bool { return k == KindFile || k == KindExecutable }

// Entry is one item of a skill tree as reported by its source.
type Entry struct {
	// Path is the slash-separated path relative to the skill root.
	Path string
	// Kind is the entry type.
	Kind Kind
	// Size is the announced content length in bytes.
	Size int64
	// ID identifies the content for a [BlobReader].
	ID string
}

// EntryError attaches the offending path to a validation failure.
type EntryError struct {
	Path string
	Err  error
}

func (e *EntryError) Error() string { return fmt.Sprintf("%q: %v", e.Path, e.Err) }

func (e *EntryError) Unwrap() error { return e.Err }

func entryError(entryPath string, err error) error {
	return &EntryError{Path: entryPath, Err: err}
}

// Limits bounds the size and shape of a skill tree.
type Limits struct {
	// MaxFiles is the maximum number of files.
	MaxFiles int
	// MaxFileBytes is the maximum size of a single file.
	MaxFileBytes int64
	// MaxTotalBytes is the maximum combined size of all files.
	MaxTotalBytes int64
	// MaxDepth is the maximum number of path elements.
	MaxDepth int
	// MaxPathBytes is the maximum length of a path.
	MaxPathBytes int
}

// DefaultLimits returns limits generous enough for any reasonable skill.
func DefaultLimits() Limits {
	return Limits{
		MaxFiles:      1000,
		MaxFileBytes:  8 << 20,
		MaxTotalBytes: 32 << 20,
		MaxDepth:      16,
		MaxPathBytes:  512,
	}
}

// Check validates entries without touching the filesystem. It returns the
// first problem found, wrapped in an [*EntryError] when a path is to blame.
func Check(entries []Entry, limits Limits) error {
	if len(entries) > limits.MaxFiles {
		return fmt.Errorf("%w: %d files, at most %d allowed", ErrLimitExceeded, len(entries), limits.MaxFiles)
	}

	var total int64
	layout := newLayout()
	for _, entry := range entries {
		if err := checkEntry(entry, limits); err != nil {
			return entryError(entry.Path, err)
		}

		total += entry.Size
		if total > limits.MaxTotalBytes {
			return fmt.Errorf("%w: more than %d bytes in total", ErrLimitExceeded, limits.MaxTotalBytes)
		}

		if err := layout.add(entry.Path); err != nil {
			return entryError(entry.Path, err)
		}
	}

	if !hasManifest(entries) {
		return ErrMissingManifest
	}

	return nil
}

func checkEntry(entry Entry, limits Limits) error {
	if err := checkPath(entry.Path, limits); err != nil {
		return err
	}
	if !entry.Kind.isRegular() {
		return fmt.Errorf("%w: %s", ErrUnsupportedEntry, entry.Kind)
	}
	if entry.Size < 0 {
		return fmt.Errorf("%w: negative size", ErrSizeMismatch)
	}
	if entry.Size > limits.MaxFileBytes {
		return fmt.Errorf("%w: %d bytes, at most %d allowed per file", ErrLimitExceeded, entry.Size, limits.MaxFileBytes)
	}
	return nil
}

// checkPath accepts only clean, relative, slash-separated paths whose
// elements are neither special nor control characters.
func checkPath(name string, limits Limits) error {
	switch {
	case name == "":
		return fmt.Errorf("%w: empty path", ErrUnsafePath)
	case len(name) > limits.MaxPathBytes:
		return fmt.Errorf("%w: longer than %d bytes", ErrLimitExceeded, limits.MaxPathBytes)
	case !utf8.ValidString(name):
		return fmt.Errorf("%w: not valid UTF-8", ErrUnsafePath)
	case strings.ContainsFunc(name, isForbiddenRune):
		return fmt.Errorf("%w: contains a control character or backslash", ErrUnsafePath)
	case path.IsAbs(name):
		return fmt.Errorf("%w: absolute path", ErrUnsafePath)
	case path.Clean(name) != name:
		return fmt.Errorf("%w: not in canonical form", ErrUnsafePath)
	}

	elements := strings.Split(name, "/")
	if len(elements) > limits.MaxDepth {
		return fmt.Errorf("%w: deeper than %d levels", ErrLimitExceeded, limits.MaxDepth)
	}
	for _, element := range elements {
		if err := checkElement(element); err != nil {
			return err
		}
	}

	return nil
}

func checkElement(element string) error {
	switch {
	case element == "." || element == "..":
		return fmt.Errorf("%w: %q element", ErrUnsafePath, element)
	case strings.EqualFold(element, ".git"):
		return fmt.Errorf("%w: reserved %q element", ErrUnsafePath, element)
	default:
		return nil
	}
}

func isForbiddenRune(r rune) bool {
	return r == '\\' || unicode.IsControl(r)
}

func hasManifest(entries []Entry) bool {
	for _, entry := range entries {
		if entry.Path == skill.ManifestName && entry.Kind.isRegular() {
			return true
		}
	}
	return false
}

// layout tracks the files and directories a tree would create, folded to
// lower case so that names colliding on case-insensitive filesystems are
// detected on every platform.
type layout struct {
	files       map[string]string
	directories map[string]string
}

func newLayout() *layout {
	return &layout{files: map[string]string{}, directories: map[string]string{}}
}

func (l *layout) add(name string) error {
	folded := strings.ToLower(name)
	if previous, taken := l.files[folded]; taken {
		return fmt.Errorf("%w: collides with %q", ErrPathConflict, previous)
	}
	if previous, taken := l.directories[folded]; taken {
		return fmt.Errorf("%w: file collides with directory %q", ErrPathConflict, previous)
	}

	for parent := path.Dir(name); parent != "."; parent = path.Dir(parent) {
		foldedParent := strings.ToLower(parent)
		if previous, taken := l.files[foldedParent]; taken {
			return fmt.Errorf("%w: directory collides with file %q", ErrPathConflict, previous)
		}
		l.directories[foldedParent] = parent
	}

	l.files[folded] = name
	return nil
}
