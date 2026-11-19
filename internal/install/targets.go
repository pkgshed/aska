package install

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"

	"github.com/pkgshed/aska/internal/agent"
	"github.com/pkgshed/aska/internal/digest"
	"github.com/pkgshed/aska/internal/skill"
	"github.com/pkgshed/aska/internal/source"
	"github.com/pkgshed/aska/internal/state"
)

// stagingName is the directory, next to an agent's skills directory, that
// holds copies under construction. Keeping it outside the skills directory
// stops agents from discovering half-written skills.
const stagingName = ".aska-staging"

// target is one installed directory, shared by every agent that scans the
// same skills directory.
type target struct {
	dir    string
	agents []agent.Name
}

// planTargets maps agents to the directories that must hold the skill.
func (e *Engine) planTargets(scope state.Scope, name string, agents []agent.Name) ([]target, error) {
	if len(agents) == 0 {
		return nil, ErrNoAgents
	}

	byDir := map[string]*target{}
	var order []string
	for _, agentName := range agents {
		adapter, err := e.Agents.Lookup(agentName)
		if err != nil {
			return nil, err
		}

		dir := filepath.Join(adapter.SkillsDir(scope.Location()), name)
		if existing, seen := byDir[dir]; seen {
			existing.agents = append(existing.agents, agentName)
			continue
		}
		byDir[dir] = &target{dir: dir, agents: []agent.Name{agentName}}
		order = append(order, dir)
	}

	targets := make([]target, 0, len(order))
	for _, dir := range order {
		targets = append(targets, *byDir[dir])
	}
	return targets, nil
}

// validateForAgents checks that every agent would load the skill.
func (e *Engine) validateForAgents(name string, metadata skill.Metadata, agents []agent.Name) error {
	for _, agentName := range agents {
		adapter, err := e.Agents.Lookup(agentName)
		if err != nil {
			return err
		}
		if err := adapter.Validate(name, metadata); err != nil {
			return err
		}
	}
	return nil
}

// targetStatus is what currently occupies a target directory.
type targetStatus int

const (
	statusMissing targetStatus = iota
	statusCurrent
	statusOwned
	statusForeign
)

// ownership decides who owns the content of target directories. Content is
// owned by aska when its digest equals the previously locked digest or any
// snapshot aska extracted for the package; content that matches neither
// was created or modified by someone else.
type ownership struct {
	engine    *Engine
	pkg       source.Package
	previous  digest.Digest
	wanted    digest.Digest
	overwrite bool
}

func (o ownership) inspect(dir string) (targetStatus, error) {
	info, err := os.Lstat(dir)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return statusMissing, nil
	case err != nil:
		return statusForeign, fmt.Errorf("inspect %s: %w", dir, err)
	case !info.IsDir():
		return statusForeign, nil
	}

	// Content that cannot be digested, for example because it contains a
	// symbolic link, was not produced by aska.
	actual, digestErr := digest.OfDir(dir)
	isReadable := digestErr == nil
	switch {
	case !isReadable:
		return statusForeign, nil
	case actual == o.wanted:
		return statusCurrent, nil
	case actual == o.previous, o.engine.Cache.Contains(o.pkg.CacheKey(), actual):
		return statusOwned, nil
	default:
		return statusForeign, nil
	}
}

// check fails for foreign content unless overwriting was requested.
func (o ownership) check(dir string) (targetStatus, error) {
	status, err := o.inspect(dir)
	if err != nil {
		return status, err
	}
	if status == statusForeign && !o.overwrite {
		return status, fmt.Errorf("%w %s: it was not installed by aska or has local changes; "+
			"rerun with --force to replace it", ErrUnmanagedTarget, dir)
	}
	return status, nil
}

// publication installs one snapshot into a set of targets.
type publication struct {
	ownership ownership
	source    string
	targets   []target
}

// apply checks every target before changing any, so that an unmanaged
// directory aborts the installation without partial results. It reports
// whether any directory changed.
func (p publication) apply() (bool, error) {
	statuses := make([]targetStatus, len(p.targets))
	for index, item := range p.targets {
		status, err := p.ownership.check(item.dir)
		if err != nil {
			return false, err
		}
		statuses[index] = status
	}

	changed := false
	for index, item := range p.targets {
		if statuses[index] == statusCurrent {
			continue
		}
		if err := publishDir(p.source, item.dir); err != nil {
			return changed, err
		}
		changed = true
	}
	return changed, nil
}

