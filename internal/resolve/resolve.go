package resolve

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/pkgshed/aska/internal/semver"
	"github.com/pkgshed/aska/internal/source"
	"github.com/pkgshed/aska/internal/vcs"
)

var (
	// ErrNoRelease reports a repository without a stable release for the
	// skill.
	ErrNoRelease = errors.New("no stable release")

	// ErrNoMatchingVersion reports a version query that matches no tag.
	ErrNoMatchingVersion = errors.New("no matching version")

	// ErrAmbiguous reports a query that could mean more than one commit.
	ErrAmbiguous = errors.New("ambiguous version")

	// ErrUnknownRevision reports a revision that names no branch or tag.
	ErrUnknownRevision = errors.New("unknown revision")
)

// Resolution is the outcome of resolving a query.
type Resolution struct {
	// Version is the release version, or the zero value when the commit
	// was not selected through a version tag.
	Version semver.Version
	// Tag is the short name of the tag that selected the commit, such as
	// "skills/code-review/v1.2.3".
	Tag string
	// Branch is the short name of the branch that selected the commit.
	Branch string
	// Commit is the selected commit. It is empty when CommitPrefix is set.
	Commit vcs.Hash
	// CommitPrefix is an abbreviated commit id that the caller must resolve
	// against the fetched history of the repository.
	CommitPrefix string
}

// FetchRef returns the full name of the ref to fetch, or "" when the commit
// must be fetched by id.
func (r Resolution) FetchRef() string {
	switch {
	case r.Tag != "":
		return vcs.TagPrefix + r.Tag
	case r.Branch != "":
		return vcs.BranchPrefix + r.Branch
	default:
		return ""
	}
}

// Describe returns a short human-readable account of what was selected.
func (r Resolution) Describe() string {
	switch {
	case r.Version.IsValid():
		return fmt.Sprintf("%s (tag %s)", r.Version, r.Tag)
	case r.Tag != "":
		return "tag " + r.Tag
	case r.Branch != "":
		return "branch " + r.Branch
	default:
		return "commit " + r.Commit.Short()
	}
}

// candidate is a version tag applicable to the skill.
type candidate struct {
	version semver.Version
	ref     vcs.RemoteRef
}

func (c candidate) commit() vcs.Hash { return c.ref.Target() }

// Resolve selects a commit for the skill at skillPath from the refs a
// remote advertises.
//
// Version tags come in two schemes. Repository-wide tags look like
// "v1.2.3"; per-skill tags carry the skill path as a prefix, such as
// "skills/code-review/v1.2.3", following the go command's convention for
// modules in subdirectories. Only canonical semantic versions count. The
// scheme is chosen deterministically:
//
//  1. A skill at the repository root uses repository-wide tags.
//  2. A skill in a subdirectory uses its per-skill tags whenever it has at
//     least one; repository-wide tags are then ignored.
//  3. Otherwise it uses repository-wide tags.
//
// Resolution fails rather than guess when the outcome is ambiguous: when
// two tags of the chosen scheme have equal precedence but point at
// different commits, when the selected version also exists in the other
// scheme at a different commit, and when a revision names both a branch and
// a tag that point at different commits.
func Resolve(refs vcs.RemoteRefs, skillPath source.Path, query Query) (Resolution, error) {
	switch query := query.(type) {
	case Latest:
		return resolveLatest(refs, skillPath)
	case Exact:
		return resolveExact(refs, skillPath, query.Version)
	case Range:
		return resolveRange(refs, skillPath, query.Constraint)
	case Commit:
		return Resolution{Commit: query.Hash}, nil
	case Revision:
		return resolveRevision(refs, skillPath, query.Name)
	default:
		return Resolution{}, fmt.Errorf("%w: unsupported query %T", ErrInvalidQuery, query)
	}
}

// schemes holds the version tags of both schemes for one skill.
type schemes struct {
	chosen   []candidate
	other    []candidate
	perSkill bool
}

func collectSchemes(refs vcs.RemoteRefs, skillPath source.Path) schemes {
	repoWide := versionTags(refs, "")
	if skillPath.IsRoot() {
		return schemes{chosen: repoWide}
	}

	perSkill := versionTags(refs, skillPath.TagPrefix())
	if len(perSkill) == 0 {
		return schemes{chosen: repoWide}
	}
	return schemes{chosen: perSkill, other: repoWide, perSkill: true}
}

// versionTags returns the tags named prefix followed by a canonical
// semantic version.
func versionTags(refs vcs.RemoteRefs, prefix string) []candidate {
	var candidates []candidate
	for ref := range refs.Tags() {
		versionText, found := strings.CutPrefix(ref.ShortName(), prefix)
		if !found {
			continue
		}
		version, err := semver.Parse(versionText)
		if err != nil {
			continue
		}
		candidates = append(candidates, candidate{version: version, ref: ref})
	}
	return candidates
}

func resolveLatest(refs vcs.RemoteRefs, skillPath source.Path) (Resolution, error) {
	found := collectSchemes(refs, skillPath)
	best, err := highest(found, func(v semver.Version) bool { return !v.IsPrerelease() })
	if err != nil {
		return Resolution{}, err
	}
	if !best.version.IsValid() {
		return Resolution{}, noReleaseError(refs, found)
	}
	return fromCandidate(best), nil
}

