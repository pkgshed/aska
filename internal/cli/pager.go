package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"slices"
	"strings"
)

// defaultPager is used when neither ASKA_PAGER, review.pager nor PAGER
// names a pager. It is skipped when less is not installed.
const defaultPager = "less -R"

// pagerCommand picks the pager the way git does: ASKA_PAGER overrides the
// configuration, which overrides PAGER. An empty result selects the
// default.
func pagerCommand(env Environment, configured string) string {
	for _, candidate := range []string{env.AskaPager, configured, env.Pager} {
		if strings.TrimSpace(candidate) != "" {
			return candidate
		}
	}
	return ""
}

// page shows text through the pager. The pager is a shell command chosen by
// the user, so it runs through sh like git's pager does. The text has been
// sanitized already, so a pager that interprets escape sequences only sees
// aska's own colors.
func (a *App) page(ctx context.Context, text string) error {
	command := a.pager
	if command == "" {
		if _, err := exec.LookPath("less"); err != nil {
			return writeText(a.env.Stdout, text)
		}
		command = defaultPager
	}
	if command == "cat" {
		return writeText(a.env.Stdout, text)
	}

	cmd := exec.CommandContext(ctx, "sh", "-c", command)
	cmd.Stdin = strings.NewReader(text)
	cmd.Stdout = a.env.Stdout
	cmd.Stderr = a.env.Stderr
	cmd.Env = pagerEnviron(os.Environ())
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("run pager %q: %w", command, err)
	}
	return nil
}

// pagerEnviron sets the less options git uses unless the user chose their
// own: quit when the text fits on one screen, keep colors, leave the text
// on the screen.
func pagerEnviron(environ []string) []string {
	hasLess := slices.ContainsFunc(environ, func(entry string) bool { return strings.HasPrefix(entry, "LESS=") })
	if hasLess {
		return environ
	}
	return append(environ, "LESS=FRX")
}

func writeText(w io.Writer, text string) error {
	_, err := io.WriteString(w, text)
	return err
}
