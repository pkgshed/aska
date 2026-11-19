package resolve_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pkgshed/aska/internal/resolve"
	"github.com/pkgshed/aska/internal/source"
	"github.com/pkgshed/aska/internal/vcs"
)

// hash returns a distinct fake object id for a label.
func hash(label string) vcs.Hash {
	sum := fmt.Sprintf("%x", label)
	return vcs.Hash((sum + strings.Repeat("0", 40))[:40])
}

func lightweight(name, commit string) vcs.RemoteRef {
	return vcs.RemoteRef{Name: vcs.TagPrefix + name, Object: hash(commit)}
}

func annotated(name, commit string) vcs.RemoteRef {
	return vcs.RemoteRef{Name: vcs.TagPrefix + name, Object: hash("tag:" + name), Peeled: hash(commit)}
}

func branch(name, commit string) vcs.RemoteRef {
	return vcs.RemoteRef{Name: vcs.BranchPrefix + name, Object: hash(commit)}
}

func refs(list ...vcs.RemoteRef) vcs.RemoteRefs {
	return vcs.NewRemoteRefs("refs/heads/main", list...)
}

func query(t *testing.T, text string) resolve.Query {
	t.Helper()
	parsed, err := resolve.ParseQuery(text)
	require.NoError(t, err)
	return parsed
}

func TestParseQuery(t *testing.T) {
	tests := map[string]string{
		"":                      "resolve.Latest",
		"latest":                "resolve.Latest",
		"v1.2.3":                "resolve.Exact",
		"1.2.3":                 "resolve.Exact",
		"v1.2.3-rc.1":           "resolve.Exact",
		"v1":                    "resolve.Range",
		"1.2":                   "resolve.Range",
		"^1.2":                  "resolve.Range",
		"~1.2.3":                "resolve.Range",
		">=1.2 <2":              "resolve.Range",
		"main":                  "resolve.Revision",
		"feature/x":             "resolve.Revision",
		"refs/tags/v1.0.0":      "resolve.Revision",
		"deadbeef":              "resolve.Revision",
		"1234567":               "resolve.Revision",
		strings.Repeat("a", 40): "resolve.Commit",
		strings.Repeat("b", 64): "resolve.Commit",
	}

	for input, want := range tests {
		t.Run(input, func(t *testing.T) {
			// Act
			parsed, err := resolve.ParseQuery(input)

			// Assert
			require.NoError(t, err)
			assert.Equal(t, want, fmt.Sprintf("%T", parsed))
		})
	}
}

func TestParseQueryRejectsInvalidRefNames(t *testing.T) {
	inputs := []string{"-upload-pack=evil", "a..b", "a b", "a~1", "a^", "a:b", "a?", "a*", "a[", "a@{1}", "x.lock", "/x", "x/", ".x", "^"}

	for _, input := range inputs {
		t.Run(input, func(t *testing.T) {
			// Act
			_, err := resolve.ParseQuery(input)

			// Assert
			require.ErrorIs(t, err, resolve.ErrInvalidQuery)
		})
	}
}

func TestResolveSelectsHighestStableRelease(t *testing.T) {
	// Arrange
	available := refs(
		lightweight("v1.0.0", "c1"),
		annotated("v1.2.0", "c2"),
		annotated("v2.0.0-rc.1", "c3"),
		lightweight("not-a-version", "c4"),
		lightweight("v1.3", "c5"),
	)

	// Act
	resolution, err := resolve.Resolve(available, source.RootPath, resolve.Latest{})

	// Assert
	require.NoError(t, err)
	assert.Equal(t, "v1.2.0", resolution.Version.String())
	assert.Equal(t, "v1.2.0", resolution.Tag)
	assert.Equal(t, hash("c2"), resolution.Commit, "annotated tags resolve to the peeled commit")
	assert.Equal(t, "refs/tags/v1.2.0", resolution.FetchRef())
}

