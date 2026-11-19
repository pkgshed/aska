package install

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/pkgshed/aska/internal/agent"
	"github.com/pkgshed/aska/internal/digest"
	"github.com/pkgshed/aska/internal/skill"
	"github.com/pkgshed/aska/internal/state"
)

// Status is the condition of an installed directory relative to its lock.
type Status int

const (
	// StatusOK means the directory matches the locked digest.
	StatusOK Status = iota
	// StatusModified means the directory content differs from the lock.
	StatusModified
	// StatusMissing means the directory does not exist.
	StatusMissing
	// StatusUnlocked means there is no lock to compare against.
	StatusUnlocked
)

func (s Status) String() string {
	switch s {
	case StatusOK:
		return "ok"
	case StatusModified:
		return "modified"
	case StatusMissing:
		return "missing"
	default:
		return "unlocked"
	}
}

// TargetReport describes one installed directory.
type TargetReport struct {
	Dir    string
	Agents []agent.Name
	Status Status
	// Actual is the digest found on disk, if the directory could be read.
	Actual digest.Digest
}

// SkillReport describes the state of one requested skill.
type SkillReport struct {
	Name    string
	Skill   state.Skill
	Lock    state.Lock
	Locked  bool
	Targets []TargetReport
}

// Healthy reports whether every directory matches the lock.
func (r SkillReport) Healthy() bool {
	if !r.Locked {
		return false
	}
	for _, item := range r.Targets {
		if item.Status != StatusOK {
			return false
		}
	}
	return true
}

// Status summarizes the worst condition among the directories.
func (r SkillReport) Status() Status {
	if !r.Locked {
		return StatusUnlocked
	}
	worst := StatusOK
	for _, item := range r.Targets {
		worst = max(worst, item.Status)
	}
	return worst
}

// Report inspects the named skills, or every skill when names is empty,
// and compares their installed directories with the lockfile.
func (e *Engine) Report(ctx context.Context, scope state.Scope, names []string) ([]SkillReport, error) {
	current, err := scope.Load(e.Policy)
	if err != nil {
		return nil, err
	}
	if len(names) == 0 {
		names = current.Names()
	}

	reports := make([]SkillReport, 0, len(names))
	for _, name := range names {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		report, err := e.reportOne(scope, current, name)
		if err != nil {
			return nil, err
		}
		reports = append(reports, report)
	}
	return reports, nil
}

func (e *Engine) reportOne(scope state.Scope, current *state.State, name string) (SkillReport, error) {
	requested, found := current.Skills[name]
	if !found {
		return SkillReport{}, fmt.Errorf("%w: %q in %s", ErrNotInstalled, name, scope)
	}

	lock, locked := current.CurrentLock(name)
	targets, err := e.planTargets(scope, name, requested.Agents)
	if err != nil {
		return SkillReport{}, err
	}

	report := SkillReport{Name: name, Skill: requested, Lock: lock, Locked: locked}
	for _, item := range targets {
		report.Targets = append(report.Targets, inspectTarget(item, lock, locked))
	}
	return report, nil
}

func inspectTarget(item target, lock state.Lock, locked bool) TargetReport {
	report := TargetReport{Dir: item.dir, Agents: item.agents}

	if _, err := os.Lstat(item.dir); errors.Is(err, fs.ErrNotExist) {
		report.Status = StatusMissing
		return report
	}

	actual, err := digest.OfDir(item.dir)
	report.Actual = actual
	switch {
	case !locked:
		report.Status = StatusUnlocked
	case err != nil || actual != lock.Digest:
		report.Status = StatusModified
	default:
		report.Status = StatusOK
	}
	return report
}

// Metadata reads the SKILL.md frontmatter of an installed skill, preferring
// the cached snapshot and falling back to an installed directory.
func (e *Engine) Metadata(report SkillReport) (skill.Metadata, error) {
	if report.Locked {
		if dir, found, err := e.Cache.Lookup(report.Lock.Package.CacheKey(), report.Lock.Digest); err == nil && found {
			return readMetadata(dir)
		}
	}

	for _, item := range report.Targets {
		if item.Status == StatusMissing {
			continue
		}
		if metadata, err := readMetadata(item.Dir); err == nil {
			return metadata, nil
		}
	}
	return skill.Metadata{}, fmt.Errorf("read metadata of %s: no readable copy of %s",
		report.Name, filepath.Join(report.Name, skill.ManifestName))
}
