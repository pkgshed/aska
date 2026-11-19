package install

import (
	"context"
	"fmt"
	"slices"

	"github.com/pkgshed/aska/internal/resolve"
	"github.com/pkgshed/aska/internal/state"
	"github.com/pkgshed/aska/internal/vcs"
)

// UpdateOptions controls [Engine.Update].
type UpdateOptions struct {
	// Force replaces directories that aska cannot prove it installed.
	Force bool
}

// UpdateResult reports the outcome of [Engine.Update].
type UpdateResult struct {
	// Changes lists the skills that need an update.
	Changes  []Change
	UpToDate []string
	// Applied reports whether the changes were installed; it is false when
	// the reviewer declined.
	Applied   bool
	Installed []Installed
}

// Update resolves the requested version of each named skill, or of every
// skill when names is empty, and installs the ones that changed once the
// reviewer agrees. Resolution and review happen without holding the scope
// lock; before applying, the scope is checked for concurrent changes to the
// affected skills.
func (e *Engine) Update(ctx context.Context, scope state.Scope, names []string, reviewer Reviewer,
	options UpdateOptions,
) (UpdateResult, error) {
	snapshot, err := scope.Load(e.Policy)
	if err != nil {
		return UpdateResult{}, err
	}
	if len(names) == 0 {
		names = snapshot.Names()
	}

	var result UpdateResult
	for _, name := range names {
		change, upToDate, err := e.planUpdate(ctx, scope, snapshot, name, options)
		if err != nil {
			return UpdateResult{}, err
		}
		if upToDate {
			result.UpToDate = append(result.UpToDate, name)
			continue
		}
		result.Changes = append(result.Changes, change)
	}

	if len(result.Changes) == 0 {
		return result, nil
	}

	approved, err := review(ctx, reviewer, result.Changes)
	if err != nil || !approved {
		return result, err
	}

	result.Installed, err = e.applyChanges(ctx, scope, snapshot, result.Changes, options.Force)
	if err != nil {
		return result, err
	}
	result.Applied = true
	return result, nil
}

// planUpdate resolves one skill and reports whether it is already current.
// A skill whose lock no longer matches its manifest entry is re-recorded
// even when its content stays the same.
func (e *Engine) planUpdate(ctx context.Context, scope state.Scope, snapshot *state.State, name string,
	options UpdateOptions,
) (Change, bool, error) {
	requested, found := snapshot.Skills[name]
	if !found {
		return Change{}, false, fmt.Errorf("%w: %q", ErrNotInstalled, name)
	}

	prepared, err := e.prepare(ctx, Request{Package: requested.Package, Query: requested.Requested})
	if err != nil {
		return Change{}, false, err
	}
	change, err := e.describe(ctx, scope, snapshot, prepared, AddOptions{Force: options.Force})
	if err != nil {
		return Change{}, false, err
	}

	if change.HasCurrent {
		if err := checkTagUnmoved(change.Current, change.Next); err != nil {
			return Change{}, false, err
		}
	}

	_, lockMatches := snapshot.CurrentLock(name)
	upToDate := lockMatches && change.Name == name && change.Current.Commit == change.Next.Commit &&
		!change.NeedsReview()
	return change, upToDate, nil
}

// checkTagUnmoved fails when the locked tag now names a different commit.
func checkTagUnmoved(current, next state.Lock) error {
	if current.Tag == "" || current.Tag != next.Tag || current.Commit == next.Commit {
		return nil
	}
	return fmt.Errorf("%w: %s of %s pointed at %s when locked and now points at %s; "+
		"inspect the change and add the skill again to accept it",
		ErrTagMoved, current.Tag, current.Name, current.Commit.Short(), next.Commit.Short())
}

// sameEntry reports whether a skill and its lock are unchanged between two
// states.
func sameEntry(before, after *state.State, name string) bool {
	skillBefore, wantedBefore := before.Skills[name]
	skillAfter, wantedAfter := after.Skills[name]
	if wantedBefore != wantedAfter {
		return false
	}

	sameSkill := skillBefore.Package == skillAfter.Package &&
		querySpelling(skillBefore.Requested) == querySpelling(skillAfter.Requested) &&
		slices.Equal(skillBefore.Agents, skillAfter.Agents)

	lockBefore, lockedBefore := before.Locks[name]
	lockAfter, lockedAfter := after.Locks[name]
	sameLock := lockedBefore == lockedAfter && lockBefore == lockAfter

	return sameSkill && sameLock
}

func querySpelling(query resolve.Query) string {
	if query == nil {
		return ""
	}
	return query.String()
}

// DiffResult holds the changes between the locked and a candidate commit.
type DiffResult struct {
	Name  string
	From  state.Lock
	To    resolve.Resolution
	Patch []byte
}

// Diff shows how a skill would change. The target defaults to the version
// an update would select; query overrides it when not nil.
func (e *Engine) Diff(ctx context.Context, scope state.Scope, name string, query resolve.Query) (DiffResult, error) {
	current, err := scope.Load(e.Policy)
	if err != nil {
		return DiffResult{}, err
	}

	requested, found := current.Skills[name]
	if !found {
		return DiffResult{}, fmt.Errorf("%w: %q in %s", ErrNotInstalled, name, scope)
	}
	lock, locked := current.Locks[name]
	if !locked {
		return DiffResult{}, fmt.Errorf("%w for %s; run aska update first", ErrLockfileOutdated, name)
	}
	if query == nil {
		query = requested.Requested
	}

	resolution, repo, err := e.resolve(ctx, Request{Package: requested.Package, Query: query})
	if err != nil {
		return DiffResult{}, err
	}
	if err := repo.Fetch(ctx, lock.Commit, ""); err != nil {
		return DiffResult{}, err
	}

	patch, err := repo.Diff(ctx, lock.Commit, resolution.Commit, requested.Package.Path.String(),
		vcs.DiffOptions{MaxBytes: maxDiffBytes})
	if err != nil {
		return DiffResult{}, err
	}
	return DiffResult{Name: name, From: lock, To: resolution, Patch: patch}, nil
}
