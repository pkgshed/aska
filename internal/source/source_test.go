package source_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pkgshed/aska/internal/source"
)

func TestParseRepoURLCanonicalizes(t *testing.T) {
	tests := []struct {
		input string
		url   string
		key   string
	}{
		{"https://github.com/acme/skills.git", "https://github.com/acme/skills.git", "github.com/acme/skills.git"},
		{"HTTPS://GitHub.COM/acme/skills/", "https://github.com/acme/skills", "github.com/acme/skills.git"},
		{"https://example.com:443/repo.git", "https://example.com/repo.git", "example.com/repo.git"},
		{"https://example.com:8443/repo.git", "https://example.com:8443/repo.git", "example.com%2B8443/repo.git"},
		{"ssh://git@github.com/Acme/Skills.git", "ssh://git@github.com/Acme/Skills.git", "github.com/!acme/!skills.git"},
		{"https://example.com/a+b/repo", "https://example.com/a+b/repo", "example.com/a%2Bb/repo.git"},
	}

	for _, test := range tests {
		t.Run(test.input, func(t *testing.T) {
			// Act
			repo, err := source.ParseRepoURL(test.input, source.Policy{})

			// Assert
			require.NoError(t, err)
			assert.Equal(t, test.url, repo.URL())
			assert.Equal(t, test.key, repo.CacheKey())
		})
	}
}

func TestParseRepoURLRejectsUnsafeInput(t *testing.T) {
	tests := map[string]error{
		"http://example.com/repo.git":           source.ErrInsecureScheme,
		"git://example.com/repo.git":            source.ErrInsecureScheme,
		"file:///etc/repo.git":                  source.ErrInsecureScheme,
		"ext::sh -c touch% /tmp/pwned":          source.ErrInsecureScheme,
		"https://-oProxyCommand=evil/repo.git":  source.ErrInvalidRepo,
		"https://user:secret@example.com/r.git": source.ErrInvalidRepo,
		"https://example.com/repo.git?x=1":      source.ErrInvalidRepo,
		"https://example.com/a/../b.git":        source.ErrInvalidRepo,
		"https://example.com":                   source.ErrInvalidRepo,
		"git@github.com:acme/skills.git":        source.ErrInvalidRepo,
	}

	for input, want := range tests {
		t.Run(input, func(t *testing.T) {
			// Act
			_, err := source.ParseRepoURL(input, source.Policy{})

			// Assert
			require.ErrorIs(t, err, want)
		})
	}
}

func TestParseRepoURLAllowsInsecureSchemesByPolicy(t *testing.T) {
	// Act
	repo, err := source.ParseRepoURL("http://forge.lan/repo.git", source.Policy{AllowInsecure: true})

	// Assert
	require.NoError(t, err)
	assert.Equal(t, "git+http://forge.lan/repo.git", repo.String())
}

func TestCacheKeysStayDistinct(t *testing.T) {
	// Arrange: names that collide on case-insensitive filesystems.
	upper, err := source.ParseRepoURL("https://github.com/Acme/skills.git", source.Policy{})
	require.NoError(t, err)
	lower, err := source.ParseRepoURL("https://github.com/acme/skills.git", source.Policy{})
	require.NoError(t, err)

	// Act and assert
	assert.NotEqual(t, strings.ToLower(upper.CacheKey()), strings.ToLower(lower.CacheKey()))
}

func TestParsePath(t *testing.T) {
	valid := map[string]source.Path{
		"":                         source.RootPath,
		".":                        source.RootPath,
		"skills/code-review":       "skills/code-review",
		".claude/skills/my_skill~": ".claude/skills/my_skill~",
	}
	for input, want := range valid {
		t.Run("valid "+input, func(t *testing.T) {
			// Act
			got, err := source.ParsePath(input)

			// Assert
			require.NoError(t, err)
			assert.Equal(t, want, got)
		})
	}

	invalid := []string{"/abs", "a/../b", "..", "a//b", "a/", ".git/x", "a b", "a:b", "a^{tree}", "a\\b"}
	for _, input := range invalid {
		t.Run("invalid "+input, func(t *testing.T) {
			// Act
			_, err := source.ParsePath(input)

			// Assert
			require.ErrorIs(t, err, source.ErrInvalidPath)
		})
	}
}

