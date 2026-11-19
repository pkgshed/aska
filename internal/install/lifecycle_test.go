package install_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pkgshed/aska/internal/agent"
	"github.com/pkgshed/aska/internal/digest"
	"github.com/pkgshed/aska/internal/install"
	"github.com/pkgshed/aska/internal/resolve"
	"github.com/pkgshed/aska/internal/state"
)

var codexOnly = []agent.Name{agent.Codex}

func codexDir(scope state.Scope, name string) string {
	return filepath.Join(scope.ProjectRoot, ".agents", "skills", name)
}

func TestUpdateInstallsNewerCompatibleVersionAfterApproval(t *testing.T) {
	// Arrange
	h := newHarness(t)
	scope := h.newProject("app")
	h.add(scope, codexOnly, h.request("skills/code-review", "^1.0"))
	next := h.releaseCodeReview("v1.1.0", "Version 1.1")
	h.releaseCodeReview("v2.0.0", "Version 2, incompatible")

	var planned []install.Change
	reviewer := reviewerFunc(func(_ context.Context, changes []install.Change) (bool, error) {
		planned = changes
		return true, nil
	})

	// Act
	result, err := h.engine.Update(context.Background(), scope, nil, reviewer, install.UpdateOptions{})

	// Assert
	require.NoError(t, err)
	assert.True(t, result.Applied)
	require.Len(t, planned, 1)
	assert.Equal(t, "v1.0.0", planned[0].Current.Version.String())
	assert.Equal(t, "v1.1.0", planned[0].Next.Version.String(), "the ^1.0 range excludes v2")
	assert.Contains(t, planned[0].Diffstat, "SKILL.md")

	lock, _ := h.load(scope).CurrentLock("code-review")
	assert.Equal(t, next, lock.Commit)
	assert.Contains(t, readFile(t, filepath.Join(codexDir(scope, "code-review"), "SKILL.md")), "Version 1.1")
}

func TestUpdateLeavesEverythingUntouchedWhenDenied(t *testing.T) {
	// Arrange
	h := newHarness(t)
	scope := h.newProject("app")
	installed := h.add(scope, codexOnly, h.request("skills/code-review", "^1.0"))
	h.releaseCodeReview("v1.1.0", "Version 1.1")
	lockfileBefore := readFile(t, scope.LockfilePath())

	// Act
	result, err := h.engine.Update(context.Background(), scope, nil, reviewerFunc(denyAll), install.UpdateOptions{})

	// Assert
	require.NoError(t, err)
	assert.False(t, result.Applied)
	assert.Len(t, result.Changes, 1)
	assert.Equal(t, lockfileBefore, readFile(t, scope.LockfilePath()))
	sum, err := digest.OfDir(codexDir(scope, "code-review"))
	require.NoError(t, err)
	assert.Equal(t, installed[0].Lock.Digest, sum)
}

func TestUpdateReportsUpToDateSkills(t *testing.T) {
	// Arrange
	h := newHarness(t)
	scope := h.newProject("app")
	h.add(scope, codexOnly, h.request("skills/code-review", "v1.0.0"))
	h.releaseCodeReview("v1.1.0", "Version 1.1")

	// Act
	result, err := h.engine.Update(context.Background(), scope, nil, reviewerFunc(approveAll), install.UpdateOptions{})

	// Assert
	require.NoError(t, err)
	assert.Empty(t, result.Changes, "an exact version stays pinned")
	assert.Equal(t, []string{"code-review"}, result.UpToDate)
}

func TestUpdateFollowsBranches(t *testing.T) {
	// Arrange
	h := newHarness(t)
	scope := h.newProject("app")
	h.add(scope, codexOnly, h.request("skills/writer", "main"))
	h.upstream.WriteFile("skills/writer/SKILL.md", "---\nname: writer\ndescription: Drafts prose.\n---\nNew\n", 0o644)
	head := h.upstream.Commit("writer changes")

	// Act
	result, err := h.engine.Update(context.Background(), scope, []string{"writer"}, reviewerFunc(approveAll),
		install.UpdateOptions{})

	// Assert
	require.NoError(t, err)
	require.Len(t, result.Installed, 1)
	assert.Equal(t, head, result.Installed[0].Lock.Commit)
	assert.Equal(t, "main", result.Installed[0].Lock.Branch)
}

