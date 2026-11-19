package cli

import (
	"context"
	"path/filepath"
	"strings"

	"github.com/pkgshed/aska/internal/install"
	"github.com/pkgshed/aska/internal/state"
)

// AddCmd installs skills.
type AddCmd struct {
	ScopeFlags
	ReviewFlags
	Agent   []string `short:"a" sep:"," placeholder:"AGENT" help:"${agent_help}"`
	Force   bool     `help:"Replace existing directories that aska did not install."`
	Sources []string `arg:"" name:"package" help:"${package_help}"`
}

// Run implements the add command.
func (c *AddCmd) Run(ctx context.Context, app *App) error {
	scope, err := app.scope(c.ScopeFlags)
	if err != nil {
		return err
	}

	options, err := c.options(app)
	if err != nil {
		return err
	}

	requests := make([]install.Request, 0, len(c.Sources))
	for _, text := range c.Sources {
		request, err := app.request(ctx, text)
		if err != nil {
			return err
		}
		requests = append(requests, request)
	}

	app.out.heading("Installing %d skill(s) into the %s", len(requests), scopeLabel(scope))
	result, err := app.engine.Add(ctx, scope, requests, app.reviewer(scope, c.ReviewFlags, "installation"), options)
	if err != nil {
		return err
	}
	if !result.Applied {
		app.out.line("No changes were made.")
		return nil
	}

	for _, installed := range result.Installed {
		app.printInstalled(scope, installed)
	}
	return nil
}

func (c *AddCmd) options(app *App) (install.AddOptions, error) {
	explicit, err := app.selectedAgents(c.Agent)
	if err != nil {
		return install.AddOptions{}, err
	}
	defaults, err := app.defaultAgents()
	if err != nil {
		return install.AddOptions{}, err
	}
	return install.AddOptions{Agents: explicit, DefaultAgents: defaults, Force: c.Force}, nil
}

// RemoveCmd uninstalls skills.
type RemoveCmd struct {
	ScopeFlags
	Force bool     `help:"Delete directories even when they have local changes."`
	Names []string `arg:"" name:"skill" help:"Names of installed skills."`
}

// Run implements the remove command.
func (c *RemoveCmd) Run(ctx context.Context, app *App) error {
	scope, err := app.scope(c.ScopeFlags)
	if err != nil {
		return err
	}

	if err := app.engine.Remove(ctx, scope, c.Names, install.RemoveOptions{Force: c.Force}); err != nil {
		return err
	}
	for _, name := range c.Names {
		app.out.success("Removed %s", name)
	}
	return nil
}

// SyncCmd installs the lockfile.
type SyncCmd struct {
	ScopeFlags
	Force bool `help:"Replace existing directories that aska did not install or that have local changes."`
}

// Run implements the sync command.
func (c *SyncCmd) Run(ctx context.Context, app *App) error {
	scope, err := app.scope(c.ScopeFlags)
	if err != nil {
		return err
	}

	app.out.heading("Synchronizing the %s with %s", scopeLabel(scope), state.LockfileName)
	installed, err := app.engine.Sync(ctx, scope, install.SyncOptions{Force: c.Force})
	if err != nil {
		return err
	}

	changed := 0
	for _, result := range installed {
		if result.Changed {
			changed++
			app.printInstalled(scope, result)
		}
	}
	app.out.success("%d of %d skill(s) needed changes", changed, len(installed))
	return nil
}

// printInstalled reports an installed skill, distinguishing the requested
// query, the resolved version and the installed revision.
func (a *App) printInstalled(scope state.Scope, result install.Installed) {
	a.out.success("%s %s", result.Name, result.Lock.Describe())
	a.out.detail("requested %s · revision %s · digest %s",
		result.Skill.Requested, result.Lock.Commit.Short(), result.Lock.Digest.Short())
	for _, dir := range result.Dirs {
		a.out.detail("→ %s", a.relativeToScope(scope, dir))
	}
}

func (a *App) relativeToScope(scope state.Scope, dir string) string {
	if scope.ProjectRoot != "" {
		if relative, err := filepath.Rel(scope.ProjectRoot, dir); err == nil && !strings.HasPrefix(relative, "..") {
			return relative
		}
	}
	return a.out.displayPath(dir)
}

func scopeLabel(scope state.Scope) string {
	if scope.ProjectRoot == "" {
		return "user scope"
	}
	return "project"
}
