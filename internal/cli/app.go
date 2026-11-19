package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/lmittmann/tint"

	"github.com/pkgshed/aska/internal/agent"
	"github.com/pkgshed/aska/internal/cache"
	"github.com/pkgshed/aska/internal/config"
	"github.com/pkgshed/aska/internal/discovery"
	"github.com/pkgshed/aska/internal/extract"
	"github.com/pkgshed/aska/internal/install"
	"github.com/pkgshed/aska/internal/netguard"
	"github.com/pkgshed/aska/internal/paths"
	"github.com/pkgshed/aska/internal/resolve"
	"github.com/pkgshed/aska/internal/source"
	"github.com/pkgshed/aska/internal/state"
	"github.com/pkgshed/aska/internal/vcs"
)

// discoveryTimeout bounds every discovery request.
const discoveryTimeout = 30 * time.Second

// stagingRetention is how long an unfinished snapshot build is kept before
// it counts as abandoned.
const stagingRetention = time.Hour

// Environment is everything aska takes from the process it runs in. Tests
// substitute their own values.
type Environment struct {
	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer
	// Interactive reports whether stdin and stdout are terminals.
	Interactive bool
	// Color reports whether stdout and stderr support color.
	Color bool
	// WorkDir is the current working directory.
	WorkDir string
	// Home is the user's home directory.
	Home string
	// ClaudeConfigDir is the value of CLAUDE_CONFIG_DIR.
	ClaudeConfigDir string
	// AskaPager is the value of ASKA_PAGER, which takes precedence over the
	// configured pager.
	AskaPager string
	// Pager is the value of PAGER, the fallback for the configured pager.
	Pager string
	// Paths locates aska's own files.
	Paths paths.Paths
	// GitEnv holds extra environment entries for git.
	GitEnv []string
	// HTTPClient replaces the discovery client when set.
	HTTPClient *http.Client
	// Now returns the current time.
	Now func() time.Time
	// Exit terminates the process after help or version output.
	Exit func(int)
}

// SystemEnvironment describes the running process.
func SystemEnvironment() (Environment, error) {
	workDir, err := os.Getwd()
	if err != nil {
		return Environment{}, fmt.Errorf("get working directory: %w", err)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return Environment{}, fmt.Errorf("get home directory: %w", err)
	}

	_, noColor := os.LookupEnv("NO_COLOR")
	return Environment{
		Stdin:           os.Stdin,
		Stdout:          os.Stdout,
		Stderr:          os.Stderr,
		Interactive:     isTerminal(os.Stdin) && isTerminal(os.Stdout),
		Color:           !noColor && isTerminal(os.Stdout),
		WorkDir:         workDir,
		Home:            home,
		ClaudeConfigDir: os.Getenv("CLAUDE_CONFIG_DIR"),
		AskaPager:       os.Getenv("ASKA_PAGER"),
		Pager:           os.Getenv("PAGER"),
		Paths:           paths.Default(),
		Now:             time.Now,
		Exit:            os.Exit,
	}, nil
}

func isTerminal(file *os.File) bool {
	info, err := file.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}

// App holds the services shared by all commands.
type App struct {
	env       Environment
	config    config.Config
	engine    *install.Engine
	discovery *discovery.Resolver
	agents    agent.Registry
	out       *printer
	logger    *slog.Logger
	// input reads answers to prompts. A single buffered reader serves every
	// prompt so that no answer is lost to another reader's buffer.
	input *bufio.Reader
	// pager is the shell command that pages review material, or "" for
	// the default.
	pager string
}

func newApp(globals Globals, env Environment) (*App, error) {
	cfg, err := config.Load(globals.Config)
	if err != nil {
		return nil, err
	}

	color := env.Color && !globals.NoColor
	logger := newLogger(env.Stderr, logLevel(cfg, globals), color)

	agents := agent.NewRegistry(agentDirs(cfg, env))
	policy := source.Policy{AllowInsecure: cfg.Security.AllowInsecure}

	cacheStore := &cache.Store{Root: env.Paths.Packages()}
	if err := cacheStore.PruneStaging(env.Now().Add(-stagingRetention)); err != nil {
		logger.Warn("prune snapshot staging", slog.Any("error", err))
	}

	engine := &install.Engine{
		Repos: &vcs.Store{
			Root: env.Paths.Repos(),
			Git: &vcs.Git{
				Path:    cfg.Git.Command,
				Env:     env.GitEnv,
				Logger:  logger,
				Timeout: time.Duration(cfg.Git.Timeout),
			},
		},
		Cache:  cacheStore,
		Agents: agents,
		Limits: limits(cfg),
		Policy: policy,
		Logger: logger,
	}

	return &App{
		env:       env,
		config:    cfg,
		engine:    engine,
		discovery: &discovery.Resolver{Client: discoveryClient(cfg, env), Policy: policy},
		agents:    agents,
		out:       &printer{w: env.Stdout, color: color, home: env.Home},
		logger:    logger,
		input:     bufio.NewReader(env.Stdin),
		pager:     pagerCommand(env, cfg.Review.Pager),
	}, nil
}

