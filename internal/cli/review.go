package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"
	"unicode"

	"github.com/pkgshed/aska/internal/install"
	"github.com/pkgshed/aska/internal/state"
)

// errApprovalRequired reports changes that need confirmation when no
// terminal is available to ask.
var errApprovalRequired = errors.New("approval required")

// errInvalidSelection reports an answer to the review menu that names no
// skills.
var errInvalidSelection = errors.New("invalid selection")

// ReviewFlags control the review of new and changed skills.
type ReviewFlags struct {
	Review   bool `xor:"review" help:"Inspect new and changed skills before installing them (the default unless configured otherwise)."`
	NoReview bool `xor:"review" help:"Skip the inspection menu but still ask for confirmation."`
	Yes      bool `short:"y" help:"Install without asking, which also skips the inspection."`
}

// terminalReviewer implements [install.Reviewer] in the style of yay and
// paru: it lists the changes, lets the user pick which ones to inspect in a
// pager and asks for confirmation before anything is installed.
type terminalReviewer struct {
	app       *App
	scope     state.Scope
	inspect   bool
	assumeYes bool
	// action names the operation in the confirmation prompt.
	action string
}

// reviewer builds the reviewer for a command from its flags and the
// configuration.
func (a *App) reviewer(scope state.Scope, flags ReviewFlags, action string) *terminalReviewer {
	inspect := a.config.Review.Enabled
	switch {
	case flags.Review:
		inspect = true
	case flags.NoReview:
		inspect = false
	}
	return &terminalReviewer{app: a, scope: scope, inspect: inspect, assumeYes: flags.Yes, action: action}
}

// Review implements [install.Reviewer].
func (r *terminalReviewer) Review(ctx context.Context, changes []install.Change) (bool, error) {
	r.printSummary(changes)

	switch {
	case r.assumeYes:
		return true, nil
	case !r.app.env.Interactive:
		return false, fmt.Errorf("%w: review the skills in a terminal or rerun with --yes", errApprovalRequired)
	}

	if r.inspect {
		proceed, err := r.inspectChanges(ctx, changes)
		if err != nil || !proceed {
			return false, err
		}
	}
	return confirm(r.app.input, r.app.out, fmt.Sprintf("Proceed with %s?", r.action))
}

// inspectChanges asks which changes to inspect and pages them. It reports
// false when the user aborts.
func (r *terminalReviewer) inspectChanges(ctx context.Context, changes []install.Change) (bool, error) {
	selected, abort, err := r.askSelection(changes)
	if err != nil || abort {
		return false, err
	}
	if len(selected) == 0 {
		return true, nil
	}

	document, err := r.app.reviewDocument(ctx, r.scope, selected)
	if err != nil {
		return false, err
	}
	return true, r.app.page(ctx, document)
}

func (r *terminalReviewer) printSummary(changes []install.Change) {
	out := r.app.out
	out.heading("%d skill(s) to review", len(changes))

	rows := make([][]string, 0, len(changes))
	for index, change := range changes {
		rows = append(rows, []string{
			"   " + strconv.Itoa(index+1),
			out.style(styleBold, change.Name),
			changeKind(change),
			changeVersions(change),
			r.changeDetails(change),
		})
	}
	_ = out.table(nil, rows)
}

func changeKind(change install.Change) string {
	if change.IsNew() {
		return "new"
	}
	return "update"
}

func changeVersions(change install.Change) string {
	next := fmt.Sprintf("%s (%s)", resolvedLabel(change.Next), change.Next.Commit.Short())
	if change.IsNew() {
		return next
	}
	return fmt.Sprintf("%s (%s) → %s", resolvedLabel(change.Current), change.Current.Commit.Short(), next)
}

