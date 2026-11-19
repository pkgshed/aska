package vcs

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

// Local ref namespaces. Fetched refs live below refs/aska so they never
// mix with anything a user might create, and every fetched commit is
// pinned so that a manual garbage collection keeps it.
const (
	remoteRefPrefix = "refs/aska/remote/"
	pinRefPrefix    = "refs/aska/commits/"
)

var (
	// ErrCommitNotFound reports a commit the remote does not provide.
	ErrCommitNotFound = errors.New("commit not found")

	// ErrRefMoved reports a ref that no longer points at the expected commit.
	ErrRefMoved = errors.New("ref moved")

	// ErrUnknownRevision reports a revision that does not name a commit.
	ErrUnknownRevision = errors.New("unknown revision")
)

// RefMovedError carries the details of [ErrRefMoved].
type RefMovedError struct {
	Ref  string
	Want Hash
	Got  Hash
}

func (e *RefMovedError) Error() string {
	return fmt.Sprintf("%v: %s points at %s, expected %s", ErrRefMoved, e.Ref, e.Got.Short(), e.Want.Short())
}

func (e *RefMovedError) Unwrap() error { return ErrRefMoved }

// HasCommit reports whether the commit is present in the repository.
func (r *Repo) HasCommit(ctx context.Context, commit Hash) (bool, error) {
	_, err := r.run(ctx, "cat-file", "-e", string(commit)+"^{commit}")

	// A non-zero exit status is git's answer that the object is absent;
	// anything else, such as a missing executable, is a real failure.
	_, exited := errors.AsType[*exec.ExitError](err)
	switch {
	case err == nil:
		return true, nil
	case exited && ctx.Err() == nil:
		return false, nil
	default:
		return false, err
	}
}

// Fetch makes commit available locally. When ref is not empty it names the
// remote ref, such as "refs/tags/v1.2.3", that is expected to point at the
// commit; it is fetched by name, which works with every server, and
// verified afterwards. A tag that points elsewhere fails with
// [ErrRefMoved]; a branch that moved falls back to fetching the commit by
// id. Without a ref the commit is fetched by id directly. Fetching by id
// starts shallow and falls back to the full history of all branches and
// tags when the server refuses the shallow request.
func (r *Repo) Fetch(ctx context.Context, commit Hash, ref string) error {
	unlock, err := r.lock(ctx)
	if err != nil {
		return err
	}
	defer unlock()

	present, err := r.HasCommit(ctx, commit)
	if err != nil || present {
		return err
	}

	if ref != "" {
		err = r.fetchRef(ctx, commit, ref)
	} else {
		err = r.fetchCommit(ctx, commit)
	}

	// A branch that moved since it was listed is an ordinary race, unlike a
	// moved tag, so the resolved commit is fetched by id instead.
	if _, moved := errors.AsType[*RefMovedError](err); moved && strings.HasPrefix(ref, BranchPrefix) {
		err = r.fetchCommit(ctx, commit)
	}
	if err != nil {
		return err
	}

	return r.pin(ctx, commit)
}

// fetchRef fetches a single named ref shallowly and checks that it still
// points at the expected commit.
func (r *Repo) fetchRef(ctx context.Context, commit Hash, ref string) error {
	local := remoteRefPrefix + strings.TrimPrefix(ref, "refs/")
	if err := r.fetchShallow(ctx, "+"+ref+":"+local); err != nil {
		return fmt.Errorf("fetch %s: %w", ref, err)
	}

	got, err := r.resolve(ctx, local+"^{commit}")
	if err != nil {
		return fmt.Errorf("fetch %s: %w", ref, err)
	}
	if got != commit {
		return &RefMovedError{Ref: ref, Want: commit, Got: got}
	}

	return nil
}

// fetchCommit fetches a commit by id, falling back to the full history when
// the server does not allow fetching unadvertised objects.
func (r *Repo) fetchCommit(ctx context.Context, commit Hash) error {
	if err := r.fetchShallow(ctx, string(commit)); err == nil {
		return nil
	} else if ctx.Err() != nil {
		return err
	}

	if err := r.fetchHistory(ctx); err != nil {
		return err
	}

	present, err := r.HasCommit(ctx, commit)
	switch {
	case err != nil:
		return err
	case !present:
		return fmt.Errorf("%w: %s in %s", ErrCommitNotFound, commit, r.remote)
	default:
		return nil
	}
}

func (r *Repo) fetchShallow(ctx context.Context, refspec string) error {
	_, err := r.run(ctx, "fetch", "--quiet", "--no-tags", "--no-write-fetch-head",
		"--depth=1", r.remote, refspec)
	return err
}

// FetchHistory fetches every branch and tag with full history. Resolving an
// abbreviated commit id needs it.
func (r *Repo) FetchHistory(ctx context.Context) error {
	unlock, err := r.lock(ctx)
	if err != nil {
		return err
	}
	defer unlock()

	return r.fetchHistory(ctx)
}

func (r *Repo) fetchHistory(ctx context.Context) error {
	args := []string{"fetch", "--quiet", "--no-tags", "--no-write-fetch-head"}

	shallow, err := r.isShallow(ctx)
	if err != nil {
		return err
	}
	if shallow {
		args = append(args, "--unshallow")
	}

	args = append(args, r.remote,
		"+"+BranchPrefix+"*:"+remoteRefPrefix+"heads/*",
		"+"+TagPrefix+"*:"+remoteRefPrefix+"tags/*",
	)
	if _, err := r.run(ctx, args...); err != nil {
		return fmt.Errorf("fetch history: %w", err)
	}
	return nil
}

func (r *Repo) isShallow(ctx context.Context) (bool, error) {
	output, err := r.run(ctx, "rev-parse", "--is-shallow-repository")
	if err != nil {
		return false, fmt.Errorf("inspect repository: %w", err)
	}
	return strings.TrimSpace(string(output)) == "true", nil
}

// ResolveRevision resolves an abbreviated commit id against the objects
// that are present locally. Call [Repo.FetchHistory] first.
func (r *Repo) ResolveRevision(ctx context.Context, revision string) (Hash, error) {
	if !IsHashPrefix(revision) {
		return "", fmt.Errorf("%w %q: not an abbreviated commit id", ErrUnknownRevision, revision)
	}

	hash, err := r.resolve(ctx, revision+"^{commit}")
	if err != nil {
		return "", fmt.Errorf("%w %q: %w", ErrUnknownRevision, revision, err)
	}
	return hash, nil
}

func (r *Repo) resolve(ctx context.Context, revision string) (Hash, error) {
	output, err := r.run(ctx, "rev-parse", "--verify", "--quiet", "--end-of-options", revision)
	if err != nil {
		return "", err
	}
	return ParseHash(strings.TrimSpace(string(output)))
}

// pin records a ref for the commit so that it stays reachable.
func (r *Repo) pin(ctx context.Context, commit Hash) error {
	if _, err := r.run(ctx, "update-ref", pinRefPrefix+string(commit), string(commit)); err != nil {
		return fmt.Errorf("pin commit: %w", err)
	}
	return nil
}
