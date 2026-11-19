package cli

import (
	"context"
	"errors"
	"fmt"

	"github.com/pkgshed/aska/internal/install"
	"github.com/pkgshed/aska/internal/state"
)

// errVerificationFailed reports skills whose files differ from the lock.
var errVerificationFailed = errors.New("verification failed")

// ListCmd lists installed skills.
type ListCmd struct {
	ScopeFlags
}

// Run implements the list command.
func (c *ListCmd) Run(ctx context.Context, app *App) error {
	scope, err := app.scope(c.ScopeFlags)
	if err != nil {
		return err
	}

	reports, err := app.engine.Report(ctx, scope, nil)
	if err != nil {
		return err
	}
	if len(reports) == 0 {
		app.out.line("No skills installed in the %s.", scopeLabel(scope))
		return nil
	}

	rows := make([][]string, 0, len(reports))
	for _, report := range reports {
		rows = append(rows, []string{
			report.Name,
			report.Skill.Requested.String(),
			resolvedOrDash(report),
			revisionOrDash(report),
			joinAgents(report.Skill.Agents),
			app.out.status(report.Status().String()),
		})
	}
	return app.out.table([]string{"NAME", "REQUESTED", "RESOLVED", "REVISION", "AGENTS", "STATUS"}, rows)
}

func resolvedOrDash(report install.SkillReport) string {
	if !report.Locked {
		return "-"
	}
	return resolvedLabel(report.Lock)
}

func revisionOrDash(report install.SkillReport) string {
	if !report.Locked {
		return "-"
	}
	return report.Lock.Commit.Short()
}

// InfoCmd shows one skill in detail.
type InfoCmd struct {
	ScopeFlags
	Name string `arg:"" name:"skill" help:"Name of an installed skill."`
}

// Run implements the info command.
func (c *InfoCmd) Run(ctx context.Context, app *App) error {
	scope, err := app.scope(c.ScopeFlags)
	if err != nil {
		return err
	}

	reports, err := app.engine.Report(ctx, scope, []string{c.Name})
	if err != nil {
		return err
	}
	report := reports[0]

	description := "-"
	if metadata, err := app.engine.Metadata(report); err == nil && metadata.Description != "" {
		description = sanitize(metadata.Description)
	}

	app.printInfo(scope, report, description)
	return nil
}

func (a *App) printInfo(scope state.Scope, report install.SkillReport, description string) {
	field := func(label, value string) { a.out.line("%-12s %s", label+":", value) }

	field("Name", report.Name)
	field("Description", description)
	field("Package", report.Skill.Package.String())
	field("Source", report.Skill.Package.Repo.String())
	field("Path", report.Skill.Package.Path.String())
	field("Requested", report.Skill.Requested.String())
	if report.Locked {
		field("Resolved", report.Lock.Describe())
		field("Revision", report.Lock.Commit.String())
		field("Digest", "sha256:"+report.Lock.Digest.String())
	} else {
		field("Resolved", "not locked; run aska update")
	}
	field("Agents", joinAgents(report.Skill.Agents))
	field("Scope", scope.String())

	a.out.line("Installed:")
	for _, item := range report.Targets {
		a.out.line("  %-8s %s (%s)", a.out.status(item.Status.String()),
			a.relativeToScope(scope, item.Dir), joinAgents(item.Agents))
	}
}

// VerifyCmd checks installed files.
type VerifyCmd struct {
	ScopeFlags
	Names []string `arg:"" optional:"" name:"skill" help:"Skills to verify; all when omitted."`
}

// Run implements the verify command.
func (c *VerifyCmd) Run(ctx context.Context, app *App) error {
	scope, err := app.scope(c.ScopeFlags)
	if err != nil {
		return err
	}

	reports, err := app.engine.Report(ctx, scope, c.Names)
	if err != nil {
		return err
	}

	failed := 0
	for _, report := range reports {
		if !report.Healthy() {
			failed++
		}
		app.printVerification(scope, report)
	}

	if failed > 0 {
		return fmt.Errorf("%w: %d of %d skill(s) differ from %s; run aska sync to restore them",
			errVerificationFailed, failed, len(reports), state.LockfileName)
	}
	app.out.success("All %d skill(s) match %s", len(reports), state.LockfileName)
	return nil
}

func (a *App) printVerification(scope state.Scope, report install.SkillReport) {
	if !report.Locked {
		a.out.line("%-8s %s: no matching entry in %s", a.out.status("unlocked"), report.Name, state.LockfileName)
		return
	}
	for _, item := range report.Targets {
		a.out.line("%-8s %s", a.out.status(item.Status.String()), a.relativeToScope(scope, item.Dir))
	}
}
