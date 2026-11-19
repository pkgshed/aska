// Package gitfixture builds throwaway git repositories for tests.
//
// A [Sandbox] isolates git from the user's configuration and maps the fake
// host https://example.test/ to local repositories with url.insteadOf, so
// tests exercise the same remote URLs and code paths as real use without
// any network access.
package gitfixture

import (
	"bytes"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/pkgshed/aska/internal/source"
	"github.com/pkgshed/aska/internal/vcs"
)

// Host is the fake host that the sandbox redirects to local repositories.
const Host = "example.test"

// Sandbox owns a set of fixture repositories and an isolated git setup.
type Sandbox struct {
	t testing.TB
	// Root is the sandbox directory.
	Root string
	// Env isolates git; pass it to [vcs.Git.Env].
	Env []string
}

// NewSandbox creates a sandbox below a temporary directory.
func NewSandbox(t testing.TB) *Sandbox {
	t.Helper()

	root := t.TempDir()
	remotes := filepath.Join(root, "remotes")
	if err := os.MkdirAll(remotes, 0o755); err != nil {
		t.Fatalf("create remotes directory: %v", err)
	}

	gitconfig := filepath.Join(root, "gitconfig")
	config := fmt.Sprintf("[init]\n\tdefaultBranch = main\n"+
		"[commit]\n\tgpgSign = false\n[tag]\n\tgpgSign = false\n"+
		"[protocol \"file\"]\n\tallow = always\n"+
		"[url \"file://%s/\"]\n\tinsteadOf = https://%s/\n", filepath.ToSlash(remotes), Host)
	if err := os.WriteFile(gitconfig, []byte(config), 0o644); err != nil {
		t.Fatalf("write gitconfig: %v", err)
	}

	return &Sandbox{
		t:    t,
		Root: root,
		Env: []string{
			"GIT_CONFIG_NOSYSTEM=1",
			"GIT_CONFIG_GLOBAL=" + gitconfig,
			"GIT_CONFIG_COUNT=0",
			"GIT_AUTHOR_NAME=Fixture",
			"GIT_AUTHOR_EMAIL=fixture@example.invalid",
			"GIT_COMMITTER_NAME=Fixture",
			"GIT_COMMITTER_EMAIL=fixture@example.invalid",
			"GIT_AUTHOR_DATE=2026-01-01T00:00:00Z",
			"GIT_COMMITTER_DATE=2026-01-01T00:00:00Z",
		},
	}
}

// NewRepo creates a repository that is reachable as
// https://example.test/<name>.git.
func (s *Sandbox) NewRepo(name string) *Repo {
	s.t.Helper()

	dir := filepath.Join(s.Root, "remotes", name+".git")
	repo := &Repo{t: s.t, Dir: dir, env: s.Env}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		s.t.Fatalf("create repository: %v", err)
	}
	repo.Git("init", "--quiet")
	return repo
}

// URL returns the remote URL of a repository created with [Sandbox.NewRepo].
func (s *Sandbox) URL(name string) string {
	return fmt.Sprintf("https://%s/%s.git", Host, name)
}

// Package returns the package for a skill path in a sandbox repository.
func (s *Sandbox) Package(name, skillPath string) source.Package {
	s.t.Helper()

	repo, err := source.ParseRepoURL(s.URL(name), source.Policy{})
	if err != nil {
		s.t.Fatalf("parse repository URL: %v", err)
	}
	parsedPath, err := source.ParsePath(skillPath)
	if err != nil {
		s.t.Fatalf("parse skill path: %v", err)
	}
	return source.Package{Repo: repo, Path: parsedPath}
}

// Repo is a non-bare fixture repository.
type Repo struct {
	t   testing.TB
	Dir string
	env []string
}

// Git runs git in the repository and returns its trimmed output.
func (r *Repo) Git(args ...string) string {
	r.t.Helper()
	return r.gitWithInput(nil, args...)
}

func (r *Repo) gitWithInput(input []byte, args ...string) string {
	r.t.Helper()

	cmd := exec.Command("git", args...)
	cmd.Dir = r.Dir
	cmd.Env = append(os.Environ(), r.env...)
	cmd.Stdin = bytes.NewReader(input)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		r.t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, stderr.String())
	}
	return strings.TrimSpace(stdout.String())
}

