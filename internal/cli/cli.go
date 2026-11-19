// Package cli implements the aska command line.
package cli

import (
	"context"

	"github.com/alecthomas/kong"
)

// Version is the program version, set at build time with
// -ldflags "-X github.com/pkgshed/aska/internal/cli.Version=v1.2.3".
var Version = "devel"

const description = `Aska installs agent skills straight from git repositories.

Packages are addressed like Go packages: the repository is discovered from
the import path and the rest of the path is the skill directory, for example
github.com/owner/repo/skills/code-review. Append @<query> to choose a version:
a release (v1.2.3), a range (^1.2, ~1.2, >=1.2 <2), a branch, a tag or a
commit. Without --global, skills are installed into the git repository
around the working directory and recorded in .aska/skills.toml.`

const agentHelp = "Agents to install for: codex, claude, opencode or pi. Repeat the flag or separate names with " +
	"commas. Defaults to the configured agents; a reinstalled skill keeps its agents."

const packageHelp = "Packages such as github.com/owner/repo/skills/name@^1.2 or " +
	"git+https://host/repo.git//skills/name@v1.2.3."

// CLI is the command line grammar.
type CLI struct {
	Globals

	Add    AddCmd    `cmd:"" help:"Install skills from git repositories."`
	Remove RemoveCmd `cmd:"" aliases:"rm" help:"Uninstall skills."`
	List   ListCmd   `cmd:"" aliases:"ls" help:"List installed skills."`
	Info   InfoCmd   `cmd:"" help:"Show the details of an installed skill."`
	Update UpdateCmd `cmd:"" aliases:"up" help:"Update skills to the newest versions their queries allow."`
	Diff   DiffCmd   `cmd:"" help:"Show how a skill would change on update."`
	Verify VerifyCmd `cmd:"" help:"Check installed files against the lockfile."`
	Sync   SyncCmd   `cmd:"" help:"Install exactly what the lockfile records, without resolving versions."`
}

// Globals are flags shared by every command.
type Globals struct {
	Config  string           `help:"Configuration file." type:"path" default:"${config_file}" env:"ASKA_CONFIG"`
	Verbose bool             `short:"v" help:"Log diagnostic details, including every git command."`
	NoColor bool             `help:"Disable colored output."`
	Version kong.VersionFlag `help:"Print the version and exit."`
}

// ScopeFlags select the scope a command operates on.
type ScopeFlags struct {
	Global bool `short:"g" help:"Operate on the user's skills instead of the current repository."`
}

// Execute parses args and runs the selected command in env.
func Execute(ctx context.Context, args []string, env Environment) error {
	var grammar CLI
	parser, err := kong.New(&grammar,
		kong.Name("aska"),
		kong.Description(description),
		kong.UsageOnError(),
		kong.ConfigureHelp(kong.HelpOptions{Compact: true}),
		kong.Vars{
			"config_file":  env.Paths.ConfigFile,
			"version":      Version,
			"agent_help":   agentHelp,
			"package_help": packageHelp,
		},
		kong.Writers(env.Stdout, env.Stderr),
		kong.Exit(env.Exit),
	)
	if err != nil {
		return err
	}

	kctx, err := parser.Parse(args)
	if err != nil {
		return err
	}

	app, err := newApp(grammar.Globals, env)
	if err != nil {
		return err
	}

	kctx.BindTo(ctx, (*context.Context)(nil))
	return kctx.Run(app)
}
