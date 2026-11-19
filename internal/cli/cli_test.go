package cli_test

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pkgshed/aska/internal/cli"
	"github.com/pkgshed/aska/internal/paths"
	"github.com/pkgshed/aska/internal/testutil/gitfixture"
)

// world is an isolated machine with one upstream repository and one
// project checkout.
type world struct {
	t        *testing.T
	sandbox  *gitfixture.Sandbox
	upstream *gitfixture.Repo
	project  string
	home     string
	paths    paths.Paths
	// pager and askaPager stand in for PAGER and ASKA_PAGER.
	pager     string
	askaPager string
}

func newWorld(t *testing.T) *world {
	t.Helper()

	sandbox := gitfixture.NewSandbox(t)
	upstream := sandbox.NewRepo("skills")
	upstream.WriteFile("skills/code-review/SKILL.md",
		"---\nname: code-review\ndescription: Reviews code.\n---\nVersion 1.0\n", 0o644)
	upstream.Commit("v1.0.0")
	upstream.AnnotatedTag("skills/code-review/v1.0.0")

	project := filepath.Join(sandbox.Root, "project")
	require.NoError(t, os.MkdirAll(filepath.Join(project, ".git"), 0o755))
	home := filepath.Join(sandbox.Root, "home")
	require.NoError(t, os.MkdirAll(home, 0o755))

	return &world{
		t:        t,
		sandbox:  sandbox,
		upstream: upstream,
		project:  project,
		home:     home,
		pager:    "cat",
		paths: paths.Paths{
			ConfigFile: filepath.Join(home, ".config", "aska", "config.toml"),
			DataDir:    filepath.Join(home, ".local", "share", "aska"),
			StateDir:   filepath.Join(home, ".local", "state", "aska"),
		},
	}
}

type outcome struct {
	stdout string
	stderr string
	err    error
}

func (w *world) run(stdin string, interactive bool, args ...string) outcome {
	w.t.Helper()

	var stdout, stderr bytes.Buffer
	env := cli.Environment{
		Stdin:       strings.NewReader(stdin),
		Stdout:      &stdout,
		Stderr:      &stderr,
		Interactive: interactive,
		WorkDir:     w.project,
		Home:        w.home,
		Paths:       w.paths,
		GitEnv:      w.sandbox.Env,
		Pager:       w.pager,
		AskaPager:   w.askaPager,
		Now:         func() time.Time { return time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC) },
		Exit:        func(code int) { w.t.Fatalf("unexpected exit with code %d", code) },
	}
	err := cli.Execute(context.Background(), args, env)
	return outcome{stdout: stdout.String(), stderr: stderr.String(), err: err}
}

func (w *world) release(version, body string) {
	w.t.Helper()
	w.upstream.WriteFile("skills/code-review/SKILL.md",
		"---\nname: code-review\ndescription: Reviews code.\n---\n"+body+"\n", 0o644)
	w.upstream.Commit(version)
	w.upstream.AnnotatedTag("skills/code-review/" + version)
}

const pkg = gitfixture.Host + "/skills.git/skills/code-review"

func TestAddListInfoAndVerify(t *testing.T) {
	// Arrange
	w := newWorld(t)

	// Act
	added := w.run("", false, "add", "--yes", pkg+"@^1.0")
	listed := w.run("", false, "list")
	info := w.run("", false, "info", "code-review")
	verified := w.run("", false, "verify")

	// Assert
	require.NoError(t, added.err, added.stderr)
	assert.Contains(t, added.stdout, "code-review v1.0.0 (tag skills/code-review/v1.0.0)")
	assert.Contains(t, added.stdout, filepath.Join(".agents", "skills", "code-review"))
	assert.Contains(t, added.stdout, filepath.Join(".claude", "skills", "code-review"), "codex and claude are the defaults")

	require.NoError(t, listed.err)
	assert.Regexp(t, `NAME\s+REQUESTED\s+RESOLVED\s+REVISION\s+AGENTS\s+STATUS`, listed.stdout)
	assert.Regexp(t, `code-review\s+\^1\.0\s+v1\.0\.0\s+[0-9a-f]{12}\s+claude,codex\s+ok`, listed.stdout)

	require.NoError(t, info.err)
	assert.Contains(t, info.stdout, "Description: Reviews code.")
	assert.Contains(t, info.stdout, "Source:      git+https://example.test/skills.git")

	require.NoError(t, verified.err)
	assert.Contains(t, verified.stdout, "All 1 skill(s) match skillslock.toml")
}

