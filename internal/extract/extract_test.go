package extract_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pkgshed/aska/internal/digest"
	"github.com/pkgshed/aska/internal/extract"
)

// memoryBlobs serves blob contents from memory, keyed by id.
type memoryBlobs map[string]string

func (m memoryBlobs) ReadBlobs(_ context.Context, ids []string, visit func(int, io.Reader) error) error {
	for index, id := range ids {
		content, found := m[id]
		if !found {
			return fmt.Errorf("blob %s not found", id)
		}
		if err := visit(index, strings.NewReader(content)); err != nil {
			return err
		}
	}
	return nil
}

func manifest() extract.Entry {
	return extract.Entry{Path: "SKILL.md", Kind: extract.KindFile, Size: 3, ID: "manifest"}
}

func file(path string) extract.Entry {
	return extract.Entry{Path: path, Kind: extract.KindFile, Size: 1, ID: "x"}
}

func TestCheckAcceptsOrdinaryTrees(t *testing.T) {
	// Arrange
	entries := []extract.Entry{
		manifest(),
		{Path: "scripts/run.sh", Kind: extract.KindExecutable, Size: 1, ID: "x"},
		file("references/.hidden/notes.md"),
		file(".gitignore"),
	}

	// Act
	err := extract.Check(entries, extract.DefaultLimits())

	// Assert
	require.NoError(t, err)
}

func TestCheckRejectsUnsafePaths(t *testing.T) {
	paths := []string{
		"../escape", "a/../../escape", "/etc/passwd", "a//b", "./a", "a/./b", "a/", "",
		".git/config", "nested/.GIT/hooks/post-checkout", "a\\..\\b", "bell\x07", "new\nline",
		"..", "a/..", string([]byte{0xff, 0xfe}),
	}

	for _, path := range paths {
		t.Run(fmt.Sprintf("%q", path), func(t *testing.T) {
			// Arrange
			entries := []extract.Entry{manifest(), file(path)}

			// Act
			err := extract.Check(entries, extract.DefaultLimits())

			// Assert
			require.ErrorIs(t, err, extract.ErrUnsafePath)
			var entryErr *extract.EntryError
			require.ErrorAs(t, err, &entryErr)
			assert.Equal(t, path, entryErr.Path)
		})
	}
}

func TestCheckRejectsUnsupportedEntries(t *testing.T) {
	kinds := []extract.Kind{extract.KindSymlink, extract.KindSubmodule, extract.KindUnsupported}

	for _, kind := range kinds {
		t.Run(kind.String(), func(t *testing.T) {
			// Arrange
			entries := []extract.Entry{manifest(), {Path: "link", Kind: kind, ID: "x"}}

			// Act
			err := extract.Check(entries, extract.DefaultLimits())

			// Assert
			require.ErrorIs(t, err, extract.ErrUnsupportedEntry)
		})
	}
}

func TestCheckRejectsConflictingPaths(t *testing.T) {
	tests := map[string][]string{
		"case collision":            {"README.md", "readme.md"},
		"manifest case collision":   {"skill.md"},
		"file above directory":      {"docs", "docs/a.md"},
		"directory above file":      {"docs/a.md", "docs"},
		"case-folded directory":     {"Docs/a.md", "docs"},
		"duplicate identical paths": {"a.md", "a.md"},
	}

	for name, paths := range tests {
		t.Run(name, func(t *testing.T) {
			// Arrange
			entries := []extract.Entry{manifest()}
			for _, path := range paths {
				entries = append(entries, file(path))
			}

			// Act
			err := extract.Check(entries, extract.DefaultLimits())

			// Assert
			require.ErrorIs(t, err, extract.ErrPathConflict)
		})
	}
}

func TestCheckEnforcesLimits(t *testing.T) {
	limits := extract.Limits{MaxFiles: 3, MaxFileBytes: 10, MaxTotalBytes: 15, MaxDepth: 3, MaxPathBytes: 20}

	tests := map[string][]extract.Entry{
		"too many files":  {manifest(), file("a"), file("b"), file("c")},
		"file too large":  {manifest(), {Path: "big", Kind: extract.KindFile, Size: 11}},
		"total too large": {manifest(), {Path: "a", Kind: extract.KindFile, Size: 10}, {Path: "b", Kind: extract.KindFile, Size: 3}},
		"path too deep":   {manifest(), file("a/b/c/d")},
		"path too long":   {manifest(), file(strings.Repeat("x", 21))},
	}

	for name, entries := range tests {
		t.Run(name, func(t *testing.T) {
			// Act
			err := extract.Check(entries, limits)

			// Assert
			require.ErrorIs(t, err, extract.ErrLimitExceeded)
		})
	}
}