// publishDir copies a snapshot into a staging directory beside the skills
// directory and renames it into place. An existing directory is moved
// aside first and restored if the final rename fails.
func publishDir(snapshotDir, dir string) error {
	work, cleanup, err := makeStaging(dir)
	if err != nil {
		return err
	}
	defer cleanup()

	staged := filepath.Join(work, "new")
	if err := os.CopyFS(staged, os.DirFS(snapshotDir)); err != nil {
		return fmt.Errorf("stage %s: %w", dir, err)
	}

	if !fileExists(dir) {
		if err := os.Rename(staged, dir); err != nil {
			return fmt.Errorf("publish %s: %w", dir, err)
		}
		return nil
	}

	previous := filepath.Join(work, "old")
	if err := os.Rename(dir, previous); err != nil {
		return fmt.Errorf("replace %s: %w", dir, err)
	}
	if err := os.Rename(staged, dir); err != nil {
		restoreErr := os.Rename(previous, dir)
		return errors.Join(fmt.Errorf("publish %s: %w", dir, err), restoreErr)
	}
	return nil
}

// removeDir moves a target into staging before deleting it, so the skill
// disappears from the agent's view in one step.
func removeDir(dir string) error {
	if !fileExists(dir) {
		return nil
	}

	work, cleanup, err := makeStaging(dir)
	if err != nil {
		return err
	}
	defer cleanup()

	if err := os.Rename(dir, filepath.Join(work, "old")); err != nil {
		return fmt.Errorf("remove %s: %w", dir, err)
	}
	return nil
}

// makeStaging creates a private work directory in the staging directory
// next to the skills directory of dir. The returned cleanup removes the
// work directory and, when no other work remains, the staging directory.
func makeStaging(dir string) (string, func(), error) {
	skillsDir := filepath.Dir(dir)
	if err := os.MkdirAll(skillsDir, 0o755); err != nil {
		return "", nil, fmt.Errorf("create %s: %w", skillsDir, err)
	}

	stagingRoot := filepath.Join(filepath.Dir(skillsDir), stagingName)
	if err := os.MkdirAll(stagingRoot, 0o755); err != nil {
		return "", nil, fmt.Errorf("create staging directory: %w", err)
	}

	work, err := os.MkdirTemp(stagingRoot, "work-*")
	if err != nil {
		return "", nil, fmt.Errorf("create staging directory: %w", err)
	}

	cleanup := func() {
		_ = os.RemoveAll(work)
		_ = os.Remove(stagingRoot) // Fails harmlessly while other work remains.
	}
	return work, cleanup, nil
}

// fileExists reports whether a path exists without following a final link.
func fileExists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

// removeStaleStaging deletes staging directories that interrupted runs left
// behind. It runs under the scope lock, so no other process is using them.
func (e *Engine) removeStaleStaging(scope state.Scope) error {
	var roots []string
	for _, adapter := range e.Agents {
		skillsDir := adapter.SkillsDir(scope.Location())
		root := filepath.Join(filepath.Dir(skillsDir), stagingName)
		if !slices.Contains(roots, root) {
			roots = append(roots, root)
		}
	}

	var errs []error
	for _, root := range roots {
		errs = append(errs, os.RemoveAll(root))
	}
	if err := errors.Join(errs...); err != nil {
		return fmt.Errorf("remove stale staging directories: %w", err)
	}
	return nil
}

// targetDirs returns the directories of targets.
func targetDirs(targets []target) []string {
	dirs := make([]string, 0, len(targets))
	for _, item := range targets {
		dirs = append(dirs, item.dir)
	}
	return dirs
}

// obsoleteDirs returns the directories in before that are not in after.
func obsoleteDirs(before, after []target) []string {
	keep := targetDirs(after)
	var obsolete []string
	for _, dir := range targetDirs(before) {
		if !slices.Contains(keep, dir) {
			obsolete = append(obsolete, dir)
		}
	}
	return obsolete
}
