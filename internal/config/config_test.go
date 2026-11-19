package config_test

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pkgshed/aska/internal/config"
)

func TestLoadWithoutFileReturnsDefaults(t *testing.T) {
	// Act
	cfg, err := config.Load(filepath.Join(t.TempDir(), "absent.toml"))

	// Assert
	require.NoError(t, err)
	assert.Equal(t, config.Default(), cfg)
	assert.Equal(t, []string{"codex", "claude"}, cfg.DefaultAgents)
	assert.True(t, cfg.Review.Enabled, "review is on by default")
}

func TestLoadOverlaysFileOnDefaults(t *testing.T) {
	// Act
	cfg, err := config.Load(filepath.Join("testdata", "custom.toml"))

	// Assert
	require.NoError(t, err)
	assert.Equal(t, []string{"pi", "opencode"}, cfg.DefaultAgents)
	assert.True(t, cfg.Security.AllowPrivateNetworks)
	assert.False(t, cfg.Security.AllowInsecure)
	assert.Equal(t, 50, cfg.Limits.MaxFiles)
	assert.Equal(t, config.Default().Limits.MaxFileSize, cfg.Limits.MaxFileSize, "unset keys keep defaults")
	assert.Equal(t, "/usr/bin/git", cfg.Git.Command)
	assert.Equal(t, config.Duration(90*time.Second), cfg.Git.Timeout)
	assert.Equal(t, "~/dotfiles/claude/skills", cfg.Agents.Claude.UserDir)
	assert.Equal(t, "tools/skills", cfg.Agents.Codex.ProjectDir)
	assert.Equal(t, "debug", cfg.Log.Level)
	assert.False(t, cfg.Review.Enabled)
	assert.Equal(t, "bat --paging=always", cfg.Review.Pager)
}

func TestLoadReportsEveryValidationIssue(t *testing.T) {
	// Act
	_, err := config.Load(filepath.Join("testdata", "invalid.toml"))

	// Assert
	require.ErrorIs(t, err, config.ErrInvalidConfig)
	for _, fragment := range []string{
		"unknown agent", "max_files must be positive", "timeout must be positive",
		"project_dir must be a relative path", "user_dir must be absolute", "Log.Level",
	} {
		assert.Contains(t, err.Error(), fragment)
	}
}

func TestLoadRejectsMalformedFiles(t *testing.T) {
	for _, fixture := range []string{"unknown-key.toml", "bad-duration.toml"} {
		t.Run(fixture, func(t *testing.T) {
			// Act
			_, err := config.Load(filepath.Join("testdata", fixture))

			// Assert
			require.ErrorIs(t, err, config.ErrInvalidConfig)
		})
	}
}

func TestValidationMessagesAreStable(t *testing.T) {
	// Arrange
	_, errFirst := config.Load(filepath.Join("testdata", "invalid.toml"))
	require.Error(t, errFirst)

	// Act: the schema visits fields in map order, so repeat the load.
	for range 10 {
		_, errAgain := config.Load(filepath.Join("testdata", "invalid.toml"))

		// Assert
		require.EqualError(t, errAgain, errFirst.Error())
	}
}
