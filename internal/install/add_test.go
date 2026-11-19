package install_test

import (
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pkgshed/aska/internal/agent"
	"github.com/pkgshed/aska/internal/digest"
	"github.com/pkgshed/aska/internal/extract"
	"github.com/pkgshed/aska/internal/install"
	"github.com/pkgshed/aska/internal/skill"
	"github.com/pkgshed/aska/internal/state"
	"github.com/pkgshed/aska/internal/testutil/gitfixture"
	"github.com/pkgshed/aska/internal/vcs"
)

func TestAddInstallsOnceIntoEachSkillsDirectory(t *testing.T) {
	// Arrange
	h := newHarness(t)
	scope := h.newProject("app")
	agents := []agent.Name{agent.Codex, agent.OpenCode, agent.Pi, agent.Claude}

	// Act
	installed := h.add(scope, agents, h.request("skills/code-review", "^1.0"))

	// Assert
	require.Len(t, installed, 1)
	agentsDir := filepath.Join(scope.ProjectRoot, ".agents", "skills", "code-review")
	claudeDir := filepath.Join(scope.ProjectRoot, ".claude", "skills", "code-review")
	assert.ElementsMatch(t, []string{agentsDir, claudeDir}, installed[0].Dirs)

	for _, dir := range installed[0].Dirs {
		sum, err := digest.OfDir(dir)
		require.NoError(t, err)
		assert.Equal(t, installed[0].Lock.Digest, sum, "installed copy matches the locked digest")
	}
	info, err := os.Stat(filepath.Join(agentsDir, "scripts", "check.sh"))
	require.NoError(t, err)
	assert.NotZero(t, info.Mode().Perm()&0o100, "executable bits survive installation")
	assert.NoDirExists(t, filepath.Join(scope.ProjectRoot, ".agents", ".aska-staging"), "staging is cleaned up")
}

func TestAddRecordsManifestAndLockfile(t *testing.T) {
	// Arrange
	h := newHarness(t)
	scope := h.newProject("app")

	// Act
	installed := h.add(scope, []agent.Name{agent.Codex}, h.request("skills/code-review", ""))

	// Assert
	current := h.load(scope)
	requested := current.Skills["code-review"]
	assert.Equal(t, "^1.0.0", requested.Requested.String(), "an unversioned add records a caret range")
	assert.Equal(t, []agent.Name{agent.Codex}, requested.Agents)

	lock, locked := current.CurrentLock("code-review")
	require.True(t, locked)
	assert.Equal(t, installed[0].Lock, lock)
	assert.Equal(t, "v1.0.0", lock.Version.String())
	assert.Equal(t, "skills/code-review/v1.0.0", lock.Tag, "per-skill tags take precedence")
	assert.Equal(t, vcs.Hash(h.upstream.Git("rev-parse", "skills/code-review/v1.0.0^{commit}")), lock.Commit)
}

func TestAddSharesOneRepositoryAcrossSkillsAndVersions(t *testing.T) {
	// Arrange
	h := newHarness(t)
	first := h.newProject("first")
	second := h.newProject("second")
	h.releaseCodeReview("v1.1.0", "Version 1.1")

	// Act
	h.add(first, []agent.Name{agent.Codex}, h.request("skills/code-review", "v1.0.0"), h.request("skills/writer", "main"))
	h.add(second, []agent.Name{agent.Codex}, h.request("skills/code-review", "v1.1.0"))

	// Assert
	repos, err := os.ReadDir(filepath.Join(h.sandbox.Root, "data", "repos", gitfixture.Host))
	require.NoError(t, err)
	var names []string
	for _, entry := range repos {
		if entry.IsDir() {
			names = append(names, entry.Name())
		}
	}
	assert.Equal(t, []string{"skills.git"}, names)

	snapshots, err := os.ReadDir(filepath.Join(h.sandbox.Root, "data", "packages",
		h.sandbox.Package("skills", "skills/code-review").CacheKey()))
	require.NoError(t, err)
	assert.Len(t, snapshots, 2, "both versions coexist in the snapshot cache")
}

func TestAddRefusesUnmanagedDirectories(t *testing.T) {
	// Arrange: the user already keeps their own skill of the same name.
	h := newHarness(t)
	scope := h.newProject("app")
	existing := filepath.Join(scope.ProjectRoot, ".agents", "skills", "code-review")
	require.NoError(t, os.MkdirAll(existing, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(existing, "SKILL.md"), []byte("mine"), 0o644))
	request := h.request("skills/code-review", "^1.0")
	agents := []agent.Name{agent.Claude, agent.Codex}

	// Act
	_, err := h.tryAdd(scope, install.AddOptions{Agents: agents}, request)

	// Assert
	require.ErrorIs(t, err, install.ErrUnmanagedTarget)
	assert.Equal(t, "mine", readFile(t, filepath.Join(existing, "SKILL.md")))
	assert.NoDirExists(t, filepath.Join(scope.ProjectRoot, ".claude", "skills", "code-review"),
		"no directory changes before every target passed the check")
	assert.NoFileExists(t, scope.ManifestPath())
}

