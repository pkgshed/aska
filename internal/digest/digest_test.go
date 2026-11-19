package digest_test

import (
	"crypto/sha256"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pkgshed/aska/internal/digest"
)

func file(path, content string, executable bool) digest.File {
	return digest.File{Path: path, Executable: executable, Sum: sha256.Sum256([]byte(content))}
}

func TestOfIgnoresFileOrder(t *testing.T) {
	// Arrange
	first := []digest.File{file("SKILL.md", "a", false), file("scripts/run.sh", "b", true)}
	second := []digest.File{first[1], first[0]}

	// Act
	a, b := digest.Of(first), digest.Of(second)

	// Assert
	assert.Equal(t, a, b)
}

func TestOfDistinguishesContentPathAndMode(t *testing.T) {
	// Arrange
	base := digest.Of([]digest.File{file("SKILL.md", "a", false)})
	variants := map[string][]digest.File{
		"content":    {file("SKILL.md", "b", false)},
		"path":       {file("skill.md", "a", false)},
		"executable": {file("SKILL.md", "a", true)},
		"extra file": {file("SKILL.md", "a", false), file("x", "", false)},
	}

	for name, files := range variants {
		t.Run(name, func(t *testing.T) {
			// Act
			variant := digest.Of(files)

			// Assert
			assert.NotEqual(t, base, variant)
		})
	}
}

func TestOfDirMatchesOf(t *testing.T) {
	// Arrange
	dir := t.TempDir()
	writeFile(t, dir, "SKILL.md", "manifest", 0o644)
	writeFile(t, dir, "scripts/run.sh", "#!/bin/sh\n", 0o755)
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "empty"), 0o755))
	want := digest.Of([]digest.File{
		file("SKILL.md", "manifest", false),
		file("scripts/run.sh", "#!/bin/sh\n", true),
	})

	// Act
	got, err := digest.OfDir(dir)

	// Assert
	require.NoError(t, err)
	assert.Equal(t, want, got)
}

func TestOfDirRejectsSymlinks(t *testing.T) {
	// Arrange
	dir := t.TempDir()
	writeFile(t, dir, "SKILL.md", "manifest", 0o644)
	require.NoError(t, os.Symlink("/etc/passwd", filepath.Join(dir, "link")))

	// Act
	_, err := digest.OfDir(dir)

	// Assert
	require.ErrorIs(t, err, digest.ErrUnsupportedFile)
}

func TestParseRoundTrip(t *testing.T) {
	// Arrange
	original := digest.Of([]digest.File{file("SKILL.md", "x", false)})

	// Act
	parsed, err := digest.Parse(original.String())

	// Assert
	require.NoError(t, err)
	assert.Equal(t, original, parsed)
	assert.Len(t, original.String(), 64)
}

func TestParseRejectsMalformedText(t *testing.T) {
	inputs := []string{
		"", "abc", "sha256:" + string(make([]byte, 64)), string(make([]byte, 64)),
		"ABCDEF0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF0123456789",
	}

	for _, input := range inputs {
		// Act
		_, err := digest.Parse(input)

		// Assert
		require.ErrorIs(t, err, digest.ErrInvalidDigest, "input %q", input)
	}
}

func writeFile(t *testing.T, dir, name, content string, mode os.FileMode) {
	t.Helper()
	path := filepath.Join(dir, filepath.FromSlash(name))
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(content), mode))
	require.NoError(t, os.Chmod(path, mode))
}