// changeDetails summarizes the size of a change, drawing attention to
// executable files in new skills.
func (r *terminalReviewer) changeDetails(change install.Change) string {
	if !change.IsNew() {
		return sanitize(lastLine(change.Diffstat))
	}

	files, err := change.Files()
	if err != nil {
		return "files unavailable"
	}
	executables := 0
	for _, file := range files {
		if file.Executable {
			executables++
		}
	}

	details := fmt.Sprintf("%d file(s)", len(files))
	if executables > 0 {
		details += ", " + r.app.out.style(styleYellow, fmt.Sprintf("%d executable", executables))
	}
	return details
}

func lastLine(text string) string {
	lines := strings.Split(strings.TrimSpace(text), "\n")
	return strings.TrimSpace(lines[len(lines)-1])
}

// askSelection shows the review menu until the answer is valid.
func (r *terminalReviewer) askSelection(changes []install.Change) ([]install.Change, bool, error) {
	out := r.app.out
	for {
		out.heading("Skills to inspect? [A]ll [N]one [Ab]ort or numbers such as 1 3-4 (default: all)")
		fmt.Fprint(out.w, out.style(styleBold+styleBlue, "==>")+" ")

		answer, err := readAnswer(r.app.input)
		if err != nil {
			return nil, false, err
		}

		indices, abort, err := parseSelection(answer, len(changes))
		if errors.Is(err, errInvalidSelection) {
			out.line("%v", err)
			continue
		}

		selected := make([]install.Change, 0, len(indices))
		for _, index := range indices {
			selected = append(selected, changes[index])
		}
		return selected, abort, nil
	}
}

// parseSelection interprets an answer to the review menu: "" and "a" select
// everything, "n" nothing, "ab" aborts, and numbers or ranges such as
// "1 3-4" select individual changes. Indices are returned zero-based.
func parseSelection(answer string, count int) ([]int, bool, error) {
	answer = strings.TrimSpace(answer)
	switch strings.ToLower(answer) {
	case "", "a", "all":
		return allIndices(count), false, nil
	case "n", "none":
		return nil, false, nil
	case "ab", "abort":
		return nil, true, nil
	}

	var indices []int
	fields := strings.FieldsFunc(answer, func(r rune) bool { return r == ',' || unicode.IsSpace(r) })
	for _, field := range fields {
		first, last, err := parseRange(field, count)
		if err != nil {
			return nil, false, err
		}
		for index := first; index <= last; index++ {
			if !slices.Contains(indices, index) {
				indices = append(indices, index)
			}
		}
	}
	return indices, false, nil
}

func parseRange(field string, count int) (int, int, error) {
	low, high, isRange := strings.Cut(field, "-")
	if !isRange {
		high = low
	}

	first, firstErr := strconv.Atoi(low)
	last, lastErr := strconv.Atoi(high)
	switch {
	case firstErr != nil || lastErr != nil:
		return 0, 0, fmt.Errorf("%w %q: use numbers between 1 and %d", errInvalidSelection, field, count)
	case first < 1 || last > count || first > last:
		return 0, 0, fmt.Errorf("%w %q: use numbers between 1 and %d", errInvalidSelection, field, count)
	default:
		return first - 1, last - 1, nil
	}
}

func allIndices(count int) []int {
	indices := make([]int, count)
	for index := range indices {
		indices[index] = index
	}
	return indices
}

// confirm asks a yes-or-no question and defaults to no.
func confirm(input *bufio.Reader, out *printer, question string) (bool, error) {
	fmt.Fprint(out.w, out.style(styleBold+styleBlue, "::")+" "+out.style(styleBold, question+" [y/N] "))

	answer, err := readAnswer(input)
	if err != nil {
		return false, err
	}

	switch strings.ToLower(strings.TrimSpace(answer)) {
	case "y", "yes":
		return true, nil
	default:
		return false, nil
	}
}

// readAnswer reads one line. The end of input counts as an empty answer so
// that every prompt falls back to its safe default.
func readAnswer(input *bufio.Reader) (string, error) {
	answer, err := input.ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", fmt.Errorf("read answer: %w", err)
	}
	return answer, nil
}
