package vcs_test

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pkgshed/aska/internal/testutil/gitfixture"
	"github.com/pkgshed/aska/internal/vcs"
)

type fixture struct {
	sandbox  *gitfixture.Sandbox
	upstream *gitfixture.Repo
	store    *vcs.Store
	url      string
}

// newFixture creates an upstream repository with two releases:
// v1.0.0 as a lightweight tag and v2.0.0 as an annotated tag.
func newFixture(t *testing.T) (*fixture, vcs.Hash, vcs.Hash) {
	t.Helper()

	sandbox := gitfixture.NewSandbox(t)
	upstream := sandbox.NewRepo("skills")
	upstream.WriteFile("skills/review/SKILL.md", "---\nname: review\n---\nv1\n", 0o644)
	upstream.WriteFile("skills/review/run.sh", "#!/bin/sh\n", 0o755)
	upstream.Symlink("SKILL.md", "skills/review/link")
	first := upstream.Commit("v1")
	upstream.Tag("v1.0.0")

	upstream.WriteFile("skills/review/SKILL.md", "---\nname: review\n---\nv2\n", 0o644)
	second := upstream.Commit("v2")
	upstream.AnnotatedTag("v2.0.0")

	store := &vcs.Store{
		Root: filepath.Join(sandbox.Root, "store"),
		Git:  &vcs.Git{Path: "git", Env: sandbox.Env},
	}
	return &fixture{sandbox: sandbox, upstream: upstream, store: store, url: sandbox.URL("skills")}, first, second
}

func (f *fixture) open(t *testing.T) *vcs.Repo {
	t.Helper()
	repo, err := f.store.Open(context.Background(), "example.test/skills.git", f.url, vcs.FormatSHA1)
	require.NoError(t, err)
	return repo
}

func TestListRemotePeelsAnnotatedTags(t *testing.T) {
	// Arrange
	f, first, second := newFixture(t)

	// Act
	refs, err := f.store.ListRemote(context.Background(), f.url)

	// Assert
	require.NoError(t, err)
	assert.Equal(t, "refs/heads/main", refs.Head)
	assert.Equal(t, vcs.FormatSHA1, refs.Format())

	lightweight, found := refs.Lookup("refs/tags/v1.0.0")
	require.True(t, found)
	assert.Equal(t, first, lightweight.Target())
	assert.Empty(t, lightweight.Peeled)

	annotated, found := refs.Lookup("refs/tags/v2.0.0")
	require.True(t, found)
	assert.Equal(t, second, annotated.Target())
	assert.NotEqual(t, second, annotated.Object, "the annotated tag object differs from its commit")
}

func TestOpenInitializesOneBareRepository(t *testing.T) {
	// Arrange
	f, _, _ := newFixture(t)

	// Act
	var wg sync.WaitGroup
	errs := make([]error, 8)
	for index := range errs {
		wg.Go(func() {
			_, errs[index] = f.store.Open(context.Background(), "example.test/skills.git", f.url, vcs.FormatUnknown)
		})
	}
	wg.Wait()

	// Assert
	for _, err := range errs {
		require.NoError(t, err)
	}
	head, err := os.ReadFile(filepath.Join(f.store.Root, "example.test", "skills.git", "HEAD"))
	require.NoError(t, err)
	assert.NotEmpty(t, head)
	_, err = os.Stat(filepath.Join(f.store.Root, "example.test", "skills.git", "hooks"))
	assert.ErrorIs(t, err, os.ErrNotExist, "repositories are created without hook templates")
}

func TestOpenRejectsKeysOutsideTheStore(t *testing.T) {
	// Arrange
	f, _, _ := newFixture(t)

	// Act
	_, err := f.store.Open(context.Background(), "../escape.git", f.url, vcs.FormatSHA1)

	// Assert
	require.ErrorIs(t, err, vcs.ErrInvalidKey)
}

func TestFetchSharesOneRepositoryAcrossVersions(t *testing.T) {
	// Arrange
	f, first, second := newFixture(t)
	repo := f.open(t)
	ctx := context.Background()

	// Act
	errFirst := repo.Fetch(ctx, first, "refs/tags/v1.0.0")
	errSecond := repo.Fetch(ctx, second, "refs/tags/v2.0.0")

	// Assert
	require.NoError(t, errFirst)
	require.NoError(t, errSecond)
	for _, commit := range []vcs.Hash{first, second} {
		present, err := repo.HasCommit(ctx, commit)
		require.NoError(t, err)
		assert.True(t, present, "commit %s", commit.Short())
	}
	shallow := filepath.Join(repo.Dir(), "shallow")
	assert.FileExists(t, shallow, "fetches are shallow")
}

func TestFetchByCommitId(t *testing.T) {
	// Arrange
	f, first, _ := newFixture(t)
	repo := f.open(t)

	// Act
	err := repo.Fetch(context.Background(), first, "")

	// Assert
	require.NoError(t, err)
	present, err := repo.HasCommit(context.Background(), first)
	require.NoError(t, err)
	assert.True(t, present)
}

func TestFetchDetectsMovedTag(t *testing.T) {
	// Arrange
	f, _, second := newFixture(t)
	f.upstream.WriteFile("skills/review/SKILL.md", "---\nname: review\n---\nmoved\n", 0o644)
	moved := f.upstream.Commit("moved")
	f.upstream.MoveTag("v2.0.0")
	repo := f.open(t)

	// Act
	err := repo.Fetch(context.Background(), second, "refs/tags/v2.0.0")

	// Assert
	require.ErrorIs(t, err, vcs.ErrRefMoved)
	var movedErr *vcs.RefMovedError
	require.ErrorAs(t, err, &movedErr)
	assert.Equal(t, second, movedErr.Want)
	assert.Equal(t, moved, movedErr.Got)
}

