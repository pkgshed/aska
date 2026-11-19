package cli

import (
	"context"

	"github.com/pkgshed/aska/internal/install"
	"github.com/pkgshed/aska/internal/resolve"
)

// UpdateCmd updates skills.
type UpdateCmd struct {
	ScopeFlags
	ReviewFlags
	Force bool     `help:"Replace directories that have local changes."`
	Names []string `arg:"" optional:"" name:"skill" help:"Skills to update; all when omitted."`
}

// Run implements the update command.
func (c *UpdateCmd) Run(ctx context.Context, app *App) error {
	scope, err := app.scope(c.ScopeFlags)
	if err != nil {
		return err
	}

	app.out.heading("Checking for updates in the %s", scopeLabel(scope))
	reviewer := app.reviewer(scope, c.ReviewFlags, "the update")
	result, err := app.engine.Update(ctx, scope, c.Names, reviewer, install.UpdateOptions{Force: c.Force})
	if err != nil {
		return err
	}

	switch {
	case len(result.Changes) == 0:
		app.out.success("All %d skill(s) are up to date", len(result.UpToDate))
	case !result.Applied:
		app.out.line("No changes were made.")
	default:
		for _, installed := range result.Installed {
			app.out.success("Updated %s to %s (%s)", installed.Name, installed.Lock.Describe(), installed.Lock.Commit.Short())
		}
	}
	return nil
}

// DiffCmd shows pending changes.
type DiffCmd struct {
	ScopeFlags
	To   string `placeholder:"QUERY" help:"Compare against this version query instead of the requested one."`
	Name string `arg:"" name:"skill" help:"Name of an installed skill."`
}

// Run implements the diff command.
func (c *DiffCmd) Run(ctx context.Context, app *App) error {
	scope, err := app.scope(c.ScopeFlags)
	if err != nil {
		return err
	}

	var query resolve.Query
	if c.To != "" {
		if query, err = resolve.ParseQuery(c.To); err != nil {
			return err
		}
	}

	result, err := app.engine.Diff(ctx, scope, c.Name, query)
	if err != nil {
		return err
	}

	app.out.heading("%s: %s (%s) → %s (%s)", result.Name,
		resolvedLabel(result.From), result.From.Commit.Short(), result.To.Describe(), result.To.Commit.Short())
	if len(result.Patch) == 0 {
		app.out.line("No changes.")
		return nil
	}
	app.out.patch(string(result.Patch))
	return nil
}
