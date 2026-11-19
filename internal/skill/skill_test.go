package skill_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pkgshed/aska/internal/skill"
)

func readFixture(t *testing.T, name string) []byte {
	t.Helper()
	content, err := os.ReadFile(filepath.Join("testdata", name))
	require.NoError(t, err)
	return content
}

func TestParseReadsFrontmatter(t *testing.T) {
	tests := []struct {
		fixture string
		want    skill.Metadata
	}{
		{
			fixture: "valid.md",
			want: skill.Metadata{
				Name:        "code-review",
				Description: "Reviews pull requests for correctness.",
				License:     "MIT",
			},
		},
		{fixture: "crlf.md", want: skill.Metadata{Name: "crlf-skill", Description: "Written on Windows."}},
		{fixture: "bom.md", want: skill.Metadata{Name: "bom-skill", Description: "Starts with a byte order mark."}},
	}

	for _, test := range tests {
		t.Run(test.fixture, func(t *testing.T) {
			// Arrange
			content := readFixture(t, test.fixture)

			// Act
			metadata, err := skill.Parse(content)

			// Assert
			require.NoError(t, err)
			assert.Equal(t, test.want, metadata)
		})
	}
}

func TestParseRejectsInvalidManifests(t *testing.T) {
	tests := []struct {
		fixture string
		reason  string
	}{
		{fixture: "missing-frontmatter.md", reason: "missing frontmatter"},
		{fixture: "unterminated.md", reason: "unterminated frontmatter"},
		{fixture: "invalid-yaml.md", reason: "decode frontmatter"},
		{fixture: "numeric-name.md", reason: "decode frontmatter"},
		{fixture: "duplicate-keys.md", reason: "decode frontmatter"},
		{fixture: "invalid-utf8.md", reason: "not valid UTF-8"},
	}

	for _, test := range tests {
		t.Run(test.fixture, func(t *testing.T) {
			// Arrange
			content := readFixture(t, test.fixture)

			// Act
			_, err := skill.Parse(content)

			// Assert
			require.ErrorIs(t, err, skill.ErrInvalidManifest)
			assert.Contains(t, err.Error(), test.reason)
		})
	}
}

func TestParseRejectsOversizedFrontmatter(t *testing.T) {
	// Arrange
	content := "---\nname: big\ndescription: " + strings.Repeat("x", 70<<10) + "\n---\n"

	// Act
	_, err := skill.Parse([]byte(content))

	// Assert
	require.ErrorIs(t, err, skill.ErrInvalidManifest)
	assert.Contains(t, err.Error(), "exceeds")
}

func TestValidateName(t *testing.T) {
	tests := []struct {
		name  string
		valid bool
	}{
		{name: "code-review", valid: true},
		{name: "a", valid: true},
		{name: "pdf2text", valid: true},
		{name: strings.Repeat("a", skill.MaxNameLength), valid: true},
		{name: strings.Repeat("a", skill.MaxNameLength+1), valid: false},
		{name: "", valid: false},
		{name: "Code-Review", valid: false},
		{name: "-leading", valid: false},
		{name: "trailing-", valid: false},
		{name: "double--hyphen", valid: false},
		{name: "under_score", valid: false},
		{name: "..", valid: false},
		{name: "a/b", valid: false},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			// Act
			err := skill.ValidateName(test.name)

			// Assert
			if test.valid {
				require.NoError(t, err)
				return
			}
			require.ErrorIs(t, err, skill.ErrInvalidName)
		})
	}
}
