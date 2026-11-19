package cli_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pkgshed/aska/internal/testutil/gitfixture"
)

func installedManifest(w *world) string {
	return filepath.Join(w.project, ".agents", "skills", "code-review", "SKILL.md")
}

func TestAddReviewsNewSkillsByDefault(t *testing.T) {
	// Arrange
	w := newWorld(t)
	w.upstream.WriteFile("skills/code-review/scripts/check.sh", "#!/bin/sh\necho checking\n", 0o755)
	w.upstream.Commit("add script")

	// Act: accept the default selection, then confirm.
	result := w.run("\ny\n", true, "add", "-a", "codex", pkg+"@main")

	// Assert
	require.NoError(t, result.err, result.stderr)
	assert.Contains(t, result.stdout, "1 skill(s) to review")
	assert.Regexp(t, `1\s+code-review\s+new\s+branch main \([0-9a-f]{12}\)\s+2 file\(s\), 1 executable`, result.stdout)
	assert.Contains(t, result.stdout, "Skills to inspect?")
	assert.Contains(t, result.stdout, ">>> SKILL.md")
	assert.Contains(t, result.stdout, ">>> scripts/check.sh (executable)")
	assert.Contains(t, result.stdout, "    2 | echo checking")
	assert.Less(t, strings.Index(result.stdout, ">>> SKILL.md"), strings.Index(result.stdout, ">>> scripts/check.sh"),
		"SKILL.md is shown first")
	assert.Contains(t, result.stdout, "installs into "+filepath.Join(".agents", "skills", "code-review"))
	assert.Contains(t, result.stdout, "Proceed with installation? [y/N]")
	assert.FileExists(t, installedManifest(w))
}

func TestAddInstallsNothingWhenDeclined(t *testing.T) {
	tests := map[string]string{
		"declined after inspection": "a\nn\n",
		"aborted from the menu":     "ab\n",
		"end of input":              "",
	}

	for name, answers := range tests {
		t.Run(name, func(t *testing.T) {
			// Arrange
			w := newWorld(t)

			// Act
			result := w.run(answers, true, "add", "-a", "codex", pkg)

			// Assert
			require.NoError(t, result.err, result.stderr)
			assert.Contains(t, result.stdout, "No changes were made.")
			assert.NoFileExists(t, installedManifest(w))
			assert.NoFileExists(t, filepath.Join(w.project, ".aska", "skills.toml"))
		})
	}
}

func TestAddRequiresApprovalWithoutTerminal(t *testing.T) {
	// Arrange
	w := newWorld(t)

	// Act
	result := w.run("", false, "add", "-a", "codex", pkg)

	// Assert
	require.Error(t, result.err)
	assert.Contains(t, result.err.Error(), "approval required")
	assert.Contains(t, result.stdout, "1 skill(s) to review", "the summary is shown before refusing")
	assert.NoFileExists(t, installedManifest(w))
}

func TestReviewMenuSelectsChangesAndRepromptsOnInvalidInput(t *testing.T) {
	// Arrange: two new skills from the same repository.
	w := newWorld(t)
	w.upstream.WriteFile("skills/writer/SKILL.md", "---\nname: writer\ndescription: Writes prose.\n---\nWrite well.\n", 0o644)
	w.upstream.Commit("writer")

	// Act: an out-of-range answer, then pick only the second skill.
	result := w.run("7\n2\ny\n", true, "add", "-a", "codex", pkg+"@main", gitfixture.Host+"/skills.git/skills/writer@main")

	// Assert
	require.NoError(t, result.err, result.stderr)
	assert.Contains(t, result.stdout, `invalid selection "7"`)
	assert.Contains(t, result.stdout, "Write well.")
	assert.NotContains(t, result.stdout, ">>> SKILL.md\n    1 | ---\n    2 | name: code-review")
	assert.FileExists(t, filepath.Join(w.project, ".agents", "skills", "writer", "SKILL.md"))
	assert.FileExists(t, installedManifest(w))
}