func resolveRange(refs vcs.RemoteRefs, skillPath source.Path, constraint semver.Constraint) (Resolution, error) {
	found := collectSchemes(refs, skillPath)
	best, err := highest(found, func(v semver.Version) bool {
		return !v.IsPrerelease() && constraint.Allows(v)
	})
	if err != nil {
		return Resolution{}, err
	}
	if !best.version.IsValid() {
		return Resolution{}, fmt.Errorf("%w for %q among %s", ErrNoMatchingVersion, constraint, describeScheme(found))
	}
	return fromCandidate(best), nil
}

func resolveExact(refs vcs.RemoteRefs, skillPath source.Path, version semver.Version) (Resolution, error) {
	found := collectSchemes(refs, skillPath)
	matches := func(v semver.Version) bool { return semver.Compare(v, version) == 0 }

	best, err := highest(found, matches)
	if err != nil {
		return Resolution{}, err
	}
	if best.version.IsValid() {
		return fromCandidate(best), nil
	}

	if found.perSkill && slices.ContainsFunc(found.other, func(c candidate) bool { return matches(c.version) }) {
		return Resolution{}, fmt.Errorf("%w %s: the skill has per-skill tags, which take precedence over "+
			"the repository tag %s; request the tag by name or pin its commit", ErrNoMatchingVersion, version, version)
	}
	return Resolution{}, fmt.Errorf("%w %s among %s", ErrNoMatchingVersion, version, describeScheme(found))
}

// highest returns the highest candidate of the chosen scheme accepted by
// keep, or the zero candidate when none qualifies, and rejects ambiguous
// selections.
func highest(found schemes, keep func(semver.Version) bool) (candidate, error) {
	var best candidate
	var rivals []candidate

	for _, current := range found.chosen {
		if !keep(current.version) {
			continue
		}

		order := 1
		if best.version.IsValid() {
			order = semver.Compare(current.version, best.version)
		}
		switch {
		case order > 0:
			best, rivals = current, nil
		case order == 0:
			rivals = append(rivals, current)
		}
	}

	if !best.version.IsValid() {
		return candidate{}, nil
	}
	if err := checkUnambiguous(best, rivals, found.other); err != nil {
		return candidate{}, err
	}
	return best, nil
}

func checkUnambiguous(best candidate, rivals, other []candidate) error {
	for _, rival := range rivals {
		if rival.commit() != best.commit() {
			return fmt.Errorf("%w: tags %s and %s have equal precedence but point at different commits",
				ErrAmbiguous, best.ref.ShortName(), rival.ref.ShortName())
		}
	}

	for _, counterpart := range other {
		sameVersion := semver.Compare(counterpart.version, best.version) == 0
		if sameVersion && counterpart.commit() != best.commit() {
			return fmt.Errorf("%w: tags %s and %s both name %s but point at different commits",
				ErrAmbiguous, best.ref.ShortName(), counterpart.ref.ShortName(), best.version)
		}
	}
	return nil
}

func fromCandidate(selected candidate) Resolution {
	return Resolution{
		Version: selected.version,
		Tag:     selected.ref.ShortName(),
		Commit:  selected.commit(),
	}
}

func describeScheme(found schemes) string {
	if found.perSkill {
		return "per-skill tags"
	}
	return "repository tags"
}

func noReleaseError(refs vcs.RemoteRefs, found schemes) error {
	hint := "install a branch or commit explicitly"
	if branch, isBranch := strings.CutPrefix(refs.Head, vcs.BranchPrefix); isBranch {
		hint = fmt.Sprintf("install a branch or commit explicitly, for example @%s", branch)
	}

	if len(found.chosen) > 0 {
		return fmt.Errorf("%w: only prereleases among %s; request one exactly or %s",
			ErrNoRelease, describeScheme(found), hint)
	}
	return fmt.Errorf("%w: the repository has no version tags; %s", ErrNoRelease, hint)
}

func resolveRevision(refs vcs.RemoteRefs, skillPath source.Path, name string) (Resolution, error) {
	if strings.HasPrefix(name, "refs/") {
		ref, found := refs.Lookup(name)
		if !found {
			return Resolution{}, fmt.Errorf("%w %q", ErrUnknownRevision, name)
		}
		return fromRef(ref, skillPath), nil
	}

	branch, isBranch := refs.Lookup(vcs.BranchPrefix + name)
	tag, isTag := refs.Lookup(vcs.TagPrefix + name)
	switch {
	case isBranch && isTag && branch.Target() != tag.Target():
		return Resolution{}, fmt.Errorf("%w: %q names both a branch and a tag; use refs/heads/%s or refs/tags/%s",
			ErrAmbiguous, name, name, name)
	case isTag:
		return fromRef(tag, skillPath), nil
	case isBranch:
		return fromRef(branch, skillPath), nil
	case vcs.IsHashPrefix(name):
		return Resolution{CommitPrefix: name}, nil
	default:
		return Resolution{}, fmt.Errorf("%w %q: no branch or tag has that name", ErrUnknownRevision, name)
	}
}

// fromRef builds a resolution for an explicitly named ref. A tag that
// follows the version scheme of the skill also reports its version.
func fromRef(ref vcs.RemoteRef, skillPath source.Path) Resolution {
	if !ref.IsTag() {
		return Resolution{Branch: ref.ShortName(), Commit: ref.Target()}
	}

	resolution := Resolution{Tag: ref.ShortName(), Commit: ref.Target()}
	for _, prefix := range []string{skillPath.TagPrefix(), ""} {
		versionText, found := strings.CutPrefix(ref.ShortName(), prefix)
		if version, err := semver.Parse(versionText); found && err == nil {
			resolution.Version = version
			break
		}
	}
	return resolution
}
