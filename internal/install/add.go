package install

import (
	"context"
	"log/slog"
	"slices"

	"github.com/pkgshed/aska/internal/agent"
	"github.com/pkgshed/aska/internal/state"
)

// AddOptions controls [Engine.Add].
type AddOptions struct {
	// Agents selects the agents explicitly. When empty, a skill that is
	// already installed keeps its agents and a new one uses DefaultAgents.
	Agents []agent.Name
	// DefaultAgents applies to new skills when Agents is empty.
	DefaultAgents []agent.Name
	// Force replaces directories that aska cannot prove it installed.
	Force bool
}

// Installed reports one skill that an operation installed.
type Installed struct {
	Name    string
	Skill   state.Skill
	Lock    state.Lock
	Dirs    []string
	Changed bool
}

// AddResult reports the outcome of [Engine.Add].
type AddResult struct {
	// Changes lists every planned installation.
	Changes []Change
	// Applied reports whether the changes were installed; it is false when
	// the reviewer declined.
	Applied   bool
	Installed []Installed
}

// Add resolves, reviews, installs and records the requested packages.
// Resolution, extraction and review happen without holding the scope lock,
// so slow network operations and a pending review do not block other
// processes; the scope is checked for concurrent changes before applying.
func (e *Engine) Add(ctx context.Context, scope state.Scope, requests []Request, reviewer Reviewer,
	options AddOptions,
) (AddResult, error) {
	snapshot, err := scope.Load(e.Policy)
	if err != nil {
		return AddResult{}, err
	}

	var result AddResult
	for _, request := range requests {
		prepared, err := e.prepare(ctx, request)
		if err != nil {
			return AddResult{}, err
		}
		change, err := e.describe(ctx, scope, snapshot, prepared, options)
		if err != nil {
			return AddResult{}, err
		}
		result.Changes = append(result.Changes, change)
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

// installChange publishes a reviewed change and records it in current.
func (e *Engine) installChange(scope state.Scope, current *state.State, change Change, force bool) (Installed, error) {
	owner := change.ownership(e, force)
	publish := publication{ownership: owner, source: change.candidate.snapshot.dir, targets: change.targets}
	changed, err := publish.apply()
	if err != nil {
		return Installed{}, err
	}

	if change.previousName != "" {
		if err := e.retireTargets(scope, current, change.previousName, change.targets, owner); err != nil {
			return Installed{}, err
		}
		current.Delete(change.previousName)
	}

	requested := state.Skill{
		Name:      change.Name,
		Package:   change.Package,
		Requested: change.candidate.request.Query,
		Agents:    change.Agents,
	}
	current.Put(requested, change.Next)

	e.Logger.Debug("installed", slog.String("skill", change.Name), slog.Any("dirs", change.Dirs))
	return Installed{
		Name:    change.Name,
		Skill:   requested,
		Lock:    change.Next,
		Dirs:    change.Dirs,
		Changed: changed || change.Current.Commit != change.Next.Commit,
	}, nil
}

// selectAgents applies the agent selection rules of [AddOptions].
func selectAgents(options AddOptions, existing []agent.Name, wasRequested bool) []agent.Name {
	agents := options.Agents
	if len(agents) == 0 && wasRequested {
		agents = existing
	}
	if len(agents) == 0 {
		agents = options.DefaultAgents
	}

	sorted := slices.Clone(agents)
	slices.Sort(sorted)
	return slices.Compact(sorted)
}

// retireTargets removes the directories a previous installation of a skill
// used that the new installation no longer needs, for example after the
// skill was renamed or an agent was dropped.
func (e *Engine) retireTargets(scope state.Scope, current *state.State, previousName string,
	keep []target, owner ownership,
) error {
	previous, found := current.Skills[previousName]
	if !found {
		return nil
	}

	before, err := e.planTargets(scope, previousName, previous.Agents)
	if err != nil {
		return err
	}

	for _, dir := range obsoleteDirs(before, keep) {
		if err := e.removeOwned(dir, owner); err != nil {
			return err
		}
	}
	return nil
}

// removeOwned deletes a directory if aska owns its content.
func (e *Engine) removeOwned(dir string, owner ownership) error {
	status, err := owner.check(dir)
	switch {
	case err != nil:
		return err
	case status == statusMissing:
		return nil
	}
	return removeDir(dir)
}