func TestResolveConstraints(t *testing.T) {
	available := refs(
		lightweight("v1.0.0", "c100"),
		lightweight("v1.4.2", "c142"),
		annotated("v1.5.0", "c150"),
		lightweight("v2.0.0", "c200"),
		lightweight("v2.1.0-beta.1", "c210b"),
	)

	tests := map[string]string{
		"^1.4":          "v1.5.0",
		"~1.4":          "v1.4.2",
		"v1":            "v1.5.0",
		"1.0":           "v1.0.0",
		">=1.4.2 <2":    "v1.5.0",
		"v1.4.2":        "v1.4.2",
		"v2.1.0-beta.1": "v2.1.0-beta.1",
		"^2":            "v2.0.0",
	}

	for text, want := range tests {
		t.Run(text, func(t *testing.T) {
			// Act
			resolution, err := resolve.Resolve(available, source.RootPath, query(t, text))

			// Assert
			require.NoError(t, err)
			assert.Equal(t, want, resolution.Version.String())
		})
	}
}

func TestResolveReportsMissingVersions(t *testing.T) {
	// Arrange
	available := refs(lightweight("v1.0.0", "c1"))

	// Act
	_, rangeErr := resolve.Resolve(available, source.RootPath, query(t, "^2"))
	_, exactErr := resolve.Resolve(available, source.RootPath, query(t, "v1.0.1"))

	// Assert
	require.ErrorIs(t, rangeErr, resolve.ErrNoMatchingVersion)
	require.ErrorIs(t, exactErr, resolve.ErrNoMatchingVersion)
}

func TestResolveWithoutReleasesSuggestsDefaultBranch(t *testing.T) {
	// Arrange
	available := refs(branch("main", "c1"))

	// Act
	_, err := resolve.Resolve(available, source.RootPath, resolve.Latest{})

	// Assert
	require.ErrorIs(t, err, resolve.ErrNoRelease)
	assert.Contains(t, err.Error(), "@main")
}

func TestResolveWithOnlyPrereleases(t *testing.T) {
	// Arrange
	available := refs(lightweight("v1.0.0-rc.1", "c1"))

	// Act
	_, err := resolve.Resolve(available, source.RootPath, resolve.Latest{})

	// Assert
	require.ErrorIs(t, err, resolve.ErrNoRelease)
	assert.Contains(t, err.Error(), "only prereleases")
}

func TestResolvePrefersPerSkillTags(t *testing.T) {
	// Arrange: a monorepo with repository-wide releases and per-skill tags
	// for one of its skills.
	available := refs(
		lightweight("v3.0.0", "repo300"),
		annotated("skills/review/v1.0.0", "review100"),
		annotated("skills/review/v1.1.0", "review110"),
		annotated("skills/other/v9.0.0", "other900"),
	)

	tests := []struct {
		skill string
		want  string
		tag   string
	}{
		{skill: "skills/review", want: "v1.1.0", tag: "skills/review/v1.1.0"},
		{skill: "skills/other", want: "v9.0.0", tag: "skills/other/v9.0.0"},
		{skill: "skills/plain", want: "v3.0.0", tag: "v3.0.0"},
		{skill: ".", want: "v3.0.0", tag: "v3.0.0"},
	}

	for _, test := range tests {
		t.Run(test.skill, func(t *testing.T) {
			// Arrange
			skillPath, err := source.ParsePath(test.skill)
			require.NoError(t, err)

			// Act
			resolution, err := resolve.Resolve(available, skillPath, resolve.Latest{})

			// Assert
			require.NoError(t, err)
			assert.Equal(t, test.want, resolution.Version.String())
			assert.Equal(t, test.tag, resolution.Tag)
		})
	}
}

func TestResolveRefusesRepositoryVersionWhenPerSkillTagsExist(t *testing.T) {
	// Arrange
	available := refs(lightweight("v3.0.0", "repo300"), lightweight("skills/review/v1.0.0", "review100"))

	// Act
	_, err := resolve.Resolve(available, "skills/review", query(t, "v3.0.0"))

	// Assert
	require.ErrorIs(t, err, resolve.ErrNoMatchingVersion)
	assert.Contains(t, err.Error(), "per-skill tags")
}

