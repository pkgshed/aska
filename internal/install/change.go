package install

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"

	"github.com/pkgshed/aska/internal/agent"
	"github.com/pkgshed/aska/internal/skill"
	"github.com/pkgshed/aska/internal/source"
	"github.com/pkgshed/aska/internal/state"
	"github.com/pkgshed/aska/internal/vcs"
)

// maxDiffBytes bounds the size of a displayed patch.
const maxDiffBytes = 4 << 20

// ErrNoBaseline reports a patch request for a skill that is not installed
// yet, which has nothing to compare against.
var ErrNoBaseline = errors.New("no installed version to compare with")

// Reviewer inspects changes before they are applied. Only changes that
// bring content the scope has not installed before are passed to it.
type Reviewer interface {
	// Review reports whether the changes may be applied.
	Review(ctx context.Context, changes []Change) (bool, error)
}

// Change describes one skill an operation is about to install. It has
// passed every check that needs no changes on disk: the skill satisfies
// every agent's rules and none of its target directories is unmanaged.
type Change struct {
	// Name is the directory name the skill is installed under.
	Name    string
	Package source.Package
	// Metadata is the frontmatter of the new SKILL.md.
	Metadata skill.Metadata
	// Agents and Dirs are the agents installed for and their directories.
	Agents []agent.Name
	Dirs   []string
	// Current is the lock in effect, valid when HasCurrent is set.
	Current    state.Lock
	HasCurrent bool
	// Next is what the skill will be locked to.
	Next state.Lock
	// Diffstat summarizes the changes against Current, or is empty when
	// there is no current version or the old commit is unavailable.
	Diffstat string

	candidate    candidate
	targets      []target
	previousName string
}

// IsNew reports whether the scope does not have the skill yet.
func (c Change) IsNew() bool { return !c.HasCurrent }

// NeedsReview reports whether the change brings content the scope has not
// installed before. A change that only re-records existing content, for
// example after selecting different agents, needs no review.
func (c Change) NeedsReview() bool {
	return !c.HasCurrent || c.Current.Digest != c.Next.Digest
}

// File is one file of a skill under review.
type File struct {
	// Path is the slash-separated path relative to the skill root.
	Path       string
	Size       int64
	Executable bool
}

// Files lists the files of the new version in path order.
func (c Change) Files() ([]File, error) {
	root, err := os.OpenRoot(c.candidate.snapshot.dir)
	if err != nil {
		return nil, fmt.Errorf("open snapshot: %w", err)
	}
	defer root.Close()

	var files []File
	walk := func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil || entry.IsDir() {
			return walkErr
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		files = append(files, File{Path: name, Size: info.Size(), Executable: info.Mode().Perm()&0o111 != 0})
		return nil
	}
	if err := fs.WalkDir(root.FS(), ".", walk); err != nil {
		return nil, fmt.Errorf("list snapshot: %w", err)
	}
	return files, nil
}

// ReadFile returns the content of one file of the new version.
func (c Change) ReadFile(name string) ([]byte, error) {
	root, err := os.OpenRoot(c.candidate.snapshot.dir)
	if err != nil {
		return nil, fmt.Errorf("open snapshot: %w", err)
	}
	defer root.Close()

	content, err := root.ReadFile(name)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", name, err)
	}
	return content, nil
}

// Patch returns the unified diff from the current to the new version. It
// fails with [ErrNoBaseline] for new skills.
func (c Change) Patch(ctx context.Context) ([]byte, error) {
	if !c.HasCurrent {
		return nil, ErrNoBaseline
	}

	repo := c.candidate.repo
	if err := repo.Fetch(ctx, c.Current.Commit, ""); err != nil {
		return nil, err
	}
	return repo.Diff(ctx, c.Current.Commit, c.Next.Commit, c.Package.Path.String(),
		vcs.DiffOptions{MaxBytes: maxDiffBytes})
}

