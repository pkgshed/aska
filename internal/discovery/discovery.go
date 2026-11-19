// Package discovery maps import paths to git repositories the way the go
// command does for "go get", with an aska-specific meta tag.
//
// Well-known hosts and paths containing a ".git" element are resolved
// statically. For any other host, discovery fetches
// "https://<import path>?aska-get=1" and reads a tag of the form
//
//	<meta name="aska-import" content="<prefix> git <repository URL> [<subdirectory>]">
//
// where prefix is the import path of the repository root and the optional
// subdirectory locates that root inside the repository. Only the git
// version control system is supported.
package discovery

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strings"

	"github.com/pkgshed/aska/internal/source"
)

// maxResponseBytes bounds how much of a discovery page is read.
const maxResponseBytes = 1 << 20

var (
	// ErrNoMetaTag reports a page without a matching aska-import tag.
	ErrNoMetaTag = errors.New("no matching aska-import meta tag")

	// ErrAmbiguousMetaTag reports several tags matching the same import path.
	ErrAmbiguousMetaTag = errors.New("multiple aska-import meta tags match")

	// ErrMetaTagMismatch reports a repository root whose own page disagrees
	// with the tag that pointed to it.
	ErrMetaTagMismatch = errors.New("aska-import meta tag mismatch")

	// ErrUnsupportedVCS reports a tag for a version control system other
	// than git.
	ErrUnsupportedVCS = errors.New("unsupported version control system")
)

// Resolver turns import paths into packages.
type Resolver struct {
	// Client fetches discovery pages. It should come from netguard.
	Client *http.Client
	// Policy decides which repository transports a tag may name.
	Policy source.Policy
}

// Resolve returns the package an import path refers to.
func (r *Resolver) Resolve(ctx context.Context, importPath source.ImportPath) (source.Package, error) {
	if pkg, matched, err := source.StaticRoot(importPath); matched {
		return pkg, err
	}

	pkg, err := r.resolveDynamic(ctx, importPath)
	if err != nil {
		return source.Package{}, fmt.Errorf("discover %s: %w", importPath, err)
	}
	return pkg, nil
}

func (r *Resolver) resolveDynamic(ctx context.Context, importPath source.ImportPath) (source.Package, error) {
	tag, err := r.lookup(ctx, importPath.String(), importPath)
	if err != nil {
		return source.Package{}, err
	}

	// Like the go command, confirm a tag found below the root by fetching
	// the root's own page, so a page cannot claim a root it does not own.
	if tag.prefix != importPath.String() {
		confirmed, err := r.lookup(ctx, tag.prefix, importPath)
		if err != nil {
			return source.Package{}, fmt.Errorf("confirm repository root %s: %w", tag.prefix, err)
		}
		if confirmed != tag {
			return source.Package{}, fmt.Errorf("%w: %s and %s disagree", ErrMetaTagMismatch, importPath, tag.prefix)
		}
	}

	return r.buildPackage(importPath, tag)
}

// lookup fetches the page for location and returns the single tag whose
// prefix matches importPath.
func (r *Resolver) lookup(ctx context.Context, location string, importPath source.ImportPath) (metaTag, error) {
	body, err := r.fetch(ctx, location)
	if err != nil {
		return metaTag{}, err
	}
	defer body.Close()

	tags, err := parseMetaTags(io.LimitReader(body, maxResponseBytes))
	if err != nil {
		return metaTag{}, fmt.Errorf("parse https://%s: %w", location, err)
	}

	return matchTag(tags, importPath)
}

func (r *Resolver) fetch(ctx context.Context, location string) (io.ReadCloser, error) {
	host, rest, _ := strings.Cut(location, "/")
	pageURL := url.URL{Scheme: "https", Host: host, Path: "/" + rest, RawQuery: "aska-get=1"}

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, pageURL.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}

	response, err := r.Client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("fetch %s: %w", pageURL.String(), err)
	}
	if response.StatusCode != http.StatusOK {
		_ = response.Body.Close()
		return nil, fmt.Errorf("fetch %s: %s", pageURL.String(), response.Status)
	}

	return response.Body, nil
}

func matchTag(tags []metaTag, importPath source.ImportPath) (metaTag, error) {
	var matches []metaTag
	for _, tag := range tags {
		if importPath.HasPathPrefix(tag.prefix) {
			matches = append(matches, tag)
		}
	}

	switch len(matches) {
	case 0:
		return metaTag{}, fmt.Errorf("%w for %s; hosts without discovery support can be addressed "+
			"with the .git form, for example %s/<path>/<repo>.git/<skill path>", ErrNoMetaTag, importPath, importPath.Host())
	case 1:
		return matches[0], nil
	default:
		return metaTag{}, fmt.Errorf("%w %s", ErrAmbiguousMetaTag, importPath)
	}
}

func (r *Resolver) buildPackage(importPath source.ImportPath, tag metaTag) (source.Package, error) {
	if tag.vcs != "git" {
		return source.Package{}, fmt.Errorf("%w %q", ErrUnsupportedVCS, tag.vcs)
	}

	repo, err := source.ParseRepoURL(tag.repoURL, r.Policy)
	if err != nil {
		return source.Package{}, err
	}

	remainder := strings.TrimPrefix(strings.TrimPrefix(importPath.String(), tag.prefix), "/")
	skillPath, err := source.ParsePath(path.Join(tag.subdirectory, remainder))
	if err != nil {
		return source.Package{}, err
	}

	return source.Package{Repo: repo, Path: skillPath}, nil
}