func TestAddWithForceReplacesUnmanagedDirectories(t *testing.T) {
	// Arrange
	h := newHarness(t)
	scope := h.newProject("app")
	existing := filepath.Join(scope.ProjectRoot, ".agents", "skills", "code-review")
	require.NoError(t, os.MkdirAll(existing, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(existing, "SKILL.md"), []byte("mine"), 0o644))
	options := install.AddOptions{Agents: []agent.Name{agent.Codex}, Force: true}

	// Act
	_, err := h.tryAdd(scope, options, h.request("skills/code-review", "^1.0"))

	// Assert
	require.NoError(t, err)
	assert.Contains(t, readFile(t, filepath.Join(existing, "SKILL.md")), "name: code-review")
}

func TestAddRejectsNameConflicts(t *testing.T) {
	// Arrange: a second repository ships a different skill with the same name.
	h := newHarness(t)
	other := h.sandbox.NewRepo("other")
	other.CopyDir(filepath.Join("testdata", "skills", "writer"), "writer")
	other.Commit("writer")
	scope := h.newProject("app")
	h.add(scope, []agent.Name{agent.Codex}, h.request("skills/writer", "main"))
	conflicting := h.request("skills/writer", "main")
	conflicting.Package = h.sandbox.Package("other", "writer")

	// Act
	_, err := h.tryAdd(scope, install.AddOptions{Agents: []agent.Name{agent.Codex}}, conflicting)

	// Assert
	require.ErrorIs(t, err, install.ErrNameConflict)
}

func TestAddKeepsAgentsOfReinstalledSkill(t *testing.T) {
	// Arrange
	h := newHarness(t)
	scope := h.newProject("app")
	h.add(scope, []agent.Name{agent.Claude}, h.request("skills/code-review", "^1.0"))

	// Act
	_, err := h.tryAdd(scope, install.AddOptions{DefaultAgents: []agent.Name{agent.Codex}},
		h.request("skills/code-review", "v1.0.0"))

	// Assert
	require.NoError(t, err)
	assert.Equal(t, []agent.Name{agent.Claude}, h.load(scope).Skills["code-review"].Agents)
}

func TestAddDropsDirectoriesOfDeselectedAgents(t *testing.T) {
	// Arrange
	h := newHarness(t)
	scope := h.newProject("app")
	h.add(scope, []agent.Name{agent.Claude, agent.Codex}, h.request("skills/code-review", "^1.0"))

	// Act
	h.add(scope, []agent.Name{agent.Codex}, h.request("skills/code-review", "^1.0"))

	// Assert
	assert.NoDirExists(t, filepath.Join(scope.ProjectRoot, ".claude", "skills", "code-review"))
	assert.DirExists(t, filepath.Join(scope.ProjectRoot, ".agents", "skills", "code-review"))
}

func TestAddAppliesAgentSpecificNamingRules(t *testing.T) {
	// Arrange: a skill without a frontmatter name.
	h := newHarness(t)
	h.upstream.CopyDir(filepath.Join("testdata", "skills", "nameless"), "skills/nameless")
	h.upstream.Commit("nameless skill")
	scope := h.newProject("app")
	request := h.request("skills/nameless", "main")

	// Act
	claudeResult, claudeErr := h.tryAdd(scope, install.AddOptions{Agents: []agent.Name{agent.Claude}}, request)
	_, codexErr := h.tryAdd(h.newProject("other"), install.AddOptions{Agents: []agent.Name{agent.Codex}}, request)

	// Assert
	require.NoError(t, claudeErr, "Claude Code falls back to the directory name")
	assert.Equal(t, "nameless", claudeResult.Installed[0].Name)
	require.ErrorIs(t, codexErr, agent.ErrIncompatibleSkill, "Codex requires a name")
}

func TestAddRejectsInvalidSkills(t *testing.T) {
	// Arrange
	h := newHarness(t)
	h.upstream.CopyDir(filepath.Join("testdata", "skills", "broken"), "skills/broken")
	h.upstream.Commit("broken skill")
	scope := h.newProject("app")

	// Act
	_, err := h.tryAdd(scope, install.AddOptions{Agents: []agent.Name{agent.Claude}}, h.request("skills/broken", "main"))

	// Assert
	require.ErrorIs(t, err, skill.ErrInvalidManifest)
	assert.NoDirExists(t, filepath.Join(h.sandbox.Root, "data", "packages",
		h.sandbox.Package("skills", "skills/broken").CacheKey()), "invalid skills never reach the cache")
}

