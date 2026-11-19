// Package state records which skills a scope wants and what they resolved
// to.
//
// Every scope keeps two files side by side. The manifest, skills.toml,
// lists the requested skills: where they come from, the version query and
// the agents to install for. The lockfile, skillslock.toml, pins each of
// them to a commit and a content digest. Installing from the lockfile
// reproduces an installation exactly, updating is an explicit operation,
// and a moved upstream tag is detected instead of silently followed.
package state

import (
	"maps"
	"slices"

	"github.com/pkgshed/aska/internal/agent"
	"github.com/pkgshed/aska/internal/digest"
	"github.com/pkgshed/aska/internal/resolve"
	"github.com/pkgshed/aska/internal/semver"
	"github.com/pkgshed/aska/internal/source"
	"github.com/pkgshed/aska/internal/vcs"
)

// Skill is a manifest entry: a skill the scope asks for.
type Skill struct {
	// Name is the installed directory name, taken from the frontmatter.
	Name string
	// Package locates the skill.
	Package source.Package
	// Requested is the version query.
	Requested resolve.Query
	// Agents lists the agents to install for, sorted.
	Agents []agent.Name
}

// Lock is a lockfile entry: what a skill resolved to.
type Lock struct {
	Name    string
	Package source.Package
	// Requested is the text of the query that produced this resolution.
	Requested string
	// Version is the release version, if a version tag was selected.
	Version semver.Version
	// Tag is the short name of the selecting tag, if any.
	Tag string
	// Branch is the short name of the selecting branch, if any.
	Branch string
	// Commit is the resolved commit.
	Commit vcs.Hash
	// Digest is the content digest of the skill at the commit.
	Digest digest.Digest
}

// Describe returns a short account of the resolution for display.
func (l Lock) Describe() string {
	return resolve.Resolution{Version: l.Version, Tag: l.Tag, Branch: l.Branch, Commit: l.Commit}.Describe()
}

// Matches reports whether the lock was produced for the skill as it is
// currently requested.
func (l Lock) Matches(requested Skill) bool {
	return l.Package == requested.Package && l.Requested == requested.Requested.String()
}

// State is the combined content of a manifest and its lockfile.
type State struct {
	Skills map[string]Skill
	Locks  map[string]Lock
}

// NewState returns an empty state.
func NewState() *State {
	return &State{Skills: map[string]Skill{}, Locks: map[string]Lock{}}
}

// Names returns the names of all requested skills in order.
func (s *State) Names() []string {
	return slices.Sorted(maps.Keys(s.Skills))
}

// Put records a skill together with its lock.
func (s *State) Put(requested Skill, lock Lock) {
	s.Skills[requested.Name] = requested
	s.Locks[lock.Name] = lock
}

// Delete forgets a skill and its lock.
func (s *State) Delete(name string) {
	delete(s.Skills, name)
	delete(s.Locks, name)
}

// FindPackage returns the name under which a package is requested.
func (s *State) FindPackage(pkg source.Package) (string, bool) {
	for _, name := range s.Names() {
		if s.Skills[name].Package == pkg {
			return name, true
		}
	}
	return "", false
}

// CurrentLock returns the lock of a skill if it matches the manifest entry.
func (s *State) CurrentLock(name string) (Lock, bool) {
	requested, wanted := s.Skills[name]
	lock, locked := s.Locks[name]
	if !wanted || !locked || !lock.Matches(requested) {
		return Lock{}, false
	}
	return lock, true
}
