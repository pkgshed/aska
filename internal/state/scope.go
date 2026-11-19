package state

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/gofrs/flock"

	"github.com/pkgshed/aska/internal/agent"
	"github.com/pkgshed/aska/internal/source"
)

// ProjectStateDir is the directory inside a repository that holds the
// project manifest and lockfile.
const ProjectStateDir = ".aska"

const lockRetryDelay = 50 * time.Millisecond

// ErrNotInRepository reports a project operation outside a git repository.
var ErrNotInRepository = errors.New("not inside a git repository")

// Scope is a set of installed skills with its own manifest and lockfile.
type Scope struct {
	// Kind distinguishes project from user installations.
	Kind agent.Scope
	// Dir holds the manifest and the lockfile.
	Dir string
	// ProjectRoot is the repository root for project scopes.
	ProjectRoot string
	// lockPath is the file that serializes writers of this scope.
	lockPath string
}

// NewUserScope returns the scope of the user's own skills, recorded in
// stateDir.
func NewUserScope(stateDir string) Scope {
	return Scope{
		Kind:     agent.UserScope,
		Dir:      stateDir,
		lockPath: lockPath(stateDir, stateDir),
	}
}

// NewProjectScope returns the scope of a repository's skills, recorded in
// its .aska directory. The lock lives in the user's stateDir so that it
// never shows up in the repository.
func NewProjectScope(projectRoot, stateDir string) Scope {
	dir := filepath.Join(projectRoot, ProjectStateDir)
	return Scope{
		Kind:        agent.ProjectScope,
		Dir:         dir,
		ProjectRoot: projectRoot,
		lockPath:    lockPath(stateDir, dir),
	}
}

func lockPath(stateDir, scopeDir string) string {
	sum := sha256.Sum256([]byte(scopeDir))
	return filepath.Join(stateDir, "locks", hex.EncodeToString(sum[:8])+".lock")
}

// FindProjectRoot walks up from dir to the nearest directory containing a
// .git entry. It inspects the filesystem only and never runs git, so a
// repository's configuration cannot influence the answer.
func FindProjectRoot(dir string) (string, error) {
	current, err := filepath.Abs(dir)
	if err != nil {
		return "", fmt.Errorf("find project root: %w", err)
	}

	for {
		if _, err := os.Lstat(filepath.Join(current, ".git")); err == nil {
			return current, nil
		}

		parent := filepath.Dir(current)
		if parent == current {
			return "", fmt.Errorf("%w: %s", ErrNotInRepository, dir)
		}
		current = parent
	}
}

// ManifestPath returns the path of skills.toml.
func (s Scope) ManifestPath() string { return filepath.Join(s.Dir, ManifestName) }

// LockfilePath returns the path of skillslock.toml.
func (s Scope) LockfilePath() string { return filepath.Join(s.Dir, LockfileName) }

// Location returns the anchor for agent skill directories.
func (s Scope) Location() agent.Location {
	return agent.Location{Scope: s.Kind, ProjectRoot: s.ProjectRoot}
}

// String names the scope for messages.
func (s Scope) String() string {
	if s.Kind == agent.UserScope {
		return "user scope"
	}
	return "project " + s.ProjectRoot
}

// Lock takes the exclusive lock of the scope, waiting until it is free or
// ctx ends. Every read-modify-write cycle of the manifest, the lockfile
// and the installed directories must happen under this lock. Once held,
// leftovers of interrupted writes are removed.
func (s Scope) Lock(ctx context.Context) (func(), error) {
	if err := os.MkdirAll(filepath.Dir(s.lockPath), 0o755); err != nil {
		return nil, fmt.Errorf("lock %s: %w", s, err)
	}

	fileLock := flock.New(s.lockPath)
	locked, err := fileLock.TryLockContext(ctx, lockRetryDelay)
	switch {
	case err != nil:
		return nil, fmt.Errorf("lock %s: %w", s, err)
	case !locked:
		return nil, fmt.Errorf("lock %s: busy", s)
	}

	unlock := func() { _ = fileLock.Unlock() }
	if err := s.removeTemporaryFiles(); err != nil {
		unlock()
		return nil, err
	}
	return unlock, nil
}

// removeTemporaryFiles deletes temporary files that an interrupted write
// left next to the manifest or lockfile. The atomic rename means the real
// files are intact.
func (s Scope) removeTemporaryFiles() error {
	var errs []error
	for _, name := range []string{ManifestName, LockfileName} {
		leftovers, err := filepath.Glob(filepath.Join(s.Dir, temporaryPattern(name)))
		if err != nil {
			return fmt.Errorf("find temporary files: %w", err)
		}
		for _, leftover := range leftovers {
			errs = append(errs, os.Remove(leftover))
		}
	}

	if err := errors.Join(errs...); err != nil {
		return fmt.Errorf("remove temporary files: %w", err)
	}
	return nil
}

// Load reads the manifest and the lockfile. Missing files yield an empty
// state.
func (s Scope) Load(policy source.Policy) (*State, error) {
	skills, err := loadManifest(s.ManifestPath(), policy)
	if err != nil {
		return nil, err
	}
	locks, err := loadLockfile(s.LockfilePath(), policy)
	if err != nil {
		return nil, err
	}
	return &State{Skills: skills, Locks: locks}, nil
}

// Save writes the lockfile and then the manifest, each atomically. Locks
// of skills that are no longer requested are dropped.
func (s Scope) Save(current *State) error {
	locks := make(map[string]Lock, len(current.Locks))
	for name, lock := range current.Locks {
		if _, wanted := current.Skills[name]; wanted {
			locks[name] = lock
		}
	}

	lockfile, err := encodeLockfile(locks)
	if err != nil {
		return fmt.Errorf("encode lockfile: %w", err)
	}
	manifest, err := encodeManifest(current.Skills)
	if err != nil {
		return fmt.Errorf("encode manifest: %w", err)
	}

	if err := os.MkdirAll(s.Dir, 0o755); err != nil {
		return fmt.Errorf("create state directory: %w", err)
	}
	if err := writeFileAtomic(s.LockfilePath(), lockfile); err != nil {
		return fmt.Errorf("write lockfile: %w", err)
	}
	if err := writeFileAtomic(s.ManifestPath(), manifest); err != nil {
		return fmt.Errorf("write manifest: %w", err)
	}
	return nil
}