func TestCheckRequiresManifest(t *testing.T) {
	tests := map[string][]extract.Entry{
		"absent":      {file("README.md")},
		"nested":      {file("docs/SKILL.md")},
		"not a file":  {{Path: "SKILL.md", Kind: extract.KindSymlink}},
		"empty skill": nil,
	}

	for name, entries := range tests {
		t.Run(name, func(t *testing.T) {
			// Act
			err := extract.Check(entries, extract.DefaultLimits())

			// Assert
			require.Error(t, err)
			isMissing := errors.Is(err, extract.ErrMissingManifest)
			isUnsupported := errors.Is(err, extract.ErrUnsupportedEntry)
			assert.True(t, isMissing || isUnsupported, "unexpected error: %v", err)
		})
	}
}

func TestWriteMaterializesFilesAndDigest(t *testing.T) {
	// Arrange
	dir := t.TempDir()
	blobs := memoryBlobs{"manifest": "---\n", "script": "#!/bin/sh\n"}
	entries := []extract.Entry{
		{Path: "SKILL.md", Kind: extract.KindFile, Size: 4, ID: "manifest"},
		{Path: "scripts/run.sh", Kind: extract.KindExecutable, Size: 10, ID: "script"},
	}

	// Act
	sum, err := extract.Write(context.Background(), dir, entries, blobs, extract.DefaultLimits())

	// Assert
	require.NoError(t, err)
	onDisk, err := digest.OfDir(dir)
	require.NoError(t, err)
	assert.Equal(t, onDisk, sum)

	content, err := os.ReadFile(filepath.Join(dir, "scripts", "run.sh"))
	require.NoError(t, err)
	assert.Equal(t, "#!/bin/sh\n", string(content))

	info, err := os.Stat(filepath.Join(dir, "scripts", "run.sh"))
	require.NoError(t, err)
	assert.NotZero(t, info.Mode().Perm()&0o100, "script should be executable")
}

func TestWriteRejectsContentOfUnexpectedSize(t *testing.T) {
	tests := map[string]string{"shorter": "--", "longer": "-----"}

	for name, content := range tests {
		t.Run(name, func(t *testing.T) {
			// Arrange
			dir := t.TempDir()
			entries := []extract.Entry{{Path: "SKILL.md", Kind: extract.KindFile, Size: 4, ID: "manifest"}}

			// Act
			_, err := extract.Write(context.Background(), dir, entries, memoryBlobs{"manifest": content},
				extract.DefaultLimits())

			// Assert
			require.ErrorIs(t, err, extract.ErrSizeMismatch)
		})
	}
}

func TestWriteDoesNotFollowSymlinksInDestination(t *testing.T) {
	// Arrange: a hostile destination that already contains a link outside.
	outside := t.TempDir()
	dir := t.TempDir()
	require.NoError(t, os.Symlink(outside, filepath.Join(dir, "docs")))
	entries := []extract.Entry{
		{Path: "SKILL.md", Kind: extract.KindFile, Size: 4, ID: "manifest"},
		{Path: "docs/escape.md", Kind: extract.KindFile, Size: 4, ID: "manifest"},
	}

	// Act
	_, err := extract.Write(context.Background(), dir, entries, memoryBlobs{"manifest": "---\n"},
		extract.DefaultLimits())

	// Assert
	require.Error(t, err)
	_, statErr := os.Stat(filepath.Join(outside, "escape.md"))
	assert.ErrorIs(t, statErr, os.ErrNotExist, "nothing may be written outside the destination")
}

func TestWriteValidatesBeforeWriting(t *testing.T) {
	// Arrange
	dir := t.TempDir()
	entries := []extract.Entry{manifest(), file("../escape")}

	// Act
	_, err := extract.Write(context.Background(), dir, entries, memoryBlobs{}, extract.DefaultLimits())

	// Assert
	require.ErrorIs(t, err, extract.ErrUnsafePath)
	remaining, readErr := os.ReadDir(dir)
	require.NoError(t, readErr)
	assert.Empty(t, remaining)
}
