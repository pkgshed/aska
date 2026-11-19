// Package config loads the user configuration of aska.
//
// The configuration lives in $XDG_CONFIG_HOME/aska/config.toml. Every
// setting is optional; omitted settings keep their defaults. Unknown keys
// are rejected so that typos do not go unnoticed.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	z "github.com/Oudwins/zog"
	"github.com/pelletier/go-toml/v2"
)

// ErrInvalidConfig reports a configuration file that cannot be used.
var ErrInvalidConfig = errors.New("invalid configuration")

// Config is the user configuration.
type Config struct {
	// DefaultAgents are installed for when a command names no agent.
	DefaultAgents []string `toml:"default_agents"`
	// Security relaxes transport and network restrictions.
	Security Security `toml:"security"`
	// Limits bounds the size of extracted skills.
	Limits Limits `toml:"limits"`
	// Git configures the git executable.
	Git Git `toml:"git"`
	// Agents overrides the skill directories of individual agents.
	Agents Agents `toml:"agents"`
	// Review configures the inspection of skills before installation.
	Review Review `toml:"review"`
	// Log configures diagnostic output.
	Log Log `toml:"log"`
}

// Security relaxes the default restrictions on remote access.
type Security struct {
	// AllowInsecure permits http:// and git:// repositories and discovery
	// over plain HTTP.
	AllowInsecure bool `toml:"allow_insecure"`
	// AllowPrivateNetworks lets discovery contact loopback and private
	// addresses, for example a forge on the local network.
	AllowPrivateNetworks bool `toml:"allow_private_networks"`
}

// Limits bounds extracted skills.
type Limits struct {
	MaxFiles     int   `toml:"max_files"`
	MaxFileSize  int64 `toml:"max_file_size"`
	MaxTotalSize int64 `toml:"max_total_size"`
}

// Git configures how git runs.
type Git struct {
	// Command is the git executable, looked up in PATH unless absolute.
	Command string `toml:"command"`
	// Timeout bounds every network operation.
	Timeout Duration `toml:"timeout"`
}

// Agents holds per-agent directory overrides.
type Agents struct {
	Codex    AgentDirs `toml:"codex"`
	Claude   AgentDirs `toml:"claude"`
	OpenCode AgentDirs `toml:"opencode"`
	Pi       AgentDirs `toml:"pi"`
}

// AgentDirs overrides where an agent's skills are installed. Empty fields
// keep the agent's default.
type AgentDirs struct {
	// ProjectDir is relative to the repository root.
	ProjectDir string `toml:"project_dir"`
	// UserDir is absolute or starts with "~/".
	UserDir string `toml:"user_dir"`
}

// Review configures the inspection of new and changed skills before they
// are installed.
type Review struct {
	// Enabled shows the review menu by default.
	Enabled bool `toml:"enabled"`
	// Pager is the shell command that displays review material. The
	// ASKA_PAGER environment variable overrides it; PAGER and then
	// "less -R" are the fallbacks.
	Pager string `toml:"pager"`
}

// Log configures diagnostic output.
type Log struct {
	// Level is one of debug, info, warn or error.
	Level string `toml:"level"`
}

// Duration is a [time.Duration] written as text, such as "5m".
type Duration time.Duration

// UnmarshalText implements [encoding.TextUnmarshaler].
func (d *Duration) UnmarshalText(text []byte) error {
	parsed, err := time.ParseDuration(string(text))
	if err != nil {
		return err
	}
	*d = Duration(parsed)
	return nil
}

// Default returns the configuration used when no file exists.
func Default() Config {
	return Config{
		DefaultAgents: []string{"codex", "claude"},
		Limits: Limits{
			MaxFiles:     1000,
			MaxFileSize:  8 << 20,
			MaxTotalSize: 32 << 20,
		},
		Git: Git{
			Command: "git",
			Timeout: Duration(10 * time.Minute),
		},
		Review: Review{Enabled: true},
		Log:    Log{Level: "info"},
	}
}

// Load reads the configuration file at path on top of the defaults. A
// missing file is not an error.
func Load(path string) (Config, error) {
	cfg := Default()

	data, err := os.ReadFile(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return cfg, nil
	case err != nil:
		return Config{}, fmt.Errorf("read config %s: %w", path, err)
	}

	decoder := toml.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&cfg); err != nil {
		return Config{}, fmt.Errorf("%w %s: %w", ErrInvalidConfig, path, err)
	}

	if err := cfg.Validate(); err != nil {
		return Config{}, fmt.Errorf("%w %s: %w", ErrInvalidConfig, path, err)
	}
	return cfg, nil
}

var agentNames = []string{"codex", "claude", "opencode", "pi"}

var agentDirsSchema = z.Struct(z.Shape{
	"ProjectDir": z.String().TestFunc(isRelativeLocalPath,
		z.Message("project_dir must be a relative path inside the repository")),
	"UserDir": z.String().TestFunc(isAbsoluteOrHomePath,
		z.Message("user_dir must be absolute or start with ~/")),
})

var configSchema = z.Struct(z.Shape{
	"DefaultAgents": z.Slice(z.String().OneOf(agentNames,
		z.Message("unknown agent; known agents: "+strings.Join(agentNames, ", ")))).
		Min(1, z.Message("default_agents must name at least one agent")),
	"Limits": z.Struct(z.Shape{
		"MaxFiles":     z.Int().Required(positive("max_files")).GT(0, positive("max_files")),
		"MaxFileSize":  z.Int64().Required(positive("max_file_size")).GT(0, positive("max_file_size")),
		"MaxTotalSize": z.Int64().Required(positive("max_total_size")).GT(0, positive("max_total_size")),
	}),
	"Git": z.Struct(z.Shape{
		"Command": z.String().Required(z.Message("command must not be empty")),
		"Timeout": z.IntLike[Duration]().Required(positive("timeout")).GT(0, positive("timeout")),
	}),
	"Agents": z.Struct(z.Shape{
		"Codex":    agentDirsSchema,
		"Claude":   agentDirsSchema,
		"OpenCode": agentDirsSchema,
		"Pi":       agentDirsSchema,
	}),
	"Log": z.Struct(z.Shape{
		"Level": z.String().OneOf([]string{"debug", "info", "warn", "error"}),
	}),
})

func positive(key string) z.TestOption { return z.Message(key + " must be positive") }

// Validate checks the configuration against its schema.
func (c *Config) Validate() error {
	issues := configSchema.Validate(c)
	if len(issues) == 0 {
		return nil
	}

	// The schema visits fields in map order; sort for stable messages.
	slices.SortStableFunc(issues, func(a, b *z.ZogIssue) int {
		return strings.Compare(z.Issues.FlattenPath(a.Path), z.Issues.FlattenPath(b.Path))
	})
	return errors.New(strings.TrimSpace(z.Issues.Prettify(issues)))
}

func isRelativeLocalPath(value *string, _ z.Ctx) bool {
	return *value == "" || filepath.IsLocal(filepath.FromSlash(*value))
}

func isAbsoluteOrHomePath(value *string, _ z.Ctx) bool {
	return *value == "" || filepath.IsAbs(*value) || strings.HasPrefix(*value, "~/")
}
