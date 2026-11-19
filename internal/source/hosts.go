package source

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
)

// wellKnownHosts host repositories at exactly "<host>/<owner>/<repo>", so
// the repository root of an import path is known without asking the host.
var wellKnownHosts = []string{"github.com", "bitbucket.org", "codeberg.org"}

func isWellKnownHost(host string) bool { return slices.Contains(wellKnownHosts, host) }

var (
	ownerPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]*$`)
	repoPattern  = regexp.MustCompile(`^[A-Za-z0-9_.-]+$`)
)

// StaticRoot splits an import path into repository and skill path using
// rules that need no network access, mirroring the go command:
//
//   - On github.com, bitbucket.org and codeberg.org the repository is the
//     first two elements after the host, for example
//     "github.com/owner/repo/skills/x" is https://github.com/owner/repo.git
//     with the skill path "skills/x".
//   - On any host, the first element ending in ".git" ends the repository
//     path, for example "example.com/group/repo.git/skills/x" is
//     https://example.com/group/repo.git with the skill path "skills/x".
//
// It reports false when neither rule applies and discovery must ask the
// host for an aska-import meta tag.
func StaticRoot(importPath ImportPath) (Package, bool, error) {
	if isWellKnownHost(importPath.Host()) {
		pkg, err := wellKnownRoot(importPath)
		return pkg, true, err
	}

	elements := importPath.Elements()
	for index, element := range elements {
		if len(element) > len(".git") && strings.HasSuffix(element, ".git") {
			pkg, err := buildPackage(importPath.Host(), elements[:index+1], elements[index+1:])
			return pkg, true, err
		}
	}

	return Package{}, false, nil
}

func wellKnownRoot(importPath ImportPath) (Package, error) {
	elements := importPath.Elements()
	if len(elements) < 2 {
		return Package{}, fmt.Errorf("%w %q: want %s/<owner>/<repo>[/<skill path>]",
			ErrInvalidImportPath, importPath, importPath.Host())
	}

	owner, repo := elements[0], strings.TrimSuffix(elements[1], ".git")
	if !ownerPattern.MatchString(owner) || !repoPattern.MatchString(repo) {
		return Package{}, fmt.Errorf("%w %q: invalid owner or repository name", ErrInvalidImportPath, importPath)
	}

	return buildPackage(importPath.Host(), []string{owner, repo + ".git"}, elements[2:])
}

func buildPackage(host string, repoElements, skillElements []string) (Package, error) {
	repo, err := ParseRepoURL("https://"+host+"/"+strings.Join(repoElements, "/"), Policy{})
	if err != nil {
		return Package{}, err
	}

	skillPath, err := ParsePath(strings.Join(skillElements, "/"))
	if err != nil {
		return Package{}, err
	}

	return Package{Repo: repo, Path: skillPath}, nil
}
