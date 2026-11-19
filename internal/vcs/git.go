package vcs

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// maxStderrBytes bounds how much git diagnostic output an error carries.
const maxStderrBytes = 4 << 10

// Git runs git commands with a hardened, non-interactive configuration.
type Git struct {
	// Path is the git executable to run.
	Path string
	// Env holds extra environment entries that override inherited ones.
	Env []string
	// Logger receives debug records for every command.
	Logger *slog.Logger
	// Timeout bounds every command; zero means no limit.
	Timeout time.Duration
}

// baseConfig is passed to every invocation. Automatic garbage collection and
// maintenance stay off so that objects aska relies on are never pruned
// behind its back, and fetches never recurse into submodules.
var baseConfig = []string{
	"-c", "gc.auto=0",
	"-c", "maintenance.auto=false",
	"-c", "fetch.recurseSubmodules=false",
	"-c", "core.fsmonitor=false",
}

// CommandError describes a git command that did not succeed.
type CommandError struct {
	Args   []string
	Stderr string
	Err    error
}

func (e *CommandError) Error() string {
	subcommand := "git"
	if len(e.Args) > 0 {
		subcommand = "git " + e.Args[0]
	}
	if e.Stderr == "" {
		return fmt.Sprintf("%s: %v", subcommand, e.Err)
	}
	return fmt.Sprintf("%s: %v: %s", subcommand, e.Err, e.Stderr)
}

func (e *CommandError) Unwrap() error { return e.Err }

// command builds a git invocation. When gitDir is empty the command runs
// outside any repository, with discovery stopped at workDir.
func (g *Git) command(ctx context.Context, gitDir, workDir string, args ...string) *exec.Cmd {
	full := append([]string{}, baseConfig...)
	if gitDir != "" {
		full = append(full, "--git-dir="+gitDir)
	}
	full = append(full, args...)

	cmd := exec.CommandContext(ctx, g.Path, full...)
	cmd.Dir = workDir
	cmd.Env = g.environ(workDir)
	cmd.Cancel = func() error { return cmd.Process.Signal(os.Interrupt) }
	cmd.WaitDelay = 5 * time.Second
	return cmd
}

// repositoryVariables locate a repository and its objects. They are removed
// from the inherited environment because a caller such as a git hook may
// set them for an unrelated repository, which would redirect aska's reads
// and writes.
var repositoryVariables = []string{
	"GIT_DIR",
	"GIT_WORK_TREE",
	"GIT_INDEX_FILE",
	"GIT_OBJECT_DIRECTORY",
	"GIT_ALTERNATE_OBJECT_DIRECTORIES",
	"GIT_COMMON_DIR",
	"GIT_NAMESPACE",
	"GIT_PREFIX",
}

// environ disables interactive prompts and stops repository discovery at
// workDir, so commands meant to run outside a repository never pick up an
// unrelated one from a parent directory.
func (g *Git) environ(workDir string) []string {
	env := slices.DeleteFunc(os.Environ(), func(entry string) bool {
		name, _, _ := strings.Cut(entry, "=")
		return slices.Contains(repositoryVariables, name)
	})
	env = append(env,
		"GIT_TERMINAL_PROMPT=0",
		"GIT_CEILING_DIRECTORIES="+filepath.Dir(workDir),
		"GIT_NO_REPLACE_OBJECTS=1",
		"GIT_LITERAL_PATHSPECS=1",
		"LC_ALL=C",
	)
	return append(env, g.Env...)
}

// withTimeout applies the configured command timeout to ctx.
func (g *Git) withTimeout(ctx context.Context) (context.Context, context.CancelFunc) {
	if g.Timeout <= 0 {
		return context.WithCancel(ctx)
	}
	return context.WithTimeout(ctx, g.Timeout)
}

// run executes a command and returns its standard output.
func (g *Git) run(ctx context.Context, gitDir, workDir string, args ...string) ([]byte, error) {
	ctx, cancel := g.withTimeout(ctx)
	defer cancel()

	cmd := g.command(ctx, gitDir, workDir, args...)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &limitedBuffer{buffer: &stderr, limit: maxStderrBytes}

	started := time.Now()
	err := cmd.Run()
	g.log(ctx, args, started, err)
	if err != nil {
		return nil, g.commandError(ctx, args, stderr.String(), err)
	}

	return stdout.Bytes(), nil
}

func (g *Git) commandError(ctx context.Context, args []string, stderr string, err error) error {
	if ctxErr := ctx.Err(); ctxErr != nil {
		err = ctxErr
	}
	return &CommandError{Args: args, Stderr: strings.TrimSpace(stderr), Err: err}
}

func (g *Git) log(ctx context.Context, args []string, started time.Time, err error) {
	if g.Logger == nil {
		return
	}
	g.Logger.DebugContext(ctx, "git",
		slog.String("args", strings.Join(args, " ")),
		slog.Duration("elapsed", time.Since(started)),
		slog.Any("error", err),
	)
}

// limitedBuffer keeps the first limit bytes written to it and discards the
// rest, so a chatty failing command cannot exhaust memory.
type limitedBuffer struct {
	buffer *bytes.Buffer
	limit  int
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	remaining := b.limit - b.buffer.Len()
	if remaining > 0 {
		b.buffer.Write(p[:min(len(p), remaining)])
	}
	return len(p), nil
}

var _ io.Writer = (*limitedBuffer)(nil)