func TestStaticRoot(t *testing.T) {
	tests := []struct {
		importPath string
		repoURL    string
		skillPath  source.Path
	}{
		{"github.com/acme/skills", "https://github.com/acme/skills.git", source.RootPath},
		{"github.com/acme/skills/skills/code-review", "https://github.com/acme/skills.git", "skills/code-review"},
		{"github.com/acme/skills.git/x", "https://github.com/acme/skills.git", "x"},
		{"codeberg.org/acme/skills/.agents/skills/x", "https://codeberg.org/acme/skills.git", ".agents/skills/x"},
		{"gitlab.com/group/sub/repo.git/skills/x", "https://gitlab.com/group/sub/repo.git", "skills/x"},
		{"example.com/repo.git", "https://example.com/repo.git", source.RootPath},
	}

	for _, test := range tests {
		t.Run(test.importPath, func(t *testing.T) {
			// Arrange
			importPath, err := source.ParseImportPath(test.importPath)
			require.NoError(t, err)

			// Act
			pkg, matched, err := source.StaticRoot(importPath)

			// Assert
			require.NoError(t, err)
			require.True(t, matched)
			assert.Equal(t, test.repoURL, pkg.Repo.URL())
			assert.Equal(t, test.skillPath, pkg.Path)
		})
	}
}

func TestStaticRootDefersUnknownHosts(t *testing.T) {
	// Arrange
	importPath, err := source.ParseImportPath("example.com/skills/code-review")
	require.NoError(t, err)

	// Act
	_, matched, err := source.StaticRoot(importPath)

	// Assert
	require.NoError(t, err)
	assert.False(t, matched)
}

func TestStaticRootRejectsIncompleteWellKnownPaths(t *testing.T) {
	// Arrange
	importPath, err := source.ParseImportPath("github.com/acme")
	require.NoError(t, err)

	// Act
	_, matched, err := source.StaticRoot(importPath)

	// Assert
	assert.True(t, matched)
	require.ErrorIs(t, err, source.ErrInvalidImportPath)
}

func TestParseImportPathRejectsInvalidInput(t *testing.T) {
	inputs := []string{"", "github.com", "localhost/repo", "-evil.com/x", "Example..com/x", "example.com/../x", "example.com/a b"}

	for _, input := range inputs {
		t.Run(input, func(t *testing.T) {
			// Act
			_, err := source.ParseImportPath(input)

			// Assert
			require.ErrorIs(t, err, source.ErrInvalidImportPath)
		})
	}
}

func TestParseReference(t *testing.T) {
	tests := []struct {
		input      string
		importPath source.ImportPath
		repoURL    string
		skillPath  source.Path
		query      string
	}{
		{input: "github.com/acme/skills/x@^1.2", importPath: "github.com/acme/skills/x", query: "^1.2"},
		{input: "github.com/acme/skills/x", importPath: "github.com/acme/skills/x"},
		{
			input:   "git+https://example.com/repo.git//skills/x@v1.2.3",
			repoURL: "https://example.com/repo.git", skillPath: "skills/x", query: "v1.2.3",
		},
		{
			input:   "git+ssh://git@example.com/repo.git//skills/x",
			repoURL: "ssh://git@example.com/repo.git", skillPath: "skills/x",
		},
		{input: "git+https://example.com/repo.git@main", repoURL: "https://example.com/repo.git", skillPath: ".", query: "main"},
	}

	for _, test := range tests {
		t.Run(test.input, func(t *testing.T) {
			// Act
			reference, err := source.ParseReference(test.input, source.Policy{})

			// Assert
			require.NoError(t, err)
			assert.Equal(t, test.query, reference.Query)
			if test.importPath != "" {
				assert.Equal(t, test.importPath, reference.ImportPath)
				assert.False(t, reference.IsResolved())
				return
			}
			require.True(t, reference.IsResolved())
			assert.Equal(t, test.repoURL, reference.Package.Repo.URL())
			assert.Equal(t, test.skillPath, reference.Package.Path)
		})
	}
}

func TestPackageIdentity(t *testing.T) {
	// Arrange
	repo, err := source.ParseRepoURL("https://github.com/acme/agent-skills.git", source.Policy{})
	require.NoError(t, err)
	pkg := source.Package{Repo: repo, Path: "skills/code-review"}
	root := source.Package{Repo: repo, Path: source.RootPath}

	// Act and assert
	assert.Equal(t, "github.com/acme/agent-skills/skills/code-review", pkg.String())
	assert.Equal(t, "git+https://github.com/acme/agent-skills.git//skills/code-review", pkg.ID())
	assert.Equal(t, "github.com+acme+agent-skills.git@skills+code-review", pkg.CacheKey())
	assert.Equal(t, "github.com+acme+agent-skills.git@", root.CacheKey())
	assert.NotEqual(t, pkg.CacheKey(), root.CacheKey())
}

func TestPackageCacheKeyBoundsLength(t *testing.T) {
	// Arrange
	repo, err := source.ParseRepoURL("https://github.com/acme/skills.git", source.Policy{})
	require.NoError(t, err)
	long := source.Path(strings.Repeat("deeply-nested/", 20) + "skill")
	other := source.Path(strings.Repeat("deeply-nested/", 20) + "other")

	// Act
	key := source.Package{Repo: repo, Path: long}.CacheKey()
	otherKey := source.Package{Repo: repo, Path: other}.CacheKey()

	// Assert
	assert.LessOrEqual(t, len(key), 160)
	assert.NotEqual(t, key, otherKey)
}
