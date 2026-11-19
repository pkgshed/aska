package state_test

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pkgshed/aska/internal/agent"
	"github.com/pkgshed/aska/internal/digest"
	"github.com/pkgshed/aska/internal/resolve"
	"github.com/pkgshed/aska/internal/semver"
	"github.com/pkgshed/aska/internal/source"
	"github.com/pkgshed/aska/internal/state"
	"github.com/pkgshed/aska/internal/vcs"
)

// scopeWithFixture returns a user scope whose directory holds a copy of a
// fixture directory.
func scopeWithFixture(t *testing.T, fixture string) state.Scope {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "state")
	require.NoError(t, os.CopyFS(dir, os.DirFS(filepath.Join("testdata", fixture))))
	return state.NewUserScope(dir)
}

func TestLoadParsesManifestAndLockfile(t *testing.T) {
	// Arrange
	scope := scopeWithFixture(t, "valid")

	// Act
	current, err := scope.Load(source.Policy{})

	// Assert
	require.NoError(t, err)
	assert.Equal(t, []string{"code-review", "writer"}, current.Names())

	review := current.Skills["code-review"]
	assert.Equal(t, "github.com/acme/agent-skills/skills/code-review", review.Package.String())
	assert.Equal(t, "^1.4", review.Requested.String())
	assert.Equal(t, []agent.Name{agent.Claude, agent.Codex}, review.Agents)

	lock, locked := current.CurrentLock("code-review")
	require.True(t, locked)
	assert.Equal(t, "v1.4.2", lock.Version.String())
	assert.Equal(t, "skills/code-review/v1.4.2", lock.Tag)
	assert.Equal(t, vcs.Hash("1111111111111111111111111111111111111111"), lock.Commit)

	writer, locked := current.CurrentLock("writer")
	require.True(t, locked)
	assert.Equal(t, "main", writer.Branch)
	assert.True(t, writer.Package.Path.IsRoot())
}

func TestSaveWritesCanonicalFiles(t *testing.T) {
	// Arrange
	scope := scopeWithFixture(t, "valid")
	current, err := scope.Load(source.Policy{})
	require.NoError(t, err)
	wantManifest, err := os.ReadFile(filepath.Join("testdata", "valid", state.ManifestName))
	require.NoError(t, err)
	wantLockfile, err := os.ReadFile(filepath.Join("testdata", "valid", state.LockfileName))
	require.NoError(t, err)

	// Act
	err = scope.Save(current)

	// Assert
	require.NoError(t, err)
	gotManifest, err := os.ReadFile(scope.ManifestPath())
	require.NoError(t, err)
	gotLockfile, err := os.ReadFile(scope.LockfilePath())
	require.NoError(t, err)
	assert.Equal(t, string(wantManifest), string(gotManifest))
	assert.Equal(t, string(wantLockfile), string(gotLockfile))
}

func TestLoadRejectsInvalidFiles(t *testing.T) {
	tests := map[string]error{
		"unknown-field": state.ErrInvalidManifest,
		"future-lock":   state.ErrInvalidLockfile,
		"bad-commit":    state.ErrInvalidLockfile,
		"bad-tag":       state.ErrInvalidLockfile,
	}

	for fixture, want := range tests {
		t.Run(fixture, func(t *testing.T) {
			// Arrange
			scope := scopeWithFixture(t, fixture)

			// Act
			_, err := scope.Load(source.Policy{})

			// Assert
			require.ErrorIs(t, err, want)
		})
	}
}

func TestLoadOfEmptyScope(t *testing.T) {
	// Arrange
	scope := state.NewUserScope(t.TempDir())

	// Act
	current, err := scope.Load(source.Policy{})

	// Assert
	require.NoError(t, err)
	assert.Empty(t, current.Names())
}

