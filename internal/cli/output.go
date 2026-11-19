package cli

import (
	"bufio"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"unicode"

	"github.com/pkgshed/aska/internal/agent"
	"github.com/pkgshed/aska/internal/state"
)

// ANSI styles.
const (
	styleReset  = "\x1b[0m"
	styleBold   = "\x1b[1m"
	styleRed    = "\x1b[31m"
	styleGreen  = "\x1b[32m"
	styleYellow = "\x1b[33m"
	styleBlue   = "\x1b[34m"
	styleCyan   = "\x1b[36m"
)

// printer writes user-facing output to stdout. Diagnostics go to the
// logger on stderr instead.
type printer struct {
	w     io.Writer
	color bool
	home  string
}

func (p *printer) style(style, text string) string {
	if !p.color {
		return text
	}
	return style + text + styleReset
}

// heading prints a section marker in the style of pacman and yay.
func (p *printer) heading(format string, args ...any) {
	fmt.Fprintln(p.w, p.style(styleBold+styleBlue, "::")+" "+p.style(styleBold, fmt.Sprintf(format, args...)))
}

// success prints the outcome of an action.
func (p *printer) success(format string, args ...any) {
	fmt.Fprintln(p.w, p.style(styleBold+styleGreen, "==>")+" "+p.style(styleBold, fmt.Sprintf(format, args...)))
}

// detail prints an indented line below a heading.
func (p *printer) detail(format string, args ...any) {
	fmt.Fprintln(p.w, "    "+fmt.Sprintf(format, args...))
}

func (p *printer) line(format string, args ...any) {
	fmt.Fprintf(p.w, format+"\n", args...)
}

// table writes rows as aligned columns, preceded by a header unless it is
// nil.
func (p *printer) table(header []string, rows [][]string) error {
	writer := tabwriter.NewWriter(p.w, 0, 0, 2, ' ', 0)
	if header != nil {
		fmt.Fprintln(writer, strings.Join(header, "\t"))
	}
	for _, row := range rows {
		fmt.Fprintln(writer, strings.Join(row, "\t"))
	}
	return writer.Flush()
}

// displayPath shortens paths below the home directory to "~/...".
func (p *printer) displayPath(path string) string {
	if p.home == "" {
		return path
	}
	relative, err := filepath.Rel(p.home, path)
	if err != nil || relative == "." || strings.HasPrefix(relative, "..") {
		return path
	}
	return filepath.Join("~", relative)
}

// status colors a verification status.
func (p *printer) status(text string) string {
	switch text {
	case "ok":
		return p.style(styleGreen, text)
	case "missing", "modified":
		return p.style(styleRed, text)
	default:
		return p.style(styleYellow, text)
	}
}

// patch prints a unified diff, coloring the lines it recognizes.
func (p *printer) patch(text string) {
	scanner := bufio.NewScanner(strings.NewReader(text))
	scanner.Buffer(make([]byte, 0, 64<<10), 4<<20)
	for scanner.Scan() {
		line := sanitize(scanner.Text())
		switch {
		case strings.HasPrefix(line, "+++"), strings.HasPrefix(line, "---"), strings.HasPrefix(line, "diff "):
			line = p.style(styleBold, line)
		case strings.HasPrefix(line, "+"):
			line = p.style(styleGreen, line)
		case strings.HasPrefix(line, "-"):
			line = p.style(styleRed, line)
		case strings.HasPrefix(line, "@@"):
			line = p.style(styleCyan, line)
		}
		fmt.Fprintln(p.w, line)
	}
}

// FormatError renders an error for the terminal. Error messages can carry
// text from remotes, such as git's relay of server messages, so control
// characters other than line breaks are neutralized.
func FormatError(err error) string {
	lines := strings.Split(err.Error(), "\n")
	for index, line := range lines {
		lines[index] = sanitize(line)
	}
	return strings.Join(lines, "\n")
}

// sanitize neutralizes control characters in untrusted text, such as skill
// descriptions and diffs, so that a remote repository cannot drive the
// terminal with escape sequences. Tabs survive; everything else is shown
// as an escape.
func sanitize(text string) string {
	if !strings.ContainsFunc(text, isUnsafeRune) {
		return text
	}

	var builder strings.Builder
	for _, r := range text {
		if isUnsafeRune(r) {
			fmt.Fprintf(&builder, "\\x%02x", r)
			continue
		}
		builder.WriteRune(r)
	}
	return builder.String()
}

func isUnsafeRune(r rune) bool {
	return r != '\t' && (unicode.IsControl(r) || r == '\u2028' || r == '\u2029' || r == unicode.ReplacementChar)
}

// joinAgents formats agent names for display.
func joinAgents(names []agent.Name) string {
	texts := make([]string, 0, len(names))
	for _, name := range names {
		texts = append(texts, string(name))
	}
	return strings.Join(texts, ",")
}

// resolvedLabel names the resolved version compactly.
func resolvedLabel(lock state.Lock) string {
	switch {
	case lock.Version.IsValid():
		return lock.Version.String()
	case lock.Tag != "":
		return "tag " + lock.Tag
	case lock.Branch != "":
		return "branch " + lock.Branch
	default:
		return "commit"
	}
}
