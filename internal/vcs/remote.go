package vcs

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"iter"
	"maps"
	"slices"
	"strings"
)

// Ref prefixes understood by the resolver.
const (
	BranchPrefix = "refs/heads/"
	TagPrefix    = "refs/tags/"
)

// ErrEmptyRemote reports a remote without any branches or tags.
var ErrEmptyRemote = errors.New("remote has no refs")

// RemoteRef is a branch or tag advertised by a remote.
type RemoteRef struct {
	// Name is the full ref name, such as "refs/tags/v1.2.3".
	Name string
	// Object is the object the ref points at. For annotated tags this is
	// the tag object itself.
	Object Hash
	// Peeled is the object an annotated tag ultimately points at, or empty
	// for refs that point at a commit directly.
	Peeled Hash
}

// Target returns the object the ref resolves to after peeling tags.
func (r RemoteRef) Target() Hash {
	if r.Peeled != "" {
		return r.Peeled
	}
	return r.Object
}

// IsTag reports whether the ref is a tag.
func (r RemoteRef) IsTag() bool { return strings.HasPrefix(r.Name, TagPrefix) }

// ShortName returns the ref name without its "refs/heads/" or "refs/tags/"
// prefix.
func (r RemoteRef) ShortName() string {
	if name, found := strings.CutPrefix(r.Name, TagPrefix); found {
		return name
	}
	return strings.TrimPrefix(r.Name, BranchPrefix)
}

// RemoteRefs is the set of branches and tags a remote advertises.
type RemoteRefs struct {
	// Head is the branch the remote HEAD points at, for example
	// "refs/heads/main", or empty when the remote does not say.
	Head   string
	refs   map[string]RemoteRef
	format ObjectFormat
}

// NewRemoteRefs builds a ref set, for example from test fixtures.
func NewRemoteRefs(head string, refs ...RemoteRef) RemoteRefs {
	set := RemoteRefs{Head: head, refs: make(map[string]RemoteRef, len(refs))}
	for _, ref := range refs {
		set.refs[ref.Name] = ref
		set.format = ref.Object.Format()
	}
	return set
}

// Lookup returns the ref with the given full name.
func (r RemoteRefs) Lookup(name string) (RemoteRef, bool) {
	ref, found := r.refs[name]
	return ref, found
}

// All yields every ref in name order.
func (r RemoteRefs) All() iter.Seq[RemoteRef] {
	return func(yield func(RemoteRef) bool) {
		for _, name := range slices.Sorted(maps.Keys(r.refs)) {
			if !yield(r.refs[name]) {
				return
			}
		}
	}
}

// Tags yields every tag in name order.
func (r RemoteRefs) Tags() iter.Seq[RemoteRef] {
	return func(yield func(RemoteRef) bool) {
		for ref := range r.All() {
			if ref.IsTag() && !yield(ref) {
				return
			}
		}
	}
}

// Format returns the object format the remote uses.
func (r RemoteRefs) Format() ObjectFormat { return r.format }

// ListRemote lists the branches, tags and HEAD of a remote repository
// without fetching any objects.
func (g *Git) ListRemote(ctx context.Context, workDir, remote string) (RemoteRefs, error) {
	output, err := g.run(ctx, "", workDir,
		"ls-remote", "--symref", remote, "HEAD", BranchPrefix+"*", TagPrefix+"*")
	if err != nil {
		return RemoteRefs{}, fmt.Errorf("list remote refs: %w", err)
	}

	refs, err := parseRemoteRefs(output)
	if err != nil {
		return RemoteRefs{}, fmt.Errorf("parse remote refs: %w", err)
	}
	if len(refs.refs) == 0 {
		return RemoteRefs{}, ErrEmptyRemote
	}

	return refs, nil
}

// parseRemoteRefs reads "ls-remote --symref" output. It keeps branches and
// tags, attaches peeled "^{}" lines to their tags and records the symbolic
// target of HEAD.
func parseRemoteRefs(output []byte) (RemoteRefs, error) {
	refs := RemoteRefs{refs: map[string]RemoteRef{}}
	peeled := map[string]Hash{}

	scanner := bufio.NewScanner(bytes.NewReader(output))
	scanner.Buffer(make([]byte, 0, 64<<10), 1<<20)
	for scanner.Scan() {
		line, err := parseRemoteLine(scanner.Text())
		if err != nil {
			return RemoteRefs{}, err
		}

		switch {
		case line.symref != "":
			if line.name == "HEAD" {
				refs.Head = line.symref
			}
		case line.peeled:
			refs.format = line.object.Format()
			peeled[line.name] = line.object
		default:
			refs.format = line.object.Format()
			if isAcceptedRef(line.name) {
				refs.refs[line.name] = RemoteRef{Name: line.name, Object: line.object}
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return RemoteRefs{}, err
	}

	for name, object := range peeled {
		if ref, exists := refs.refs[name]; exists {
			ref.Peeled = object
			refs.refs[name] = ref
		}
	}
	return refs, nil
}

// remoteLine is one parsed line of "ls-remote --symref" output.
type remoteLine struct {
	name   string
	object Hash
	symref string
	peeled bool
}

func parseRemoteLine(text string) (remoteLine, error) {
	left, name, found := strings.Cut(text, "\t")
	if !found {
		return remoteLine{}, fmt.Errorf("malformed line %q", text)
	}

	if target, isSymref := strings.CutPrefix(left, "ref: "); isSymref {
		return remoteLine{name: name, symref: target}, nil
	}

	object, err := ParseHash(left)
	if err != nil {
		return remoteLine{}, err
	}
	base, isPeeled := strings.CutSuffix(name, "^{}")
	return remoteLine{name: base, object: object, peeled: isPeeled}, nil
}

// isAcceptedRef keeps branches and tags whose short names pass
// [CheckRefName], the same rule that lockfiles are read with; anything else
// a remote advertises is ignored rather than trusted.
func isAcceptedRef(name string) bool {
	short, isBranch := strings.CutPrefix(name, BranchPrefix)
	if !isBranch {
		var isTag bool
		if short, isTag = strings.CutPrefix(name, TagPrefix); !isTag {
			return false
		}
	}
	return CheckRefName(short) == nil
}
