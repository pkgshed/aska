package install_test

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/pkgshed/aska/internal/agent"
	"github.com/pkgshed/aska/internal/cache"
	"github.com/pkgshed/aska/internal/extract"
	"github.com/pkgshed/aska/internal/install"
	"github.com/pkgshed/aska/internal/resolve"
	"github.com/pkgshed/aska/internal/state"
	"github.com/pkgshed/aska/internal/testutil/gitfixture"
	"github.com/pkgshed/aska/internal/vcs"
)

// harness wires an engine to sandbox repositories and directories.
type harness struct {
	t        *testing.T
	sandbox  *gitfixture.Sandbox
	upstream *gitfixture.Repo
	engine   *install.Engine
	home     string
	stateDir string
}

// newHarness creates an upstream repository holding the code-review and
// writer skills. code-review is released as skills/code-review/v1.0.0;
// the repository as a whole as v1.0.0.
func newHarness(t *testing.T) *harness {
	t.Helper()

	sandbox := gitfixture.NewSandbox(t)
	upstream := sandbox.NewRepo("skills")
	upstream.CopyDir(filepath.Join("testdata", "skills", "code-review"), "skills/code-review")
	upstream.CopyDir(filepath.Join("testdata", "skills", "writer"), "skills/writer")
	upstream.Commit("initial skills")
	upstream.AnnotatedTag("skills/code-review/v1.0.0")
	upstream.Tag("v1.0.0")

	h := &harness{
		t:        t,
		sandbox:  sandbox,
		upstream: upstream,
		home:     filepath.Join(sandbox.Root, "home"),
		stateDir: filepath.Join(sandbox.Root, "state"),
	}
	h.engine = h.newEngine(filepath.Join(sandbox.Root, "data"))
	return h
}

// newEngine returns an engine with its own repository store and cache.
func (h *harness) newEngine(dataDir string) *install.Engine {
	return &install.Engine{
		Repos: &vcs.Store{
			Root: filepath.Join(dataDir, "repos"),
			Git:  &vcs.Git{Path: "git", Env: h.sandbox.Env},
		},
		Cache:  &cache.Store{Root: filepath.Join(dataDir, "packages")},
		Agents: agent.NewRegistry(agent.DefaultDirs(agent.Environment{Home: h.home})),
		Limits: extract.DefaultLimits(),
		Logger: slog.New(slog.DiscardHandler),
	}
}

// newProject creates an empty git repository and returns its scope.
func (h *harness) newProject(name string) state.Scope {
	h.t.Helper()
	root := filepath.Join(h.sandbox.Root, "projects", name)
	require.NoError(h.t, os.MkdirAll(filepath.Join(root, ".git"), 0o755))
	return state.NewProjectScope(root, h.stateDir)
}

func (h *harness) request(skillPath, query string) install.Request {
	h.t.Helper()
	parsed, err := resolve.ParseQuery(query)
	require.NoError(h.t, err)
	return install.Request{Package: h.sandbox.Package("skills", skillPath), Query: parsed, Unversioned: query == ""}
}

func (h *harness) add(scope state.Scope, agents []agent.Name, requests ...install.Request) []install.Installed {
	h.t.Helper()
	result, err := h.tryAdd(scope, install.AddOptions{Agents: agents}, requests...)
	require.NoError(h.t, err)
	require.True(h.t, result.Applied)
	return result.Installed
}

// tryAdd installs requests, approving every review.
func (h *harness) tryAdd(scope state.Scope, options install.AddOptions, requests ...install.Request) (install.AddResult, error) {
	return h.engine.Add(context.Background(), scope, requests, reviewerFunc(approveAll), options)
}

// releaseCodeReview commits a new version of the code-review skill and
// tags it.
func (h *harness) releaseCodeReview(version, body string) vcs.Hash {
	h.t.Helper()
	h.upstream.WriteFile("skills/code-review/SKILL.md",
		"---\nname: code-review\ndescription: Reviews code changes for correctness.\n---\n\n"+body+"\n", 0o644)
	commit := h.upstream.Commit("release " + version)
	h.upstream.AnnotatedTag("skills/code-review/" + version)
	return commit
}

func (h *harness) load(scope state.Scope) *state.State {
	h.t.Helper()
	current, err := scope.Load(h.engine.Policy)
	require.NoError(h.t, err)
	return current
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	content, err := os.ReadFile(path)
	require.NoError(t, err)
	return string(content)
}

// reviewerFunc adapts a function to [install.Reviewer].
type reviewerFunc func(context.Context, []install.Change) (bool, error)

func (f reviewerFunc) Review(ctx context.Context, changes []install.Change) (bool, error) {
	return f(ctx, changes)
}

func approveAll(context.Context, []install.Change) (bool, error) { return true, nil }

func denyAll(context.Context, []install.Change) (bool, error) { return false, nil }
