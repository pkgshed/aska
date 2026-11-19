// Package resolve turns a version query into an immutable commit.
//
// Resolution works on the refs a remote advertises and never needs a clone,
// except for abbreviated commit ids, which the caller resolves against a
// fetched history. The rules for choosing between repository-wide and
// per-skill version tags are documented on [Resolve].
package resolve

import (
	"errors"
	"fmt"
	"strings"

	"github.com/pkgshed/aska/internal/semver"
	"github.com/pkgshed/aska/internal/vcs"
)

// ErrInvalidQuery reports text that is not a valid version query.
var ErrInvalidQuery = errors.New("invalid version query")

// Query is a parsed version query: one of [Latest], [Exact], [Range],
// [Commit] or [Revision].
type Query interface {
	// String returns the canonical text of the query.
	String() string
	isQuery()
}

// Latest selects the highest stable release.
type Latest struct{}

// Exact selects one release version, which may be a prerelease.
type Exact struct{ Version semver.Version }

// Range selects the highest stable release allowed by a constraint.
type Range struct{ Constraint semver.Constraint }

// Commit selects a commit by its full id.
type Commit struct{ Hash vcs.Hash }

// Revision selects a branch or tag by name, or a commit by an abbreviated
// id when no ref has that name.
type Revision struct{ Name string }

func (Latest) String() string     { return "latest" }
func (q Exact) String() string    { return q.Version.String() }
func (q Range) String() string    { return q.Constraint.String() }
func (q Commit) String() string   { return q.Hash.String() }
func (q Revision) String() string { return q.Name }

func (Latest) isQuery()   {}
func (Exact) isQuery()    {}
func (Range) isQuery()    {}
func (Commit) isQuery()   {}
func (Revision) isQuery() {}

// ParseQuery interprets the text after "@" in a package reference:
//
//	""  or "latest"        the highest stable release
//	v1.2.3 or 1.2.3        exactly that release
//	v1, v1.2, ^1.2, ~1.2   the highest stable release in the range
//	>=1.2 <2               the highest stable release in the range
//	<40 or 64 hex digits>  that commit
//	main, refs/tags/x      that branch or tag, or an abbreviated commit id
func ParseQuery(text string) (Query, error) {
	switch {
	case text == "" || text == "latest":
		return Latest{}, nil
	case vcs.IsFullHash(text):
		return Commit{Hash: vcs.Hash(text)}, nil
	case looksLikeVersion(text):
		return parseVersionQuery(text)
	default:
		return parseRevision(text)
	}
}

// looksLikeVersion reports whether text starts like a version or a
// constraint operator rather than like a ref name.
func looksLikeVersion(text string) bool {
	if strings.ContainsAny(text[:1], "^~<>=") {
		return true
	}
	rest := strings.TrimPrefix(text, "v")
	return rest != "" && rest[0] >= '0' && rest[0] <= '9' && !vcs.IsHashPrefix(text)
}

func parseVersionQuery(text string) (Query, error) {
	exact, err := semver.Parse("v" + strings.TrimPrefix(text, "v"))
	if err == nil {
		return Exact{Version: exact}, nil
	}

	constraint, err := semver.ParseConstraint(text)
	if err != nil {
		return nil, fmt.Errorf("%w %q: %w", ErrInvalidQuery, text, err)
	}
	return Range{Constraint: constraint}, nil
}

// parseRevision accepts ref names that follow git's ref naming rules.
func parseRevision(text string) (Query, error) {
	if err := vcs.CheckRefName(text); err != nil {
		return nil, fmt.Errorf("%w %q: %w", ErrInvalidQuery, text, err)
	}
	return Revision{Name: text}, nil
}