func TestResolveRevisionAfterFetchingHistory(t *testing.T) {
	// Arrange
	f, first, _ := newFixture(t)
	repo := f.open(t)
	ctx := context.Background()

	// Act
	fetchErr := repo.FetchHistory(ctx)
	resolved, err := repo.ResolveRevision(ctx, first.Short())

	// Assert
	require.NoError(t, fetchErr)
	require.NoError(t, err)
	assert.Equal(t, first, resolved)
}

func TestResolveRevisionRejectsUnknownPrefix(t *testing.T) {
	// Arrange
	f, _, _ := newFixture(t)
	repo := f.open(t)
	require.NoError(t, repo.FetchHistory(context.Background()))

	// Act
	_, err := repo.ResolveRevision(context.Background(), "deadbeefdead")

	// Assert
	require.ErrorIs(t, err, vcs.ErrUnknownRevision)
}

func TestTreeReportsModesAndSizes(t *testing.T) {
	// Arrange
	f, first, _ := newFixture(t)
	repo := f.open(t)
	require.NoError(t, repo.Fetch(context.Background(), first, ""))

	// Act
	entries, err := repo.Tree(context.Background(), first, "skills/review")

	// Assert
	require.NoError(t, err)
	modes := map[string]string{}
	for _, entry := range entries {
		modes[entry.Path] = entry.Mode
	}
	assert.Equal(t, map[string]string{"SKILL.md": "100644", "run.sh": "100755", "link": "120000"}, modes)
}

func TestTreeRejectsMissingAndNonDirectoryPaths(t *testing.T) {
	// Arrange
	f, first, _ := newFixture(t)
	repo := f.open(t)
	require.NoError(t, repo.Fetch(context.Background(), first, ""))

	// Act
	_, missingErr := repo.Tree(context.Background(), first, "skills/absent")
	_, fileErr := repo.Tree(context.Background(), first, "skills/review/SKILL.md")

	// Assert
	require.ErrorIs(t, missingErr, vcs.ErrPathNotFound)
	require.ErrorIs(t, fileErr, vcs.ErrNotDirectory)
}

func TestReadBlobsStreamsContentsInOrder(t *testing.T) {
	// Arrange
	f, first, _ := newFixture(t)
	repo := f.open(t)
	ctx := context.Background()
	require.NoError(t, repo.Fetch(ctx, first, ""))
	entries, err := repo.Tree(ctx, first, "skills/review")
	require.NoError(t, err)

	ids := make([]string, 0, len(entries))
	for _, entry := range entries {
		ids = append(ids, entry.Object.String())
	}

	// Act
	contents := make([]string, len(ids))
	err = repo.ReadBlobs(ctx, ids, func(index int, content io.Reader) error {
		data, readErr := io.ReadAll(content)
		contents[index] = string(data)
		return readErr
	})

	// Assert
	require.NoError(t, err)
	for index, entry := range entries {
		switch entry.Path {
		case "SKILL.md":
			assert.Equal(t, "---\nname: review\n---\nv1\n", contents[index])
		case "run.sh":
			assert.Equal(t, "#!/bin/sh\n", contents[index])
		case "link":
			assert.Equal(t, "SKILL.md", contents[index], "links are read as raw blobs, never followed")
		}
	}
}

func TestDiffComparesSkillDirectory(t *testing.T) {
	// Arrange
	f, first, second := newFixture(t)
	repo := f.open(t)
	ctx := context.Background()
	require.NoError(t, repo.Fetch(ctx, first, ""))
	require.NoError(t, repo.Fetch(ctx, second, ""))

	// Act
	patch, err := repo.Diff(ctx, first, second, "skills/review", vcs.DiffOptions{})
	stat, statErr := repo.Diff(ctx, first, second, "skills/review", vcs.DiffOptions{Stat: true})

	// Assert
	require.NoError(t, err)
	require.NoError(t, statErr)
	assert.Contains(t, string(patch), "-v1\n+v2")
	assert.Contains(t, string(stat), "1 file changed")
}

func TestConcurrentFetchesOfOneRepository(t *testing.T) {
	// Arrange
	f, first, second := newFixture(t)
	repo := f.open(t)
	commits := []vcs.Hash{first, second, first, second, first, second}

	// Act
	var wg sync.WaitGroup
	errs := make([]error, len(commits))
	for index, commit := range commits {
		wg.Go(func() { errs[index] = repo.Fetch(context.Background(), commit, "") })
	}
	wg.Wait()

	// Assert
	for _, err := range errs {
		require.NoError(t, err)
	}
}

func TestListRemoteReportsGitFailures(t *testing.T) {
	// Arrange
	f, _, _ := newFixture(t)

	// Act
	_, err := f.store.ListRemote(context.Background(), f.sandbox.URL("absent"))

	// Assert
	var commandErr *vcs.CommandError
	require.ErrorAs(t, err, &commandErr)
	assert.True(t, strings.HasPrefix(commandErr.Error(), "git ls-remote"), commandErr.Error())
}

func TestFetchToleratesMovedBranch(t *testing.T) {
	// Arrange: the branch advances after it was listed.
	f, _, second := newFixture(t)
	f.upstream.WriteFile("skills/review/SKILL.md", "---\nname: review\n---\nv3\n", 0o644)
	f.upstream.Commit("v3")
	repo := f.open(t)

	// Act
	err := repo.Fetch(context.Background(), second, "refs/heads/main")

	// Assert
	require.NoError(t, err)
	present, err := repo.HasCommit(context.Background(), second)
	require.NoError(t, err)
	assert.True(t, present)
}
