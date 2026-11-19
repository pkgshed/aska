package vcs

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseRemoteRefs(t *testing.T) {
	// Arrange
	output, err := os.ReadFile(filepath.Join("testdata", "ls-remote.txt"))
	require.NoError(t, err)

	// Act
	refs, err := parseRemoteRefs(output)

	// Assert
	require.NoError(t, err)
	assert.Equal(t, "refs/heads/main", refs.Head)

	var names []string
	for ref := range refs.All() {
		names = append(names, ref.Name)
	}
	assert.Equal(t, []string{"refs/heads/feature/x", "refs/heads/main", "refs/tags/v1.0.0", "refs/tags/v2.0.0"}, names,
		"only branches and tags are kept")

	annotated, _ := refs.Lookup("refs/tags/v2.0.0")
	assert.Equal(t, Hash("5555555555555555555555555555555555555555"), annotated.Target())
	lightweight, _ := refs.Lookup("refs/tags/v1.0.0")
	assert.Equal(t, Hash("3333333333333333333333333333333333333333"), lightweight.Target())
}

func TestParseRemoteRefsIgnoresInvalidRefNames(t *testing.T) {
	// Arrange: a hostile remote advertises names git would never create.
	output := "1111111111111111111111111111111111111111\trefs/tags/v1.0.0\x1b]0;pwned\x07\n" +
		"2222222222222222222222222222222222222222\trefs/heads/-upload-pack=evil\n" +
		"3333333333333333333333333333333333333333\trefs/tags/v1.0.0\n"

	// Act
	refs, err := parseRemoteRefs([]byte(output))

	// Assert
	require.NoError(t, err)
	var names []string
	for ref := range refs.All() {
		names = append(names, ref.Name)
	}
	assert.Equal(t, []string{"refs/tags/v1.0.0"}, names)
}

func TestParseRemoteRefsRejectsMalformedOutput(t *testing.T) {
	inputs := map[string]string{
		"missing tab":    "1111111111111111111111111111111111111111 refs/heads/main\n",
		"short hash":     "1111\trefs/heads/main\n",
		"uppercase hash": "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA\trefs/heads/main\n",
	}

	for name, input := range inputs {
		t.Run(name, func(t *testing.T) {
			// Act
			_, err := parseRemoteRefs([]byte(input))

			// Assert
			require.Error(t, err)
		})
	}
}

func TestParseTreeKeepsPathsVerbatim(t *testing.T) {
	// Arrange
	output := "100644 blob 1111111111111111111111111111111111111111      12\tSKILL.md\x00" +
		"160000 commit 2222222222222222222222222222222222222222       -\tvendor/sub\x00" +
		"100644 blob 3333333333333333333333333333333333333333       1\tname with\ttab\x00"

	// Act
	entries, err := parseTree([]byte(output))

	// Assert
	require.NoError(t, err)
	require.Len(t, entries, 3)
	assert.Equal(t, TreeEntry{Mode: "100644", Type: "blob", Object: "1111111111111111111111111111111111111111", Size: 12, Path: "SKILL.md"}, entries[0])
	assert.Equal(t, int64(-1), entries[1].Size)
	assert.Equal(t, "name with\ttab", entries[2].Path)
}
