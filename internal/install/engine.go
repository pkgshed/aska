// Package install orchestrates the life cycle of installed skills.
//
// An installation resolves a version query to a commit, fetches the commit
// into the shared bare repository, extracts the skill directory into the
// content-addressed cache, stages a copy next to each agent's skill
// directory, publishes it with a rename and finally records the result in
// the manifest and lockfile. The records change only after every directory
// has been published, and directories are only replaced when their content
// proves that aska installed them.
package install

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/pkgshed/aska/internal/agent"
	"github.com/pkgshed/aska/internal/cache"
	"github.com/pkgshed/aska/internal/digest"
	"github.com/pkgshed/aska/internal/extract"
	"github.com/pkgshed/aska/internal/resolve"
	"github.com/pkgshed/aska/internal/source"
	"github.com/pkgshed/aska/internal/state"
	"github.com/pkgshed/aska/internal/vcs"
)

var (
	// ErrNotInstalled reports a skill name that the scope does not know.
	ErrNotInstalled = errors.New("skill not installed")

	// ErrNameConflict reports two packages competing for one skill name.
	ErrNameConflict = errors.New("skill name conflict")

	// ErrUnmanagedTarget reports an existing directory that aska cannot
	// prove it installed, so overwriting or deleting it could lose data.
	ErrUnmanagedTarget = errors.New("unmanaged directory")

	// ErrDigestMismatch reports content that differs from the locked digest.
	ErrDigestMismatch = errors.New("content digest mismatch")

	// ErrLockfileOutdated reports manifest entries without a matching lock.
	ErrLockfileOutdated = errors.New("lockfile out of date")

	// ErrStateChanged reports that another process changed the scope while
	// an update waited for approval.
	ErrStateChanged = errors.New("scope changed concurrently")

	// ErrNoAgents reports an installation without any target agent.
	ErrNoAgents = errors.New("no agents selected")

	// ErrTagMoved reports an upstream tag that now points elsewhere than
	// the locked commit.
	ErrTagMoved = errors.New("upstream tag moved")
)

// Provenance describes a snapshot that is about to be installed.
type Provenance struct {
	Package    source.Package
	Resolution resolve.Resolution
	Digest     digest.Digest
}

// TrustPolicy vets a snapshot before installation. A git commit id proves
// content identity, not who published it; signature verification or a
// trusted registry can be plugged in here without touching the pipeline.
type TrustPolicy interface {
	Check(ctx context.Context, provenance Provenance) error
}

// Engine performs installations. All fields are required except Trust.
type Engine struct {
	Repos  *vcs.Store
	Cache  *cache.Store
	Agents agent.Registry
	Limits extract.Limits
	Policy source.Policy
	Logger *slog.Logger
	// Trust holds additional policies every snapshot must satisfy.
	Trust []TrustPolicy
}

// Request asks for one package at one version query.
type Request struct {
	Package source.Package
	Query   resolve.Query
	// Unversioned marks a request that named no version. Like "cargo add",
	// it records a caret range anchored at the resolved release instead of
	// "latest", so later updates stay within the same major version.
	Unversioned bool
}

func (r Request) String() string {
	return fmt.Sprintf("%s@%s", r.Package, r.Query)
}

// withScope loads the state of a scope under its lock and passes it to fn.
func (e *Engine) withScope(ctx context.Context, scope state.Scope, fn func(*state.State) error) error {
	unlock, err := scope.Lock(ctx)
	if err != nil {
		return err
	}
	defer unlock()

	if err := e.removeStaleStaging(scope); err != nil {
		return err
	}

	current, err := scope.Load(e.Policy)
	if err != nil {
		return err
	}
	return fn(current)
}

func (e *Engine) checkTrust(ctx context.Context, provenance Provenance) error {
	for _, policy := range e.Trust {
		if err := policy.Check(ctx, provenance); err != nil {
			return fmt.Errorf("trust policy rejected %s: %w", provenance.Package, err)
		}
	}
	return nil
}