func TestUpdateRefusesMovedTag(t *testing.T) {
	// Arrange
	h := newHarness(t)
	scope := h.newProject("app")
	h.add(scope, codexOnly, h.request("skills/code-review", "^1.0"))
	h.upstream.WriteFile("skills/code-review/SKILL.md",
		"---\nname: code-review\ndescription: Reviews code changes for correctness.\n---\nSneaky\n", 0o644)
	h.upstream.Commit("rewrite history")
	h.upstream.MoveTag("skills/code-review/v1.0.0")
	h.upstream.Git("tag", "--force", "v1.0.0") // Keep both schemes in agreement.

	// Act
	_, err := h.engine.Update(context.Background(), scope, nil, reviewerFunc(approveAll), install.UpdateOptions{})

	// Assert
	require.ErrorIs(t, err, install.ErrTagMoved)
	assert.NotContains(t, readFile(t, filepath.Join(codexDir(scope, "code-review"), "SKILL.md")), "Sneaky")
}

func TestUpdateDetectsConcurrentChanges(t *testing.T) {
	// Arrange: another process removes the skill while approval is pending.
	h := newHarness(t)
	scope := h.newProject("app")
	h.add(scope, codexOnly, h.request("skills/code-review", "^1.0"))
	h.releaseCodeReview("v1.1.0", "Version 1.1")
	reviewer := reviewerFunc(func(ctx context.Context, _ []install.Change) (bool, error) {
		return true, h.engine.Remove(ctx, scope, []string{"code-review"}, install.RemoveOptions{})
	})

	// Act
	_, err := h.engine.Update(context.Background(), scope, nil, reviewer, install.UpdateOptions{})

	// Assert
	require.ErrorIs(t, err, install.ErrStateChanged)
	assert.Empty(t, h.load(scope).Names())
}

func TestSyncRestoresFromCacheWithoutNetwork(t *testing.T) {
	// Arrange: install, lose the installed copies and the upstream.
	h := newHarness(t)
	scope := h.newProject("app")
	h.add(scope, codexOnly, h.request("skills/code-review", "^1.0"))
	require.NoError(t, os.RemoveAll(codexDir(scope, "code-review")))
	require.NoError(t, os.RemoveAll(h.upstream.Dir))

	// Act
	installed, err := h.engine.Sync(context.Background(), scope, install.SyncOptions{})

	// Assert
	require.NoError(t, err)
	require.Len(t, installed, 1)
	assert.True(t, installed[0].Changed)
	assert.FileExists(t, filepath.Join(codexDir(scope, "code-review"), "SKILL.md"))
}

func TestSyncReproducesLockfileInFreshEnvironment(t *testing.T) {
	// Arrange: a teammate clones the project with its .aska directory but
	// has an empty cache, and upstream released a newer version meanwhile.
	h := newHarness(t)
	original := h.newProject("original")
	installed := h.add(original, codexOnly, h.request("skills/code-review", "^1.0"))
	h.releaseCodeReview("v1.1.0", "Version 1.1")

	clone := h.newProject("clone")
	require.NoError(t, os.CopyFS(clone.Dir, os.DirFS(original.Dir)))
	h.engine = h.newEngine(filepath.Join(h.sandbox.Root, "teammate-data"))

	// Act
	_, err := h.engine.Sync(context.Background(), clone, install.SyncOptions{})

	// Assert
	require.NoError(t, err)
	sum, err := digest.OfDir(codexDir(clone, "code-review"))
	require.NoError(t, err)
	assert.Equal(t, installed[0].Lock.Digest, sum, "sync installs the locked version, not the newest one")
}