func TestVerifyFailsForModifiedFiles(t *testing.T) {
	// Arrange
	w := newWorld(t)
	require.NoError(t, w.run("", false, "add", "--yes", "--agent", "codex", pkg).err)
	manifest := filepath.Join(w.project, ".agents", "skills", "code-review", "SKILL.md")
	require.NoError(t, os.WriteFile(manifest, []byte("tampered"), 0o644))

	// Act
	result := w.run("", false, "verify")

	// Assert
	require.Error(t, result.err)
	assert.Contains(t, result.err.Error(), "verification failed")
	assert.Contains(t, result.stdout, "modified")
}

func TestUpdateNeedsApprovalWhenNotInteractive(t *testing.T) {
	// Arrange
	w := newWorld(t)
	require.NoError(t, w.run("", false, "add", "--yes", "-a", "codex", pkg+"@^1.0").err)
	w.release("v1.1.0", "Version 1.1")

	// Act
	refused := w.run("", false, "update")
	approved := w.run("", false, "update", "--yes")

	// Assert
	require.Error(t, refused.err)
	assert.Contains(t, refused.err.Error(), "approval required")
	assert.Contains(t, refused.stdout, "v1.0.0")
	assert.Contains(t, refused.stdout, "v1.1.0")

	require.NoError(t, approved.err, approved.stderr)
	assert.Contains(t, approved.stdout, "Updated code-review to v1.1.0")
}

func TestUpdateAsksInteractively(t *testing.T) {
	// Arrange
	w := newWorld(t)
	require.NoError(t, w.run("", false, "add", "--yes", "-a", "codex", pkg+"@^1.0").err)
	w.release("v1.1.0", "Version 1.1")

	// Act: inspect nothing, then answer the confirmation.
	declined := w.run("n\nn\n", true, "update")
	accepted := w.run("n\ny\n", true, "update")

	// Assert
	require.NoError(t, declined.err)
	assert.Contains(t, declined.stdout, "No changes were made.")
	require.NoError(t, accepted.err)
	assert.Contains(t, accepted.stdout, "Updated code-review to v1.1.0")
}

func TestGlobalScopeInstallsIntoHome(t *testing.T) {
	// Arrange
	w := newWorld(t)

	// Act
	result := w.run("", false, "add", "--yes", "--global", "--agent", "pi", pkg)

	// Assert
	require.NoError(t, result.err, result.stderr)
	assert.FileExists(t, filepath.Join(w.home, ".agents", "skills", "code-review", "SKILL.md"))
	assert.FileExists(t, filepath.Join(w.paths.StateDir, "skills.toml"))
	assert.FileExists(t, filepath.Join(w.paths.StateDir, "skillslock.toml"))
	assert.Contains(t, result.stdout, filepath.Join("~", ".agents", "skills", "code-review"))
}

func TestProjectScopeRequiresRepository(t *testing.T) {
	// Arrange
	w := newWorld(t)
	w.project = t.TempDir()

	// Act
	result := w.run("", false, "list")

	// Assert
	if result.err == nil {
		t.Skip("the temporary directory lies inside a git repository")
	}
	assert.Contains(t, result.err.Error(), "use --global")
}

func TestSyncAndRemove(t *testing.T) {
	// Arrange
	w := newWorld(t)
	require.NoError(t, w.run("", false, "add", "--yes", "-a", "codex", pkg).err)
	installed := filepath.Join(w.project, ".agents", "skills", "code-review")
	require.NoError(t, os.RemoveAll(installed))

	// Act
	synced := w.run("", false, "sync")
	removed := w.run("", false, "remove", "code-review")

	// Assert
	require.NoError(t, synced.err)
	assert.Contains(t, synced.stdout, "1 of 1 skill(s) needed changes")
	require.NoError(t, removed.err)
	assert.NoDirExists(t, installed)
}

func TestInvalidConfigurationIsReported(t *testing.T) {
	// Arrange
	w := newWorld(t)
	require.NoError(t, os.MkdirAll(filepath.Dir(w.paths.ConfigFile), 0o755))
	require.NoError(t, os.WriteFile(w.paths.ConfigFile, []byte("default_agents = [\"cursor\"]\n"), 0o644))

	// Act
	result := w.run("", false, "list")

	// Assert
	require.Error(t, result.err)
	assert.Contains(t, result.err.Error(), "unknown agent")
}

func TestDiffPrintsPatch(t *testing.T) {
	// Arrange
	w := newWorld(t)
	require.NoError(t, w.run("", false, "add", "--yes", "-a", "codex", pkg+"@^1.0").err)
	w.release("v1.1.0", "Version 1.1\x1b]0;pwned\x07")

	// Act
	result := w.run("", false, "diff", "code-review")

	// Assert
	require.NoError(t, result.err)
	assert.Contains(t, result.stdout, "+Version 1.1")
	assert.NotContains(t, result.stdout, "\x1b]0;", "escape sequences from upstream are neutralized")
	assert.Contains(t, result.stdout, `\x1b]0;pwned\x07`)
}
