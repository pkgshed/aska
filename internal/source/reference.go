package source

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
)

// ErrInvalidImportPath reports text that is not a valid import path.
var ErrInvalidImportPath = errors.New("invalid import path")

// ImportPath is a go-style package path such as
// "github.com/owner/repo/skills/code-review". The first element is a host
// name; the rest is split into repository root and skill path by discovery.
type ImportPath string

// ParseImportPath validates the syntax of an import path. Like the go
// command, it requires a dotted host name as the first element, but it also
// accepts elements that start with a dot so that skills stored below
// ".claude/skills" or ".agents/skills" can be addressed.
func ParseImportPath(text string) (ImportPath, error) {
	host, rest, _ := strings.Cut(text, "/")
	if err := checkHostname(host); err != nil {
		return "", fmt.Errorf("%w %q: %w", ErrInvalidImportPath, text, err)
	}
	if !strings.Contains(host, ".") {
		return "", fmt.Errorf("%w %q: the first element must be a dotted host name", ErrInvalidImportPath, text)
	}
	if rest == "" {
		return "", fmt.Errorf("%w %q: missing repository path", ErrInvalidImportPath, text)
	}
	if _, err := ParsePath(rest); err != nil {
		return "", fmt.Errorf("%w %q: %w", ErrInvalidImportPath, text, err)
	}
	return ImportPath(text), nil
}

// Host returns the first element.
func (p ImportPath) Host() string {
	host, _, _ := strings.Cut(string(p), "/")
	return host
}

// Elements returns the elements after the host.
func (p ImportPath) Elements() []string {
	_, rest, _ := strings.Cut(string(p), "/")
	return strings.Split(rest, "/")
}

// HasPathPrefix reports whether prefix equals p or names one of its parent
// directories.
func (p ImportPath) HasPathPrefix(prefix string) bool {
	return string(p) == prefix || strings.HasPrefix(string(p), prefix+"/")
}

// String returns the import path text.
func (p ImportPath) String() string { return string(p) }

// Reference is a package reference as typed on the command line, split into
// its location and the optional version query after "@".
type Reference struct {
	// ImportPath is set for go-style references and empty for URL ones.
	ImportPath ImportPath
	// Package is set for URL references; go-style references are resolved
	// into a package by discovery.
	Package Package
	// Query is the text after "@", or "" when none was given.
	Query string
}

// IsResolved reports whether the reference already names a package.
func (r Reference) IsResolved() bool { return !r.Package.Repo.IsZero() }

// String returns the reference as the user wrote it, without the query.
func (r Reference) String() string {
	if r.IsResolved() {
		return r.Package.String()
	}
	return r.ImportPath.String()
}

// ParseReference parses a command line reference in one of two forms:
//
//	github.com/owner/repo/skills/code-review@^1.2
//	git+https://example.com/repo.git//skills/code-review@v1.2.3
//
// The URL form names the repository explicitly and separates the skill path
// with "//"; it is useful for ssh remotes and hosts without discovery.
func ParseReference(text string, policy Policy) (Reference, error) {
	if strings.HasPrefix(text, sourcePrefix) {
		return parseURLReference(text, policy)
	}

	location, query, _ := strings.Cut(text, "@")
	importPath, err := ParseImportPath(location)
	if err != nil {
		return Reference{}, err
	}
	return Reference{ImportPath: importPath, Query: query}, nil
}

func parseURLReference(text string, policy Policy) (Reference, error) {
	parsed, err := url.Parse(strings.TrimPrefix(text, sourcePrefix))
	if err != nil {
		return Reference{}, fmt.Errorf("%w %q: %w", ErrInvalidRepo, text, err)
	}

	repoPath, skillText, query := splitURLPath(parsed.Path)
	parsed.Path, parsed.RawPath = repoPath, ""

	repo, err := ParseRepoURL(parsed.String(), policy)
	if err != nil {
		return Reference{}, err
	}

	skillPath, err := ParsePath(skillText)
	if err != nil {
		return Reference{}, err
	}

	return Reference{Package: Package{Repo: repo, Path: skillPath}, Query: query}, nil
}

// splitURLPath splits "/repo.git//skills/x@v1" into the repository path,
// the skill path and the query. Without a "//" separator there is no skill
// path and the query follows the last "@".
func splitURLPath(urlPath string) (repoPath, skillPath, query string) {
	if repoPath, rest, found := strings.Cut(urlPath, "//"); found {
		skillPath, query, _ = strings.Cut(rest, "@")
		return repoPath, skillPath, query
	}
	if index := strings.LastIndex(urlPath, "@"); index >= 0 {
		return urlPath[:index], "", urlPath[index+1:]
	}
	return urlPath, "", ""
}