func TestSyncRejectsTamperedDigest(t *testing.T) {
	// Arrange
	h := newHarness(t)
	scope := h.newProject("app")
	h.add(scope, codexOnly, h.request("skills/code-review", "^1.0"))
	current := h.load(scope)
	lock := current.Locks["code-review"]
	lock.Digest = digest.Of([]digest.File{{Path: "forged"}})
	current.Locks["code-review"] = lock
	require.NoError(t, scope.Save(current))
	h.engine = h.newEngine(filepath.Join(h.sandbox.Root, "cold-cache"))

	// Act
	_, err := h.engine.Sync(context.Background(), scope, install.SyncOptions{})

	// Assert
	require.ErrorIs(t, err, install.ErrDigestMismatch)
}

func TestSyncRequiresCurrentLockfile(t *testing.T) {
	// Arrange: the manifest was edited by hand after locking.
	h := newHarness(t)
	scope := h.newProject("app")
	h.add(scope, codexOnly, h.request("skills/code-review", "^1.0"))
	current := h.load(scope)
	edited := current.Skills["code-review"]
	edited.Requested = resolve.Latest{}
	current.Skills["code-review"] = edited
	require.NoError(t, scope.Save(current))

	// Act
	_, err := h.engine.Sync(context.Background(), scope, install.SyncOptions{})

	// Assert
	require.ErrorIs(t, err, install.ErrLockfileOutdated)
}

func TestSyncRecoversFromInterruptedInstallation(t *testing.T) {
	// Arrange: a previous run published v1.1.0 but died before recording it,
	// leaving staging debris and a half-written lockfile behind.
	h := newHarness(t)
	scope := h.newProject("app")
	installed := h.add(scope, codexOnly, h.request("skills/code-review", "^1.0"))

	h.releaseCodeReview("v1.1.0", "Version 1.1")
	scratch := h.newProject("scratch")
	newer := h.add(scratch, codexOnly, h.request("skills/code-review", "v1.1.0"))

	target := codexDir(scope, "code-review")
	require.NoError(t, os.RemoveAll(target))
	require.NoError(t, os.CopyFS(target, os.DirFS(newer[0].Dirs[0])))
	debris := filepath.Join(scope.ProjectRoot, ".agents", ".aska-staging", "work-1234", "new")
	require.NoError(t, os.MkdirAll(debris, 0o755))
	halfWritten := filepath.Join(scope.Dir, ".skillslock.toml.tmp-99")
	require.NoError(t, os.WriteFile(halfWritten, []byte("lock-version = 1\n[[sk"), 0o644))

	// Act
	_, err := h.engine.Sync(context.Background(), scope, install.SyncOptions{})

	// Assert
	require.NoError(t, err, "content aska produced is recognized without --force")
	sum, err := digest.OfDir(target)
	require.NoError(t, err)
	assert.Equal(t, installed[0].Lock.Digest, sum)
	assert.NoDirExists(t, filepath.Join(scope.ProjectRoot, ".agents", ".aska-staging"))
	assert.NoFileExists(t, halfWritten)
}

func TestSyncRefusesLocallyModifiedDirectories(t *testing.T) {
	// Arrange
	h := newHarness(t)
	scope := h.newProject("app")
	h.add(scope, codexOnly, h.request("skills/code-review", "^1.0"))
	manifestPath := filepath.Join(codexDir(scope, "code-review"), "SKILL.md")
	require.NoError(t, os.WriteFile(manifestPath, []byte("local edits"), 0o644))

	// Act
	_, refused := h.engine.Sync(context.Background(), scope, install.SyncOptions{})
	_, forced := h.engine.Sync(context.Background(), scope, install.SyncOptions{Force: true})

	// Assert
	require.ErrorIs(t, refused, install.ErrUnmanagedTarget)
	require.NoError(t, forced)
	assert.Contains(t, readFile(t, manifestPath), "name: code-review")
}

