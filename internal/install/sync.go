package install

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/pkgshed/aska/internal/state"
)

// SyncOptions controls [Engine.Sync].
type SyncOptions struct {
	// Force replaces directories that aska cannot prove it installed.
	Force bool
}

// Sync installs exactly what the lockfile records, without resolving any
// version. It fails when a requested skill has no matching lock, because
// installing it would require a resolution. Cached snapshots are reused,
// so a sync works offline when the cache is warm.
func (e *Engine) Sync(ctx context.Context, scope state.Scope, options SyncOptions) ([]Installed, error) {
	var installed []Installed
	err := e.withScope(ctx, scope, func(current *state.State) error {
		if err := checkLocked(current); err != nil {
			return err
		}

		for _, name := range current.Names() {
			result, err := e.syncOne(ctx, scope, current, name, options)
			if err != nil {
				return err
			}
			installed = append(installed, result)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return installed, nil
}

// checkLocked ensures every requested skill has a lock that matches it.
func checkLocked(current *state.State) error {
	var outdated []string
	for _, name := range current.Names() {
		if _, locked := current.CurrentLock(name); !locked {
			outdated = append(outdated, name)
		}
	}
	if len(outdated) > 0 {
		return fmt.Errorf("%w for %s; run aska update to resolve them", ErrLockfileOutdated, strings.Join(outdated, ", "))
	}
	return nil
}

func (e *Engine) syncOne(ctx context.Context, scope state.Scope, current *state.State, name string,
	options SyncOptions,
) (Installed, error) {
	requested := current.Skills[name]
	lock, _ := current.CurrentLock(name)

	snap, err := e.lockedSnapshot(ctx, lock)
	if err != nil {
		return Installed{}, fmt.Errorf("sync %s: %w", name, err)
	}
	if err := e.validateForAgents(name, snap.metadata, requested.Agents); err != nil {
		return Installed{}, err
	}

	targets, err := e.planTargets(scope, name, requested.Agents)
	if err != nil {
		return Installed{}, err
	}

	publish := publication{
		ownership: ownership{
			engine:    e,
			pkg:       requested.Package,
			previous:  lock.Digest,
			wanted:    lock.Digest,
			overwrite: options.Force,
		},
		source:  snap.dir,
		targets: targets,
	}
	changed, err := publish.apply()
	if err != nil {
		return Installed{}, err
	}

	e.Logger.Debug("synced", slog.String("skill", name), slog.String("commit", lock.Commit.Short()))
	return Installed{Name: name, Skill: requested, Lock: lock, Dirs: targetDirs(targets), Changed: changed}, nil
}