func TestCurrentLockRequiresMatchingRequest(t *testing.T) {
	// Arrange
	scope := scopeWithFixture(t, "valid")
	current, err := scope.Load(source.Policy{})
	require.NoError(t, err)
	edited := current.Skills["code-review"]
	edited.Requested = resolve.Range{Constraint: mustConstraint(t, "^2")}
	current.Skills["code-review"] = edited

	// Act
	_, locked := current.CurrentLock("code-review")

	// Assert
	assert.False(t, locked, "a lock made for ^1.4 does not satisfy a request for ^2")
}

func TestLockRemovesInterruptedWrites(t *testing.T) {
	// Arrange: an interrupted atomic write leaves a temporary file behind
	// while the real lockfile stays intact.
	scope := scopeWithFixture(t, "valid")
	leftover := filepath.Join(scope.Dir, ".skillslock.toml.tmp-123456")
	require.NoError(t, os.WriteFile(leftover, []byte("lock-version = 1\n[[skill]]\nname = 'trunc"), 0o644))

	// Act
	unlock, err := scope.Lock(context.Background())
	require.NoError(t, err)
	defer unlock()
	current, loadErr := scope.Load(source.Policy{})

	// Assert
	require.NoError(t, loadErr)
	assert.NoFileExists(t, leftover)
	assert.Len(t, current.Locks, 2)
}

func TestLockSerializesWriters(t *testing.T) {
	// Arrange: concurrent writers each add one skill under the scope lock.
	stateDir := t.TempDir()
	scope := state.NewProjectScope(t.TempDir(), stateDir)
	names := []string{"alpha", "bravo", "charlie", "delta", "echo", "foxtrot"}

	// Act
	var wg sync.WaitGroup
	errs := make([]error, len(names))
	for index, name := range names {
		wg.Go(func() { errs[index] = addUnderLock(scope, name) })
	}
	wg.Wait()

	// Assert
	for _, err := range errs {
		require.NoError(t, err)
	}
	current, err := scope.Load(source.Policy{})
	require.NoError(t, err)
	assert.ElementsMatch(t, names, current.Names())
}

func addUnderLock(scope state.Scope, name string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	unlock, err := scope.Lock(ctx)
	if err != nil {
		return err
	}
	defer unlock()

	current, err := scope.Load(source.Policy{})
	if err != nil {
		return err
	}
	repo, err := source.ParseRepoURL("https://example.test/skills.git", source.Policy{})
	if err != nil {
		return err
	}
	pkg := source.Package{Repo: repo, Path: source.Path("skills/" + name)}
	sum := digest.Of([]digest.File{{Path: "SKILL.md"}})

	current.Put(
		state.Skill{Name: name, Package: pkg, Requested: resolve.Latest{}, Agents: []agent.Name{agent.Codex}},
		state.Lock{
			Name: name, Package: pkg, Requested: "latest", Version: semver.New(1, 0, 0), Tag: "v1.0.0",
			Commit: vcs.Hash("3333333333333333333333333333333333333333"), Digest: sum,
		},
	)
	return scope.Save(current)
}

func TestFindProjectRoot(t *testing.T) {
	// Arrange
	root := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(root, ".git"), 0o755))
	nested := filepath.Join(root, "a", "b")
	require.NoError(t, os.MkdirAll(nested, 0o755))

	// Act
	found, err := state.FindProjectRoot(nested)

	// Assert
	require.NoError(t, err)
	assert.Equal(t, root, found)
}

func TestFindProjectRootOutsideRepository(t *testing.T) {
	// Arrange
	dir := t.TempDir()

	// Act
	_, err := state.FindProjectRoot(dir)

	// Assert
	if err == nil {
		t.Skip("the temporary directory lies inside a git repository")
	}
	require.ErrorIs(t, err, state.ErrNotInRepository)
}

func mustConstraint(t *testing.T, text string) semver.Constraint {
	t.Helper()
	constraint, err := semver.ParseConstraint(text)
	require.NoError(t, err)
	return constraint
}
