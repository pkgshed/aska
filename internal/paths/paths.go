// Package paths locates aska's files according to the XDG Base Directory
// Specification.
package paths

import (
	"path/filepath"

	"github.com/adrg/xdg"
)

const appName = "aska"

// Paths holds the base directories of aska.
type Paths struct {
	// ConfigFile is the user configuration, $XDG_CONFIG_HOME/aska/config.toml.
	ConfigFile string
	// DataDir holds repositories and snapshots, $XDG_DATA_HOME/aska.
	DataDir string
	// StateDir holds the user scope and locks, $XDG_STATE_HOME/aska.
	StateDir string
}

// Default returns the paths derived from the XDG environment variables, or
// their defaults when unset.
func Default() Paths {
	return Paths{
		ConfigFile: filepath.Join(xdg.ConfigHome, appName, "config.toml"),
		DataDir:    filepath.Join(xdg.DataHome, appName),
		StateDir:   filepath.Join(xdg.StateHome, appName),
	}
}

// Repos returns the directory of bare git repositories.
func (p Paths) Repos() string { return filepath.Join(p.DataDir, "repos") }

// Packages returns the directory of content-addressed snapshots.
func (p Paths) Packages() string { return filepath.Join(p.DataDir, "packages") }
