// Package agent describes where coding agents discover skills and which
// SKILL.md rules each of them enforces.
//
// Agents differ in their skill directories, in how strictly they read the
// frontmatter and in how they rank skills of the same name. An [Adapter]
// captures those differences so that the installer can stay agnostic.
// Several agents may share a directory; the installer writes each directory
// only once.
package agent

import (
	"errors"
	"fmt"
	"maps"
	"path/filepath"
	"slices"
	"strings"

	"github.com/pkgshed/aska/internal/skill"
)

// ErrUnknownAgent reports an agent name without an adapter.
var ErrUnknownAgent = errors.New("unknown agent")

// ErrIncompatibleSkill reports a skill that an agent would refuse to load.
var ErrIncompatibleSkill = errors.New("skill is incompatible with agent")

// Name identifies an agent.
type Name string

// Supported agents.
const (
	Codex    Name = "codex"
	Claude   Name = "claude"
	OpenCode Name = "opencode"
	Pi       Name = "pi"
)

// Scope selects between skills of one repository and skills of the user.
type Scope int

const (
	// ProjectScope installs into the current git repository.
	ProjectScope Scope = iota
	// UserScope installs into the home directory.
	UserScope
)

// Location anchors the relative skill directories of an adapter.
type Location struct {
	Scope Scope
	// ProjectRoot is the root of the git repository for [ProjectScope].
	ProjectRoot string
}

// Adapter describes one agent.
type Adapter interface {
	// Name returns the agent name.
	Name() Name
	// SkillsDir returns the absolute directory the agent scans for skills.
	SkillsDir(location Location) string
	// Validate reports whether the agent would load a skill installed
	// under installName with the given metadata.
	Validate(installName string, metadata skill.Metadata) error
}

// Dirs locates the skill directories of an agent.
type Dirs struct {
	// Project is relative to the repository root.
	Project string
	// User is absolute.
	User string
}

// Registry maps agent names to adapters.
type Registry map[Name]Adapter

// Lookup returns the adapter for a name.
func (r Registry) Lookup(name Name) (Adapter, error) {
	adapter, found := r[name]
	if !found {
		return nil, fmt.Errorf("%w %q; known agents: %s", ErrUnknownAgent, name, strings.Join(r.names(), ", "))
	}
	return adapter, nil
}

// ParseNames converts and validates agent names, removing duplicates while
// keeping the original order.
func (r Registry) ParseNames(texts []string) ([]Name, error) {
	var names []Name
	for _, text := range texts {
		name := Name(strings.ToLower(strings.TrimSpace(text)))
		if _, err := r.Lookup(name); err != nil {
			return nil, err
		}
		if !slices.Contains(names, name) {
			names = append(names, name)
		}
	}
	return names, nil
}

func (r Registry) names() []string {
	var names []string
	for _, name := range slices.Sorted(maps.Keys(r)) {
		names = append(names, string(name))
	}
	return names
}

// basicAdapter implements [Adapter] from directories and a validation rule.
type basicAdapter struct {
	name     Name
	dirs     Dirs
	validate func(installName string, metadata skill.Metadata) error
}

func (a basicAdapter) Name() Name { return a.name }

func (a basicAdapter) SkillsDir(location Location) string {
	if location.Scope == UserScope {
		return filepath.Clean(a.dirs.User)
	}
	return filepath.Join(location.ProjectRoot, filepath.FromSlash(a.dirs.Project))
}

func (a basicAdapter) Validate(installName string, metadata skill.Metadata) error {
	if err := a.validate(installName, metadata); err != nil {
		return fmt.Errorf("%w %s: %w", ErrIncompatibleSkill, a.name, err)
	}
	return nil
}
