package agent_test

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pkgshed/aska/internal/agent"
	"github.com/pkgshed/aska/internal/skill"
)

func registry() agent.Registry {
	return agent.NewRegistry(agent.DefaultDirs(agent.Environment{Home: "/home/user"}))
}

func TestSkillsDirectories(t *testing.T) {
	project := agent.Location{Scope: agent.ProjectScope, ProjectRoot: "/work/repo"}
	user := agent.Location{Scope: agent.UserScope}

	tests := []struct {
		name    agent.Name
		project string
		user    string
	}{
		{agent.Codex, "/work/repo/.agents/skills", "/home/user/.agents/skills"},
		{agent.OpenCode, "/work/repo/.agents/skills", "/home/user/.agents/skills"},
		{agent.Pi, "/work/repo/.agents/skills", "/home/user/.agents/skills"},
		{agent.Claude, "/work/repo/.claude/skills", "/home/user/.claude/skills"},
	}

	for _, test := range tests {
		t.Run(string(test.name), func(t *testing.T) {
			// Arrange
			adapter, err := registry().Lookup(test.name)
			require.NoError(t, err)

			// Act and assert
			assert.Equal(t, filepath.FromSlash(test.project), adapter.SkillsDir(project))
			assert.Equal(t, filepath.FromSlash(test.user), adapter.SkillsDir(user))
		})
	}
}

func TestClaudeConfigDirRelocatesUserSkills(t *testing.T) {
	// Arrange
	dirs := agent.DefaultDirs(agent.Environment{Home: "/home/user", ClaudeConfigDir: "/opt/claude"})
	adapter, err := agent.NewRegistry(dirs).Lookup(agent.Claude)
	require.NoError(t, err)

	// Act
	dir := adapter.SkillsDir(agent.Location{Scope: agent.UserScope})

	// Assert
	assert.Equal(t, filepath.FromSlash("/opt/claude/skills"), dir)
}

func TestParseNames(t *testing.T) {
	// Act
	names, err := registry().ParseNames([]string{"Claude", "codex", " claude "})
	_, unknownErr := registry().ParseNames([]string{"cursor"})

	// Assert
	require.NoError(t, err)
	assert.Equal(t, []agent.Name{agent.Claude, agent.Codex}, names)
	require.ErrorIs(t, unknownErr, agent.ErrUnknownAgent)
}

func TestValidateAppliesAgentSpecificRules(t *testing.T) {
	complete := skill.Metadata{Name: "review", Description: "Reviews code."}
	nameless := skill.Metadata{Description: "Reviews code."}
	silent := skill.Metadata{Name: "review"}
	verbose := skill.Metadata{Name: "review", Description: strings.Repeat("x", skill.MaxDescriptionLength+1)}

	tests := []struct {
		label    string
		metadata skill.Metadata
		accepted map[agent.Name]bool
	}{
		{"complete", complete, map[agent.Name]bool{agent.Codex: true, agent.Claude: true, agent.OpenCode: true, agent.Pi: true}},
		{"nameless", nameless, map[agent.Name]bool{agent.Codex: false, agent.Claude: true, agent.OpenCode: false, agent.Pi: true}},
		{"no description", silent, map[agent.Name]bool{agent.Codex: false, agent.Claude: true, agent.OpenCode: false, agent.Pi: false}},
		{"long description", verbose, map[agent.Name]bool{agent.Codex: false, agent.Claude: false, agent.OpenCode: false, agent.Pi: false}},
	}

	for _, test := range tests {
		for name, accepted := range test.accepted {
			t.Run(test.label+"/"+string(name), func(t *testing.T) {
				// Arrange
				adapter, err := registry().Lookup(name)
				require.NoError(t, err)

				// Act
				err = adapter.Validate("review", test.metadata)

				// Assert
				if accepted {
					require.NoError(t, err)
					return
				}
				require.ErrorIs(t, err, agent.ErrIncompatibleSkill)
			})
		}
	}
}

func TestOpenCodeRequiresMatchingDirectoryName(t *testing.T) {
	// Arrange
	adapter, err := registry().Lookup(agent.OpenCode)
	require.NoError(t, err)

	// Act
	err = adapter.Validate("renamed", skill.Metadata{Name: "review", Description: "Reviews code."})

	// Assert
	require.ErrorIs(t, err, agent.ErrIncompatibleSkill)
}