// WriteFile writes a file in the work tree, creating parent directories.
func (r *Repo) WriteFile(name, content string, mode fs.FileMode) {
	r.t.Helper()

	path := filepath.Join(r.Dir, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		r.t.Fatalf("create directory for %s: %v", name, err)
	}
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		r.t.Fatalf("write %s: %v", name, err)
	}
	if err := os.Chmod(path, mode); err != nil {
		r.t.Fatalf("chmod %s: %v", name, err)
	}
}

// CopyDir copies a fixture directory into the work tree at name.
func (r *Repo) CopyDir(fixture, name string) {
	r.t.Helper()

	destination := filepath.Join(r.Dir, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
		r.t.Fatalf("create directory for %s: %v", name, err)
	}
	if err := os.CopyFS(destination, os.DirFS(fixture)); err != nil {
		r.t.Fatalf("copy %s to %s: %v", fixture, name, err)
	}
}

// Symlink creates a symbolic link in the work tree.
func (r *Repo) Symlink(target, name string) {
	r.t.Helper()

	path := filepath.Join(r.Dir, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		r.t.Fatalf("create directory for %s: %v", name, err)
	}
	if err := os.Symlink(target, path); err != nil {
		r.t.Fatalf("symlink %s: %v", name, err)
	}
}

// Commit stages everything and commits it.
func (r *Repo) Commit(message string) vcs.Hash {
	r.t.Helper()

	r.Git("add", "--all")
	r.Git("commit", "--quiet", "--allow-empty", "--message", message)
	return vcs.Hash(r.Git("rev-parse", "HEAD"))
}

// Tag creates a lightweight tag at HEAD.
func (r *Repo) Tag(name string) {
	r.t.Helper()
	r.Git("tag", name)
}

// AnnotatedTag creates an annotated tag at HEAD.
func (r *Repo) AnnotatedTag(name string) {
	r.t.Helper()
	r.Git("tag", "--annotate", "--message", "release "+name, name)
}

// MoveTag points an existing annotated tag at HEAD.
func (r *Repo) MoveTag(name string) {
	r.t.Helper()
	r.Git("tag", "--force", "--annotate", "--message", "moved "+name, name)
}

// TreeEntry is one entry of a raw tree built with [Repo.WriteTree].
type TreeEntry struct {
	Mode   string
	Name   string
	Object vcs.Hash
}

// WriteBlob stores content as a blob and returns its id.
func (r *Repo) WriteBlob(content string) vcs.Hash {
	r.t.Helper()
	return vcs.Hash(r.gitWithInput([]byte(content), "hash-object", "-w", "--stdin"))
}

// WriteTree stores a tree object without any validation, which lets tests
// build trees that git itself would never create, such as entries named
// "..". Entries are sorted by name as git expects.
func (r *Repo) WriteTree(entries []TreeEntry) vcs.Hash {
	r.t.Helper()

	sorted := slices.Clone(entries)
	slices.SortFunc(sorted, func(a, b TreeEntry) int { return strings.Compare(a.Name, b.Name) })

	var raw bytes.Buffer
	for _, entry := range sorted {
		id, err := hex.DecodeString(string(entry.Object))
		if err != nil {
			r.t.Fatalf("decode object id %q: %v", entry.Object, err)
		}
		fmt.Fprintf(&raw, "%s %s\x00", entry.Mode, entry.Name)
		raw.Write(id)
	}
	return vcs.Hash(r.gitWithInput(raw.Bytes(), "hash-object", "-t", "tree", "-w", "--literally", "--stdin"))
}

// CommitTree commits a tree object on the given branch and returns the
// commit.
func (r *Repo) CommitTree(tree vcs.Hash, branch string) vcs.Hash {
	r.t.Helper()

	commit := vcs.Hash(r.Git("commit-tree", string(tree), "-m", "crafted tree"))
	r.Git("update-ref", "refs/heads/"+branch, string(commit))
	return commit
}