func TestAddResolvesBranchesAndCommits(t *testing.T) {
	// Arrange
	h := newHarness(t)
	initial := vcs.Hash(h.upstream.Git("rev-parse", "HEAD"))
	h.releaseCodeReview("v1.1.0", "Version 1.1")
	scope := h.newProject("app")

	tests := map[string]vcs.Hash{
		"main":                      vcs.Hash(h.upstream.Git("rev-parse", "HEAD")),
		initial.String():            initial,
		initial.String()[:10]:       initial,
		"refs/tags/v1.0.0":          initial,
		"skills/code-review/v1.1.0": vcs.Hash(h.upstream.Git("rev-parse", "HEAD")),
	}

	for query, want := range tests {
		t.Run(query, func(t *testing.T) {
			// Act
			installed := h.add(scope, []agent.Name{agent.Codex}, h.request("skills/code-review", query))

			// Assert
			assert.Equal(t, want, installed[0].Lock.Commit)
			assert.Equal(t, query, h.load(scope).Skills["code-review"].Requested.String())
		})
	}
}

func TestAddRejectsMaliciousTrees(t *testing.T) {
	h := newHarness(t)
	manifest := h.upstream.WriteBlob("---\nname: evil\ndescription: Evil.\n---\n")
	payload := h.upstream.WriteBlob("payload")
	commit := vcs.Hash(h.upstream.Git("rev-parse", "HEAD"))

	tests := map[string]struct {
		entries []gitfixture.TreeEntry
		want    error
	}{
		"symbolic link": {
			entries: []gitfixture.TreeEntry{{Mode: "120000", Name: "link", Object: h.upstream.WriteBlob("/etc/passwd")}},
			want:    extract.ErrUnsupportedEntry,
		},
		"submodule": {
			entries: []gitfixture.TreeEntry{{Mode: "160000", Name: "vendor", Object: commit}},
			want:    extract.ErrUnsupportedEntry,
		},
		"parent directory entry": {
			entries: []gitfixture.TreeEntry{{Mode: "100644", Name: "..", Object: payload}},
			want:    extract.ErrUnsafePath,
		},
		"nested git directory": {
			entries: []gitfixture.TreeEntry{{Mode: "40000", Name: ".git", Object: h.upstream.WriteTree(
				[]gitfixture.TreeEntry{{Mode: "100644", Name: "config", Object: payload}})}},
			want: extract.ErrUnsafePath,
		},
		"case collision": {
			entries: []gitfixture.TreeEntry{{Mode: "100644", Name: "skill.md", Object: payload}},
			want:    extract.ErrPathConflict,
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			// Arrange: skills/evil holds SKILL.md plus the hostile entry.
			evil := h.upstream.WriteTree(append([]gitfixture.TreeEntry{
				{Mode: "100644", Name: "SKILL.md", Object: manifest},
			}, test.entries...))
			skills := h.upstream.WriteTree([]gitfixture.TreeEntry{{Mode: "40000", Name: "evil", Object: evil}})
			root := h.upstream.WriteTree([]gitfixture.TreeEntry{{Mode: "40000", Name: "skills", Object: skills}})
			branch := "evil-" + filepath.Base(t.Name())
			h.upstream.CommitTree(root, branch)
			scope := h.newProject(branch)

			// Act
			_, err := h.tryAdd(scope, install.AddOptions{Agents: []agent.Name{agent.Claude}}, h.request("skills/evil", branch))

			// Assert
			require.ErrorIs(t, err, test.want)
			assert.NoDirExists(t, filepath.Join(scope.ProjectRoot, ".claude", "skills", "evil"))
		})
	}
}

func TestConcurrentInstallationsIntoOneScope(t *testing.T) {
	// Arrange
	h := newHarness(t)
	names := []string{"alpha", "bravo", "charlie", "delta", "echo", "foxtrot"}
	for _, name := range names {
		h.upstream.WriteFile("skills/"+name+"/SKILL.md", "---\nname: "+name+"\ndescription: Skill "+name+".\n---\n", 0o644)
	}
	h.upstream.Commit("many skills")
	scope := h.newProject("app")

	// Act
	var wg sync.WaitGroup
	errs := make([]error, len(names))
	for index, name := range names {
		wg.Go(func() {
			_, errs[index] = h.tryAdd(scope, install.AddOptions{Agents: []agent.Name{agent.Codex}},
				h.request("skills/"+name, "main"))
		})
	}
	wg.Wait()

	// Assert
	for _, err := range errs {
		require.NoError(t, err)
	}
	current := h.load(scope)
	assert.ElementsMatch(t, names, current.Names())
	for _, name := range names {
		_, locked := current.CurrentLock(name)
		assert.True(t, locked, name)
		assert.DirExists(t, filepath.Join(scope.ProjectRoot, ".agents", "skills", name))
	}
}

func TestConcurrentInstallationsAcrossScopes(t *testing.T) {
	// Arrange
	h := newHarness(t)
	scopes := make([]state.Scope, 6)
	for index := range scopes {
		scopes[index] = h.newProject("project-" + string(rune('a'+index)))
	}

	// Act: every scope fetches the same repository at the same time.
	var wg sync.WaitGroup
	errs := make([]error, len(scopes))
	for index, scope := range scopes {
		wg.Go(func() {
			_, errs[index] = h.tryAdd(scope, install.AddOptions{Agents: []agent.Name{agent.Codex}},
				h.request("skills/code-review", "^1.0"))
		})
	}
	wg.Wait()

	// Assert
	for _, err := range errs {
		require.NoError(t, err)
	}
}
