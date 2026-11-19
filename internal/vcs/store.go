package vcs

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/gofrs/flock"
)

// lockRetryDelay is how often a blocked lock acquisition retries.
const lockRetryDelay = 50 * time.Millisecond

// ErrInvalidKey reports a repository key that is not a local relative path.
var ErrInvalidKey = errors.New("invalid repository key")

// Store keeps one bare repository per remote below Root.
type Store struct {
	// Root is the directory holding the repositories.
	Root string
	// Git runs the git commands.
	Git *Git
}

// ListRemote lists the refs of remote without touching the store.
func (s *Store) ListRemote(ctx context.Context, remote string) (RemoteRefs, error) {
	if err := os.MkdirAll(s.Root, 0o755); err != nil {
		return RemoteRefs{}, fmt.Errorf("create repository store: %w", err)
	}
	return s.Git.ListRemote(ctx, s.Root, remote)
}

// Open returns the bare repository stored under key, creating it when it
// does not exist yet. The key is a slash-separated relative path that the
// caller derives from the canonical remote identity, such as
// "github.com/owner/repo.git". A new repository uses format; when format is
// unknown, Open asks the remote.
func (s *Store) Open(ctx context.Context, key, remote string, format ObjectFormat) (*Repo, error) {
	if !filepath.IsLocal(filepath.FromSlash(key)) {
		return nil, fmt.Errorf("%w %q", ErrInvalidKey, key)
	}

	repo := &Repo{
		dir:    filepath.Join(s.Root, filepath.FromSlash(key)),
		remote: remote,
		git:    s.Git,
	}

	exists, err := repo.exists()
	if err != nil || exists {
		return repo, err
	}

	if err := s.initialize(ctx, repo, format); err != nil {
		return nil, err
	}
	return repo, nil
}

// initialize creates the bare repository under the repository lock. It
// initializes into a temporary sibling and renames it into place, so an
// interrupted initialization never leaves a half-built repository behind.
func (s *Store) initialize(ctx context.Context, repo *Repo, format ObjectFormat) error {
	unlock, err := repo.lock(ctx)
	if err != nil {
		return err
	}
	defer unlock()

	exists, err := repo.exists()
	if err != nil || exists {
		return err
	}

	if format == FormatUnknown {
		refs, err := s.ListRemote(ctx, repo.remote)
		if err != nil {
			return err
		}
		format = refs.Format()
	}

	temporary, err := os.MkdirTemp(filepath.Dir(repo.dir), ".init-*")
	if err != nil {
		return fmt.Errorf("create repository directory: %w", err)
	}
	defer os.RemoveAll(temporary)

	_, err = s.Git.run(ctx, "", temporary,
		"init", "--bare", "--quiet", "--template=", "--object-format="+string(format), temporary)
	if err != nil {
		return fmt.Errorf("initialize repository: %w", err)
	}

	if err := os.Rename(temporary, repo.dir); err != nil {
		return fmt.Errorf("publish repository: %w", err)
	}
	return nil
}

// Repo is a bare repository mirroring part of one remote.
type Repo struct {
	dir    string
	remote string
	git    *Git
}

// Dir returns the repository directory.
func (r *Repo) Dir() string { return r.dir }

func (r *Repo) exists() (bool, error) {
	info, err := os.Stat(filepath.Join(r.dir, "HEAD"))
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return false, nil
	case err != nil:
		return false, fmt.Errorf("inspect repository: %w", err)
	case !info.Mode().IsRegular():
		return false, fmt.Errorf("inspect repository: %s is not a bare repository", r.dir)
	default:
		return true, nil
	}
}

// lock takes the exclusive per-repository lock that serializes every
// operation that writes to the repository, across goroutines and processes.
func (r *Repo) lock(ctx context.Context) (func(), error) {
	if err := os.MkdirAll(filepath.Dir(r.dir), 0o755); err != nil {
		return nil, fmt.Errorf("create repository store: %w", err)
	}

	fileLock := flock.New(r.dir + ".lock")
	locked, err := fileLock.TryLockContext(ctx, lockRetryDelay)
	switch {
	case err != nil:
		return nil, fmt.Errorf("lock repository: %w", err)
	case !locked:
		return nil, fmt.Errorf("lock repository: %s is busy", r.dir)
	}

	return func() { _ = fileLock.Unlock() }, nil
}

func (r *Repo) run(ctx context.Context, args ...string) ([]byte, error) {
	return r.git.run(ctx, r.dir, r.dir, args...)
}