func TestRemoveDeletesDirectoriesAndRecords(t *testing.T) {
	// Arrange
	h := newHarness(t)
	scope := h.newProject("app")
	h.add(scope, []agent.Name{agent.Claude, agent.Codex}, h.request("skills/code-review", "^1.0"),
		h.request("skills/writer", "main"))

	// Act
	err := h.engine.Remove(context.Background(), scope, []string{"code-review"}, install.RemoveOptions{})

	// Assert
	require.NoError(t, err)
	assert.NoDirExists(t, codexDir(scope, "code-review"))
	assert.NoDirExists(t, filepath.Join(scope.ProjectRoot, ".claude", "skills", "code-review"))
	assert.DirExists(t, codexDir(scope, "writer"))
	current := h.load(scope)
	assert.Equal(t, []string{"writer"}, current.Names())
	assert.NotContains(t, current.Locks, "code-review")
}

func TestRemoveKeepsModifiedDirectoriesUnlessForced(t *testing.T) {
	// Arrange
	h := newHarness(t)
	scope := h.newProject("app")
	h.add(scope, codexOnly, h.request("skills/code-review", "^1.0"))
	notes := filepath.Join(codexDir(scope, "code-review"), "my-notes.md")
	require.NoError(t, os.WriteFile(notes, []byte("precious"), 0o644))

	// Act
	refused := h.engine.Remove(context.Background(), scope, []string{"code-review"}, install.RemoveOptions{})
	forced := h.engine.Remove(context.Background(), scope, []string{"code-review"}, install.RemoveOptions{Force: true})

	// Assert
	require.ErrorIs(t, refused, install.ErrUnmanagedTarget)
	require.NoError(t, forced)
	assert.NoDirExists(t, codexDir(scope, "code-review"))
}

func TestRemoveRejectsUnknownSkills(t *testing.T) {
	// Arrange
	h := newHarness(t)
	scope := h.newProject("app")

	// Act
	err := h.engine.Remove(context.Background(), scope, []string{"absent"}, install.RemoveOptions{})

	// Assert
	require.ErrorIs(t, err, install.ErrNotInstalled)
}

func TestReportComparesDirectoriesWithLockfile(t *testing.T) {
	// Arrange
	h := newHarness(t)
	scope := h.newProject("app")
	h.add(scope, []agent.Name{agent.Claude, agent.Codex}, h.request("skills/code-review", "^1.0"),
		h.request("skills/writer", "main"))
	require.NoError(t, os.WriteFile(filepath.Join(codexDir(scope, "code-review"), "SKILL.md"), []byte("x"), 0o644))
	require.NoError(t, os.RemoveAll(filepath.Join(scope.ProjectRoot, ".claude", "skills", "writer")))

	// Act
	reports, err := h.engine.Report(context.Background(), scope, nil)

	// Assert
	require.NoError(t, err)
	statuses := map[string]install.Status{}
	for _, report := range reports {
		for _, target := range report.Targets {
			relative, relErr := filepath.Rel(scope.ProjectRoot, target.Dir)
			require.NoError(t, relErr)
			statuses[filepath.ToSlash(relative)] = target.Status
		}
	}
	assert.Equal(t, map[string]install.Status{
		".agents/skills/code-review": install.StatusModified,
		".claude/skills/code-review": install.StatusOK,
		".agents/skills/writer":      install.StatusOK,
		".claude/skills/writer":      install.StatusMissing,
	}, statuses)
}

func TestDiffShowsPendingChanges(t *testing.T) {
	// Arrange
	h := newHarness(t)
	scope := h.newProject("app")
	h.add(scope, codexOnly, h.request("skills/code-review", "^1.0"))
	h.releaseCodeReview("v1.1.0", "Version 1.1")

	// Act
	result, err := h.engine.Diff(context.Background(), scope, "code-review", nil)

	// Assert
	require.NoError(t, err)
	assert.Equal(t, "v1.1.0", result.To.Version.String())
	assert.Contains(t, string(result.Patch), "+Version 1.1")
}
