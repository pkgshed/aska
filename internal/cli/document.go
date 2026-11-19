package cli

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/pkgshed/aska/internal/install"
	"github.com/pkgshed/aska/internal/skill"
	"github.com/pkgshed/aska/internal/state"
)

// maxReviewFileBytes bounds how much of a single file the review shows.
const maxReviewFileBytes = 256 << 10

// reviewDocument renders the material for reviewing changes: for a new
// skill every file in full, for an update the diff against the installed
// version. All text from the repository is sanitized.
func (a *App) reviewDocument(ctx context.Context, scope state.Scope, changes []install.Change) (string, error) {
	var buffer strings.Builder
	doc := &printer{w: &buffer, color: a.out.color, home: a.out.home}

	for index, change := range changes {
		if index > 0 {
			doc.line("")
		}
		writeChangeHeader(doc, change, func(dir string) string { return a.relativeToScope(scope, dir) })

		var err error
		if change.IsNew() {
			err = writeFiles(doc, change)
		} else {
			err = writePatch(ctx, doc, change)
		}
		if err != nil {
			return "", fmt.Errorf("review %s: %w", change.Name, err)
		}
	}
	return buffer.String(), nil
}

func writeChangeHeader(doc *printer, change install.Change, displayDir func(string) string) {
	doc.success("%s  %s", change.Name, change.Package)
	if change.IsNew() {
		doc.detail("new skill: %s, revision %s", change.Next.Describe(), change.Next.Commit)
	} else {
		doc.detail("update: %s (%s) → %s (%s)", change.Current.Describe(), change.Current.Commit.Short(),
			change.Next.Describe(), change.Next.Commit.Short())
	}
	doc.detail("digest sha256:%s", change.Next.Digest)
	doc.detail("description: %s", sanitize(change.Metadata.Description))
	for _, dir := range change.Dirs {
		doc.detail("installs into %s", displayDir(dir))
	}
	doc.line("")
}

// writePatch shows the diff of an update, falling back to the complete new
// version when the old commit cannot be compared.
func writePatch(ctx context.Context, doc *printer, change install.Change) error {
	patch, err := change.Patch(ctx)
	if err != nil {
		doc.line("%s", doc.style(styleYellow, sanitize("diff unavailable ("+err.Error()+"); showing the complete new version")))
		doc.line("")
		return writeFiles(doc, change)
	}

	if change.Diffstat != "" {
		for line := range strings.Lines(change.Diffstat) {
			doc.line("%s", sanitize(strings.TrimRight(line, "\n")))
		}
		doc.line("")
	}
	doc.patch(string(patch))
	return nil
}

// writeFiles lists every file of the new version and prints each one,
// SKILL.md first, with line numbers.
func writeFiles(doc *printer, change install.Change) error {
	files, err := change.Files()
	if err != nil {
		return err
	}
	slices.SortStableFunc(files, func(a, b install.File) int {
		return compareManifestFirst(a.Path, b.Path)
	})

	doc.line("%s", doc.style(styleBold, "Files:"))
	for _, file := range files {
		marker := ""
		if file.Executable {
			marker = "  " + doc.style(styleYellow, "executable")
		}
		doc.line("  %10s  %s%s", formatSize(file.Size), sanitize(file.Path), marker)
	}

	for _, file := range files {
		content, err := change.ReadFile(file.Path)
		if err != nil {
			return err
		}
		doc.line("")
		writeFile(doc, file, content)
	}
	return nil
}

func compareManifestFirst(a, b string) int {
	switch {
	case a == b:
		return 0
	case a == skill.ManifestName:
		return -1
	case b == skill.ManifestName:
		return 1
	default:
		return strings.Compare(a, b)
	}
}

func writeFile(doc *printer, file install.File, content []byte) {
	title := sanitize(file.Path)
	if file.Executable {
		title += " (executable)"
	}
	doc.line("%s", doc.style(styleBold+styleCyan, ">>> "+title))

	if isBinary(content) {
		doc.line("    [binary file, %s, not shown]", formatSize(file.Size))
		return
	}

	shown := content
	if len(shown) > maxReviewFileBytes {
		shown = truncateUTF8(shown, maxReviewFileBytes)
	}

	scanner := bufio.NewScanner(bytes.NewReader(shown))
	scanner.Buffer(make([]byte, 0, 64<<10), maxReviewFileBytes+1)
	number := 0
	for scanner.Scan() {
		number++
		doc.line("%s %s", doc.style(styleBlue, fmt.Sprintf("%5d |", number)), sanitize(scanner.Text()))
	}

	if len(shown) < len(content) {
		doc.line("    [truncated after %s of %s]", formatSize(int64(len(shown))), formatSize(file.Size))
	}
}

// isBinary treats content with NUL bytes or invalid UTF-8 as binary.
func isBinary(content []byte) bool {
	return bytes.IndexByte(content, 0) >= 0 || !utf8.Valid(content)
}

// truncateUTF8 cuts content to at most limit bytes without splitting a
// character.
func truncateUTF8(content []byte, limit int) []byte {
	cut := content[:limit]
	for len(cut) > 0 && !utf8.Valid(cut) {
		cut = cut[:len(cut)-1]
	}
	return cut
}

// formatSize renders a byte count with binary units.
func formatSize(size int64) string {
	const unit = 1024
	if size < unit {
		return fmt.Sprintf("%d B", size)
	}

	value := float64(size) / unit
	for _, suffix := range []string{"KiB", "MiB"} {
		if value < unit {
			return fmt.Sprintf("%.1f %s", value, suffix)
		}
		value /= unit
	}
	return fmt.Sprintf("%.1f GiB", value)
}