func TestUpdateReviewShowsFullPatch(t *testing.T) {
	// Arrange
	w := newWorld(t)
	require.NoError(t, w.run("", false, "add", "--yes", "-a", "codex", pkg+"@^1.0").err)
	w.release("v1.1.0", "Version 1.1")

	// Act
	result := w.run("1\ny\n", true, "update")

	// Assert
	require.NoError(t, result.err, result.stderr)
	assert.Regexp(t, `code-review\s+update\s+v1\.0\.0 \([0-9a-f]{12}\) → v1\.1\.0`, result.stdout)
	assert.Contains(t, result.stdout, "1 file changed")
	assert.Contains(t, result.stdout, "-Version 1.0")
	assert.Contains(t, result.stdout, "+Version 1.1")
	assert.Contains(t, result.stdout, "Proceed with the update? [y/N]")
	assert.Contains(t, result.stdout, "Updated code-review to v1.1.0")
}

func TestNoReviewSkipsInspectionButStillConfirms(t *testing.T) {
	// Arrange
	w := newWorld(t)

	// Act
	result := w.run("y\n", true, "add", "--no-review", "-a", "codex", pkg)

	// Assert
	require.NoError(t, result.err, result.stderr)
	assert.NotContains(t, result.stdout, "Skills to inspect?")
	assert.NotContains(t, result.stdout, ">>> SKILL.md")
	assert.Contains(t, result.stdout, "Proceed with installation?")
	assert.FileExists(t, installedManifest(w))
}

func TestConfigurationCanDisableReview(t *testing.T) {
	// Arrange
	w := newWorld(t)
	require.NoError(t, os.MkdirAll(filepath.Dir(w.paths.ConfigFile), 0o755))
	require.NoError(t, os.WriteFile(w.paths.ConfigFile, []byte("[review]\nenabled = false\n"), 0o644))

	// Act
	disabled := w.run("y\n", true, "add", "-a", "codex", pkg)
	forced := w.run("\nn\n", true, "add", "--review", "--global", "-a", "claude", pkg)

	// Assert
	require.NoError(t, disabled.err, disabled.stderr)
	assert.NotContains(t, disabled.stdout, "Skills to inspect?")
	require.NoError(t, forced.err, forced.stderr)
	assert.Contains(t, forced.stdout, "Skills to inspect?", "--review overrides the configuration")
}

func TestReviewNeutralizesEscapeSequences(t *testing.T) {
	// Arrange
	w := newWorld(t)
	w.upstream.WriteFile("skills/code-review/SKILL.md",
		"---\nname: code-review\ndescription: Reviews code.\n---\nHidden\x1b[8m text\x1b]0;pwned\x07\n", 0o644)
	w.upstream.Commit("hostile")

	// Act
	result := w.run("\nn\n", true, "add", "-a", "codex", pkg+"@main")

	// Assert
	require.NoError(t, result.err, result.stderr)
	assert.NotContains(t, result.stdout, "\x1b[8m")
	assert.NotContains(t, result.stdout, "\x1b]0;")
	assert.Contains(t, result.stdout, `Hidden\x1b[8m text\x1b]0;pwned\x07`)
}

func TestPagerPrecedence(t *testing.T) {
	tests := []struct {
		name       string
		askaPager  string
		configured string
		pager      string
		want       string
	}{
		{name: "ASKA_PAGER wins", askaPager: "sed 's/^/ASKA: /'", configured: "sed 's/^/CONFIG: /'", pager: "cat", want: "ASKA: "},
		{name: "configuration beats PAGER", configured: "sed 's/^/CONFIG: /'", pager: "sed 's/^/PAGER: /'", want: "CONFIG: "},
		{name: "PAGER as fallback", pager: "sed 's/^/PAGER: /'", want: "PAGER: "},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			// Arrange
			w := newWorld(t)
			w.pager = test.pager
			w.askaPager = test.askaPager
			if test.configured != "" {
				require.NoError(t, os.MkdirAll(filepath.Dir(w.paths.ConfigFile), 0o755))
				config := "[review]\nenabled = true\npager = \"" + test.configured + "\"\n"
				require.NoError(t, os.WriteFile(w.paths.ConfigFile, []byte(config), 0o644))
			}

			// Act
			result := w.run("\nn\n", true, "add", "-a", "codex", pkg)

			// Assert
			require.NoError(t, result.err, result.stderr)
			assert.Contains(t, result.stdout, test.want+">>> SKILL.md")
		})
	}
}
