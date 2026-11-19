package install_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pkgshed/aska/internal/agent"
	"github.com/pkgshed/aska/internal/install"
)

// recordingReviewer remembers the changes it saw and answers with approve.
type recordingReviewer struct {
	approve bool
	calls   int
	seen    []install.Change
}

func (r *recordingReviewer) Review(_ context.Context, changes []install.Change) (bool, error) {
	r.calls++
	r.seen = changes
	return r.approve, nil
}

func TestAddPresentsNewSkillsForReview(t *testing.T) {
	// Arrange
	h := newHarness(t)
	scope := h.newProject("app")
	reviewer := &recordingReviewer{approve: true}

	// Act
	result, err := h.engine.Add(context.Background(), scope, []install.Request{h.request("skills/code-review", "^1.0")},
		reviewer, install.AddOptions{Agents: []agent.Name{agent.Codex}})

	// Assert
	require.NoError(t, err)
	assert.True(t, result.Applied)
	require.Equal(t, 1, reviewer.calls)
	require.Len(t, reviewer.seen, 1)

	change := reviewer.seen[0]
	assert.True(t, change.IsNew())
	assert.Equal(t, "code-review", change.Name)
	assert.Equal(t, "Reviews code changes for correctness.", change.Metadata.Description)
	assert.Equal(t, []string{filepath.Join(scope.ProjectRoot, ".agents", "skills", "code-review")}, change.Dirs)

	fixture := filepath.Join("testdata", "skills", "code-review")
	files, err := change.Files()
	require.NoError(t, err)
	assert.Equal(t, []install.File{
		{Path: "SKILL.md", Size: int64(len(readFile(t, filepath.Join(fixture, "SKILL.md"))))},
		{Path: "scripts/check.sh", Size: int64(len(readFile(t, filepath.Join(fixture, "scripts", "check.sh")))), Executable: true},
	}, files)

	content, err := change.ReadFile("scripts/check.sh")
	require.NoError(t, err)
	assert.Equal(t, "#!/bin/sh\necho \"checking\"\n", string(content))

	_, err = change.Patch(context.Background())
	require.ErrorIs(t, err, install.ErrNoBaseline)
}

func TestAddInstallsNothingWhenReviewIsDeclined(t *testing.T) {
	// Arrange
	h := newHarness(t)
	scope := h.newProject("app")

	// Act
	result, err := h.engine.Add(context.Background(), scope, []install.Request{h.request("skills/code-review", "^1.0")},
		reviewerFunc(denyAll), install.AddOptions{Agents: []agent.Name{agent.Codex, agent.Claude}})

	// Assert
	require.NoError(t, err)
	assert.False(t, result.Applied)
	assert.Len(t, result.Changes, 1)
	assert.NoDirExists(t, filepath.Join(scope.ProjectRoot, ".agents"))
	assert.NoDirExists(t, filepath.Join(scope.ProjectRoot, ".claude"))
	assert.NoFileExists(t, scope.ManifestPath())
}

func TestAddReviewsChangedContentAsPatch(t *testing.T) {
	// Arrange
	h := newHarness(t)
	scope := h.newProject("app")
	h.add(scope, codexOnly, h.request("skills/code-review", "v1.0.0"))
	h.releaseCodeReview("v1.1.0", "Version 1.1")
	reviewer := &recordingReviewer{approve: true}

	// Act
	_, err := h.engine.Add(context.Background(), scope, []install.Request{h.request("skills/code-review", "v1.1.0")},
		reviewer, install.AddOptions{})

	// Assert
	require.NoError(t, err)
	require.Len(t, reviewer.seen, 1)
	change := reviewer.seen[0]
	assert.False(t, change.IsNew())
	assert.Equal(t, "v1.0.0", change.Current.Version.String())
	assert.Equal(t, "v1.1.0", change.Next.Version.String())
	assert.Contains(t, change.Diffstat, "SKILL.md")

	patch, err := change.Patch(context.Background())
	require.NoError(t, err)
	assert.Contains(t, string(patch), "+Version 1.1")
}

func TestAddSkipsReviewWhenContentIsUnchanged(t *testing.T) {
	// Arrange: only the agents change, the content was reviewed before.
	h := newHarness(t)
	scope := h.newProject("app")
	h.add(scope, codexOnly, h.request("skills/code-review", "^1.0"))
	reviewer := &recordingReviewer{approve: false}

	// Act
	result, err := h.engine.Add(context.Background(), scope, []install.Request{h.request("skills/code-review", "^1.0")},
		reviewer, install.AddOptions{Agents: []agent.Name{agent.Claude, agent.Codex}})

	// Assert
	require.NoError(t, err)
	assert.Zero(t, reviewer.calls)
	assert.True(t, result.Applied)
	assert.DirExists(t, filepath.Join(scope.ProjectRoot, ".claude", "skills", "code-review"))
}

func TestAddValidatesBeforeReview(t *testing.T) {
	// Arrange: an unmanaged directory would make the installation fail.
	h := newHarness(t)
	scope := h.newProject("app")
	existing := filepath.Join(scope.ProjectRoot, ".agents", "skills", "code-review")
	require.NoError(t, os.MkdirAll(existing, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(existing, "SKILL.md"), []byte("mine"), 0o644))
	reviewer := &recordingReviewer{approve: true}

	// Act
	_, err := h.engine.Add(context.Background(), scope, []install.Request{h.request("skills/code-review", "^1.0")},
		reviewer, install.AddOptions{Agents: []agent.Name{agent.Codex}})

	// Assert
	require.ErrorIs(t, err, install.ErrUnmanagedTarget)
	assert.Zero(t, reviewer.calls, "nothing is offered for review that cannot be installed")
}

func TestAddDetectsChangesDuringReview(t *testing.T) {
	// Arrange: another process installs the same skill while the review is
	// pending, so the reviewed baseline is stale.
	h := newHarness(t)
	scope := h.newProject("app")
	h.releaseCodeReview("v1.1.0", "Version 1.1")
	reviewer := reviewerFunc(func(context.Context, []install.Change) (bool, error) {
		h.add(scope, codexOnly, h.request("skills/code-review", "v1.0.0"))
		return true, nil
	})

	// Act
	_, err := h.engine.Add(context.Background(), scope, []install.Request{h.request("skills/code-review", "v1.1.0")},
		reviewer, install.AddOptions{Agents: codexOnly})

	// Assert
	require.ErrorIs(t, err, install.ErrStateChanged)
	lock, _ := h.load(scope).CurrentLock("code-review")
	assert.Equal(t, "v1.0.0", lock.Version.String(), "the concurrent installation is kept")
}

func TestUpdateRerecordsEditedRequestWithoutReview(t *testing.T) {
	// Arrange: the manifest was edited to a query that selects the same
	// commit, so only the lockfile needs to catch up.
	h := newHarness(t)
	scope := h.newProject("app")
	h.add(scope, codexOnly, h.request("skills/code-review", "^1.0"))
	current := h.load(scope)
	edited := current.Skills["code-review"]
	edited.Requested = h.request("skills/code-review", "v1.0.0").Query
	current.Skills["code-review"] = edited
	require.NoError(t, scope.Save(current))
	reviewer := &recordingReviewer{approve: false}

	// Act
	result, err := h.engine.Update(context.Background(), scope, nil, reviewer, install.UpdateOptions{})

	// Assert
	require.NoError(t, err)
	assert.Zero(t, reviewer.calls)
	assert.True(t, result.Applied)
	_, locked := h.load(scope).CurrentLock("code-review")
	assert.True(t, locked)
}