// describe turns a prepared candidate into a change against the state
// snapshot. It applies every check that does not modify anything, so that
// problems surface before the user spends time reviewing.
func (e *Engine) describe(ctx context.Context, scope state.Scope, snapshot *state.State, prepared candidate,
	options AddOptions,
) (Change, error) {
	pkg := prepared.request.Package
	metadata := prepared.snapshot.metadata
	name, err := installName(pkg, metadata)
	if err != nil {
		return Change{}, err
	}

	previousName, wasRequested := snapshot.FindPackage(pkg)
	if existing, taken := snapshot.Skills[name]; taken && existing.Package != pkg {
		return Change{}, fmt.Errorf("%w: %q is already provided by %s", ErrNameConflict, name, existing.Package)
	}

	agents := selectAgents(options, snapshot.Skills[previousName].Agents, wasRequested)
	if err := e.validateForAgents(name, metadata, agents); err != nil {
		return Change{}, err
	}
	targets, err := e.planTargets(scope, name, agents)
	if err != nil {
		return Change{}, err
	}

	current, hasCurrent := snapshot.Locks[previousName]
	change := Change{
		Name:         name,
		Package:      pkg,
		Metadata:     metadata,
		Agents:       agents,
		Dirs:         targetDirs(targets),
		Current:      current,
		HasCurrent:   hasCurrent,
		Next:         prepared.lock(name),
		candidate:    prepared,
		targets:      targets,
		previousName: previousName,
	}

	if err := e.precheckTargets(change, options.Force); err != nil {
		return Change{}, err
	}
	if hasCurrent && change.NeedsReview() {
		change.Diffstat = e.diffstat(ctx, prepared.repo, current, change.Next)
	}
	return change, nil
}

// precheckTargets fails early for unmanaged directories. Publication checks
// again under the scope lock.
func (e *Engine) precheckTargets(change Change, force bool) error {
	owner := change.ownership(e, force)
	for _, item := range change.targets {
		if _, err := owner.check(item.dir); err != nil {
			return err
		}
	}
	return nil
}

func (c Change) ownership(e *Engine, force bool) ownership {
	return ownership{
		engine:    e,
		pkg:       c.Package,
		previous:  c.Current.Digest,
		wanted:    c.Next.Digest,
		overwrite: force,
	}
}

// diffstat summarizes the changes between two locks. It is informational,
// so failures leave it empty.
func (e *Engine) diffstat(ctx context.Context, repo *vcs.Repo, current, next state.Lock) string {
	if err := repo.Fetch(ctx, current.Commit, ""); err != nil {
		return ""
	}
	stat, err := repo.Diff(ctx, current.Commit, next.Commit, next.Package.Path.String(), vcs.DiffOptions{Stat: true})
	if err != nil {
		return ""
	}
	return string(stat)
}

// review asks the reviewer about the changes that bring new content.
func review(ctx context.Context, reviewer Reviewer, changes []Change) (bool, error) {
	var pending []Change
	for _, change := range changes {
		if change.NeedsReview() {
			pending = append(pending, change)
		}
	}
	if len(pending) == 0 {
		return true, nil
	}
	return reviewer.Review(ctx, pending)
}

// applyChanges installs approved changes after confirming that no other
// process changed the affected skills since the snapshot was taken, which
// guarantees that what is installed is exactly what was reviewed.
func (e *Engine) applyChanges(ctx context.Context, scope state.Scope, snapshot *state.State, changes []Change,
	force bool,
) ([]Installed, error) {
	var installed []Installed
	err := e.withScope(ctx, scope, func(current *state.State) error {
		for _, change := range changes {
			for _, name := range []string{change.previousName, change.Name} {
				if name != "" && !sameEntry(snapshot, current, name) {
					return fmt.Errorf("%w: %s; run the command again", ErrStateChanged, name)
				}
			}
		}

		for _, change := range changes {
			result, err := e.installChange(scope, current, change, force)
			if err != nil {
				return err
			}
			installed = append(installed, result)
		}
		return scope.Save(current)
	})
	if err != nil {
		return nil, err
	}
	return installed, nil
}