func newLogger(w io.Writer, level slog.Level, color bool) *slog.Logger {
	dropTime := func(groups []string, attr slog.Attr) slog.Attr {
		if len(groups) == 0 && attr.Key == slog.TimeKey {
			return slog.Attr{}
		}
		return attr
	}
	return slog.New(tint.NewTextHandler(w, &tint.Options{Level: level, NoColor: !color, ReplaceAttr: dropTime}))
}

func logLevel(cfg config.Config, globals Globals) slog.Level {
	if globals.Verbose {
		return slog.LevelDebug
	}

	var level slog.Level
	if err := level.UnmarshalText([]byte(cfg.Log.Level)); err != nil {
		return slog.LevelInfo
	}
	return level
}

func limits(cfg config.Config) extract.Limits {
	result := extract.DefaultLimits()
	result.MaxFiles = cfg.Limits.MaxFiles
	result.MaxFileBytes = cfg.Limits.MaxFileSize
	result.MaxTotalBytes = cfg.Limits.MaxTotalSize
	return result
}

func discoveryClient(cfg config.Config, env Environment) *http.Client {
	policy := netguard.Policy{
		AllowPrivateNetworks: cfg.Security.AllowPrivateNetworks,
		AllowInsecure:        cfg.Security.AllowInsecure,
	}
	if env.HTTPClient != nil {
		return netguard.Harden(env.HTTPClient, policy, discoveryTimeout)
	}
	return netguard.NewClient(policy, discoveryTimeout)
}

// agentDirs applies the configured overrides to the default directories.
func agentDirs(cfg config.Config, env Environment) map[agent.Name]agent.Dirs {
	dirs := agent.DefaultDirs(agent.Environment{Home: env.Home, ClaudeConfigDir: env.ClaudeConfigDir})
	overrides := map[agent.Name]config.AgentDirs{
		agent.Codex:    cfg.Agents.Codex,
		agent.Claude:   cfg.Agents.Claude,
		agent.OpenCode: cfg.Agents.OpenCode,
		agent.Pi:       cfg.Agents.Pi,
	}

	for name, override := range overrides {
		current := dirs[name]
		if override.ProjectDir != "" {
			current.Project = override.ProjectDir
		}
		if override.UserDir != "" {
			current.User = expandHome(override.UserDir, env.Home)
		}
		dirs[name] = current
	}
	return dirs
}

func expandHome(path, home string) string {
	if rest, found := strings.CutPrefix(path, "~/"); found {
		return filepath.Join(home, rest)
	}
	return path
}

// scope selects the user scope or the repository around the working
// directory.
func (a *App) scope(flags ScopeFlags) (state.Scope, error) {
	if flags.Global {
		return state.NewUserScope(a.env.Paths.StateDir), nil
	}

	root, err := state.FindProjectRoot(a.env.WorkDir)
	if errors.Is(err, state.ErrNotInRepository) {
		return state.Scope{}, fmt.Errorf("%w; use --global to manage user-wide skills", err)
	} else if err != nil {
		return state.Scope{}, err
	}
	return state.NewProjectScope(root, a.env.Paths.StateDir), nil
}

// request turns a command line reference into an installation request.
func (a *App) request(ctx context.Context, text string) (install.Request, error) {
	policy := source.Policy{AllowInsecure: a.config.Security.AllowInsecure}
	reference, err := source.ParseReference(text, policy)
	if err != nil {
		return install.Request{}, err
	}

	query, err := resolve.ParseQuery(reference.Query)
	if err != nil {
		return install.Request{}, err
	}

	pkg := reference.Package
	if !reference.IsResolved() {
		if pkg, err = a.discovery.Resolve(ctx, reference.ImportPath); err != nil {
			return install.Request{}, err
		}
	}

	return install.Request{Package: pkg, Query: query, Unversioned: reference.Query == ""}, nil
}

// selectedAgents parses agent names given on the command line.
func (a *App) selectedAgents(texts []string) ([]agent.Name, error) {
	return a.agents.ParseNames(texts)
}

// defaultAgents returns the configured default agents.
func (a *App) defaultAgents() ([]agent.Name, error) {
	return a.agents.ParseNames(a.config.DefaultAgents)
}
