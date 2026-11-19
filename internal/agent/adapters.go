package agent

import (
	"errors"
	"fmt"
	"path/filepath"
	"unicode/utf8"

	"github.com/pkgshed/aska/internal/skill"
)

// Environment is the part of the process environment that decides where
// agents look for user skills. It is captured once at start-up so that
// adapters behave deterministically.
type Environment struct {
	// Home is the user's home directory.
	Home string
	// ClaudeConfigDir is the value of CLAUDE_CONFIG_DIR, which relocates
	// Claude Code's user directory when set.
	ClaudeConfigDir string
}

// DefaultDirs returns the skill directories each agent scans. Codex,
// OpenCode and pi all read the vendor-neutral .agents/skills directories;
// Claude Code reads .claude/skills.
func DefaultDirs(env Environment) map[Name]Dirs {
	agentsDirs := Dirs{
		Project: ".agents/skills",
		User:    filepath.Join(env.Home, ".agents", "skills"),
	}

	claudeHome := filepath.Join(env.Home, ".claude")
	if env.ClaudeConfigDir != "" {
		claudeHome = env.ClaudeConfigDir
	}

	return map[Name]Dirs{
		Codex:    agentsDirs,
		OpenCode: agentsDirs,
		Pi:       agentsDirs,
		Claude: {
			Project: ".claude/skills",
			User:    filepath.Join(claudeHome, "skills"),
		},
	}
}

// NewRegistry builds the adapters of all supported agents with the given
// directories. Agents missing from dirs are not registered.
func NewRegistry(dirs map[Name]Dirs) Registry {
	rules := map[Name]func(string, skill.Metadata) error{
		Codex:    validateCodex,
		Claude:   validateClaude,
		OpenCode: validateOpenCode,
		Pi:       validatePi,
	}

	registry := Registry{}
	for name, validate := range rules {
		if agentDirs, configured := dirs[name]; configured {
			registry[name] = basicAdapter{name: name, dirs: agentDirs, validate: validate}
		}
	}
	return registry
}

// validateCodex follows https://developers.openai.com/codex/skills: name
// and description are required. Codex ranks repository skills above user
// skills of the same name.
func validateCodex(installName string, metadata skill.Metadata) error {
	if err := requireName(installName, metadata); err != nil {
		return err
	}
	return requireDescription(metadata)
}

// validateClaude follows https://code.claude.com/docs/en/skills: every
// field is optional, and a missing name falls back to the directory name.
// Claude Code ranks personal skills above project skills of the same name.
func validateClaude(_ string, metadata skill.Metadata) error {
	if metadata.Name != "" {
		if err := skill.ValidateName(metadata.Name); err != nil {
			return err
		}
	}
	return checkDescriptionLength(metadata)
}

// validateOpenCode follows https://opencode.ai/docs/skills: name and
// description are required, and the name must match the directory name.
// Skills that break these rules are skipped silently, so they are rejected
// at install time instead.
func validateOpenCode(installName string, metadata skill.Metadata) error {
	if err := requireName(installName, metadata); err != nil {
		return err
	}
	return requireDescription(metadata)
}

// validatePi follows the pi coding agent's skill documentation: it warns
// about naming violations but still loads the skill, and only skips skills
// without a description.
func validatePi(_ string, metadata skill.Metadata) error {
	return requireDescription(metadata)
}

func requireName(installName string, metadata skill.Metadata) error {
	if metadata.Name == "" {
		return errors.New("frontmatter has no name")
	}
	if err := skill.ValidateName(metadata.Name); err != nil {
		return err
	}
	if metadata.Name != installName {
		return fmt.Errorf("frontmatter name %q differs from directory name %q", metadata.Name, installName)
	}
	return nil
}

func requireDescription(metadata skill.Metadata) error {
	if metadata.Description == "" {
		return errors.New("frontmatter has no description")
	}
	return checkDescriptionLength(metadata)
}

func checkDescriptionLength(metadata skill.Metadata) error {
	if utf8.RuneCountInString(metadata.Description) > skill.MaxDescriptionLength {
		return fmt.Errorf("description is longer than %d characters", skill.MaxDescriptionLength)
	}
	return nil
}