func TestResolveRejectsAmbiguousTags(t *testing.T) {
	tests := map[string]vcs.RemoteRefs{
		"same version in both schemes": refs(
			lightweight("v1.0.0", "repo"),
			lightweight("skills/review/v1.0.0", "skill"),
		),
		"build metadata variants": refs(
			lightweight("skills/review/v1.0.0", "first"),
			lightweight("skills/review/v1.0.0+rebuild", "second"),
		),
	}

	for name, available := range tests {
		t.Run(name, func(t *testing.T) {
			// Act
			_, err := resolve.Resolve(available, "skills/review", resolve.Latest{})

			// Assert
			require.ErrorIs(t, err, resolve.ErrAmbiguous)
		})
	}
}

func TestResolveAcceptsAgreeingTags(t *testing.T) {
	// Arrange: both schemes name the same commit, so there is no doubt.
	available := refs(lightweight("v1.0.0", "same"), annotated("skills/review/v1.0.0", "same"))

	// Act
	resolution, err := resolve.Resolve(available, "skills/review", resolve.Latest{})

	// Assert
	require.NoError(t, err)
	assert.Equal(t, "skills/review/v1.0.0", resolution.Tag)
}

func TestResolveRevisions(t *testing.T) {
	available := refs(
		branch("main", "main"),
		branch("release", "release-branch"),
		lightweight("release", "release-tag"),
		branch("stable", "same"),
		lightweight("stable", "same"),
		annotated("skills/review/v2.0.0", "review200"),
	)

	tests := []struct {
		query  string
		branch string
		tag    string
		commit vcs.Hash
	}{
		{query: "main", branch: "main", commit: hash("main")},
		{query: "refs/heads/release", branch: "release", commit: hash("release-branch")},
		{query: "refs/tags/release", tag: "release", commit: hash("release-tag")},
		{query: "stable", tag: "stable", commit: hash("same")},
		{query: "skills/review/v2.0.0", tag: "skills/review/v2.0.0", commit: hash("review200")},
	}

	for _, test := range tests {
		t.Run(test.query, func(t *testing.T) {
			// Act
			resolution, err := resolve.Resolve(available, "skills/review", query(t, test.query))

			// Assert
			require.NoError(t, err)
			assert.Equal(t, test.branch, resolution.Branch)
			assert.Equal(t, test.tag, resolution.Tag)
			assert.Equal(t, test.commit, resolution.Commit)
		})
	}
}

func TestResolveNamedVersionTagReportsVersion(t *testing.T) {
	// Arrange
	available := refs(annotated("skills/review/v2.0.0", "review200"))

	// Act
	resolution, err := resolve.Resolve(available, "skills/review", query(t, "skills/review/v2.0.0"))

	// Assert
	require.NoError(t, err)
	assert.Equal(t, "v2.0.0", resolution.Version.String())
}

func TestResolveRejectsAmbiguousRevision(t *testing.T) {
	// Arrange
	available := refs(branch("release", "a"), lightweight("release", "b"))

	// Act
	_, err := resolve.Resolve(available, source.RootPath, query(t, "release"))

	// Assert
	require.ErrorIs(t, err, resolve.ErrAmbiguous)
}

func TestResolveCommitQueries(t *testing.T) {
	// Arrange
	available := refs(branch("main", "main"))
	full := strings.Repeat("a", 40)

	// Act
	byID, errID := resolve.Resolve(available, source.RootPath, query(t, full))
	byPrefix, errPrefix := resolve.Resolve(available, source.RootPath, query(t, "abcdef1"))
	_, errUnknown := resolve.Resolve(available, source.RootPath, query(t, "nope"))

	// Assert
	require.NoError(t, errID)
	assert.Equal(t, vcs.Hash(full), byID.Commit)
	assert.Empty(t, byID.FetchRef())

	require.NoError(t, errPrefix)
	assert.Equal(t, "abcdef1", byPrefix.CommitPrefix)

	require.ErrorIs(t, errUnknown, resolve.ErrUnknownRevision)
}
