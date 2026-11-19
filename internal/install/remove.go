package install

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/pkgshed/aska/internal/state"
)

// RemoveOptions controls [Engine.Remove].
type RemoveOptions struct {
	// Force deletes directories even when their content has local changes.
	Force bool
}

// Remove deletes the installed directories of the named skills and drops
// them from the manifest and lockfile. Every directory is checked before
// any is deleted; directories whose content aska cannot prove it installed
// abort the removal unless Force is set.
func (e *Engine) Remove(ctx context.Context, scope state.Scope, names []string, options RemoveOptions) error {
	return e.withScope(ctx, scope, func(current *state.State) error {
		removals := map[string][]target{}
		for _, name := range names {
			targets, err := e.checkRemoval(scope, current, name, options)
			if err != nil {
				return err
			}
			removals[name] = targets
		}

		for _, name := range names {
			for _, item := range removals[name] {
				if err := removeDir(item.dir); err != nil {
					return err
				}
			}
			current.Delete(name)
			e.Logger.Debug("removed", slog.String("skill", name), slog.Any("dirs", targetDirs(removals[name])))
		}
		return scope.Save(current)
	})
}

// checkRemoval returns the directories of a skill after verifying that each
// may be deleted.
func (e *Engine) checkRemoval(scope state.Scope, current *state.State, name string,
	options RemoveOptions,
) ([]target, error) {
	requested, found := current.Skills[name]
	if !found {
		return nil, fmt.Errorf("%w: %q in %s", ErrNotInstalled, name, scope)
	}

	targets, err := e.planTargets(scope, name, requested.Agents)
	if err != nil {
		return nil, err
	}

	owner := ownership{
		engine:    e,
		pkg:       requested.Package,
		previous:  current.Locks[name].Digest,
		overwrite: options.Force,
	}
	for _, item := range targets {
		if _, err := owner.check(item.dir); err != nil {
			return nil, err
		}
	}
	return targets, nil
}
