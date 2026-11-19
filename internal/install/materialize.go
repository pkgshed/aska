package install

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"

	"github.com/pkgshed/aska/internal/digest"
	"github.com/pkgshed/aska/internal/extract"
	"github.com/pkgshed/aska/internal/resolve"
	"github.com/pkgshed/aska/internal/semver"
	"github.com/pkgshed/aska/internal/skill"
	"github.com/pkgshed/aska/internal/source"
	"github.com/pkgshed/aska/internal/state"
	"github.com/pkgshed/aska/internal/vcs"
)

// snapshot is a verified skill tree in the cache.
type snapshot struct {
	dir      string
	digest   digest.Digest
	metadata skill.Metadata
}

// candidate is a package resolved to a commit and extracted into the cache.
type candidate struct {
	request    Request
	resolution resolve.Resolution
	repo       *vcs.Repo
	snapshot   snapshot
}

// lock returns the lockfile entry that records the candidate under name.
func (c candidate) lock(name string) state.Lock {
	return state.Lock{
		Name:      name,
		Package:   c.request.Package,
		Requested: c.request.Query.String(),
		Version:   c.resolution.Version,
		Tag:       c.resolution.Tag,
		Branch:    c.resolution.Branch,
		Commit:    c.resolution.Commit,
		Digest:    c.snapshot.digest,
	}
}

// prepare resolves a request, fetches the commit and extracts the skill.
func (e *Engine) prepare(ctx context.Context, request Request) (candidate, error) {
	resolution, repo, err := e.resolve(ctx, request)
	if err != nil {
		return candidate{}, fmt.Errorf("resolve %s: %w", request, err)
	}

	snap, err := e.materialize(ctx, request.Package, repo, resolution.Commit)
	if err != nil {
		return candidate{}, fmt.Errorf("extract %s: %w", request, err)
	}

	provenance := Provenance{Package: request.Package, Resolution: resolution, Digest: snap.digest}
	if err := e.checkTrust(ctx, provenance); err != nil {
		return candidate{}, err
	}

	if request.Unversioned && resolution.Version.IsValid() {
		if request.Query, err = caretQuery(resolution.Version); err != nil {
			return candidate{}, err
		}
	}

	e.Logger.DebugContext(ctx, "resolved",
		slog.String("package", request.Package.String()),
		slog.String("requested", request.Query.String()),
		slog.String("resolved", resolution.Describe()),
		slog.String("commit", resolution.Commit.Short()),
	)
	return candidate{request: request, resolution: resolution, repo: repo, snapshot: snap}, nil
}

// caretQuery returns "^MAJOR.MINOR.PATCH" for a release version.
func caretQuery(version semver.Version) (resolve.Query, error) {
	text := fmt.Sprintf("^%d.%d.%d", version.Major(), version.Minor(), version.Patch())
	constraint, err := semver.ParseConstraint(text)
	if err != nil {
		return nil, err
	}
	return resolve.Range{Constraint: constraint}, nil
}

// resolve turns the query into a commit that is present in the local
// repository.
func (e *Engine) resolve(ctx context.Context, request Request) (resolve.Resolution, *vcs.Repo, error) {
	remote := request.Package.Repo
	refs, err := e.Repos.ListRemote(ctx, remote.URL())
	if err != nil {
		return resolve.Resolution{}, nil, err
	}

	resolution, err := resolve.Resolve(refs, request.Package.Path, request.Query)
	if err != nil {
		return resolve.Resolution{}, nil, err
	}

	repo, err := e.Repos.Open(ctx, remote.CacheKey(), remote.URL(), refs.Format())
	if err != nil {
		return resolve.Resolution{}, nil, err
	}

	if resolution.CommitPrefix != "" {
		if err := repo.FetchHistory(ctx); err != nil {
			return resolve.Resolution{}, nil, err
		}
		if resolution.Commit, err = repo.ResolveRevision(ctx, resolution.CommitPrefix); err != nil {
			return resolve.Resolution{}, nil, err
		}
	}

	if err := repo.Fetch(ctx, resolution.Commit, resolution.FetchRef()); err != nil {
		return resolve.Resolution{}, nil, err
	}
	return resolution, repo, nil
}

// materialize extracts the skill directory of a commit into the cache.
// Invalid skills are discarded before they reach the cache.
func (e *Engine) materialize(ctx context.Context, pkg source.Package, repo *vcs.Repo, commit vcs.Hash) (snapshot, error) {
	tree, err := repo.Tree(ctx, commit, pkg.Path.String())
	if err != nil {
		return snapshot{}, err
	}
	entries := toEntries(tree)
	if err := extract.Check(entries, e.Limits); err != nil {
		return snapshot{}, err
	}

	staged, err := e.Cache.Stage()
	if err != nil {
		return snapshot{}, err
	}
	defer os.RemoveAll(staged)

	sum, err := extract.Write(ctx, staged, entries, repo, e.Limits)
	if err != nil {
		return snapshot{}, err
	}

	metadata, err := readMetadata(staged)
	if err != nil {
		return snapshot{}, err
	}

	dir, err := e.Cache.Commit(staged, pkg.CacheKey(), sum)
	if err != nil {
		return snapshot{}, err
	}
	return snapshot{dir: dir, digest: sum, metadata: metadata}, nil
}

// toEntries classifies tree entries by their git file mode.
func toEntries(tree []vcs.TreeEntry) []extract.Entry {
	entries := make([]extract.Entry, 0, len(tree))
	for _, item := range tree {
		entries = append(entries, extract.Entry{
			Path: item.Path,
			Kind: kindOf(item.Mode),
			Size: max(item.Size, 0),
			ID:   item.Object.String(),
		})
	}
	return entries
}

func kindOf(mode string) extract.Kind {
	switch mode {
	case "100644", "100664":
		return extract.KindFile
	case "100755":
		return extract.KindExecutable
	case "120000":
		return extract.KindSymlink
	case "160000":
		return extract.KindSubmodule
	default:
		return extract.KindUnsupported
	}
}

func readMetadata(dir string) (skill.Metadata, error) {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return skill.Metadata{}, fmt.Errorf("open snapshot: %w", err)
	}
	defer root.Close()

	content, err := root.ReadFile(skill.ManifestName)
	if err != nil {
		return skill.Metadata{}, fmt.Errorf("read %s: %w", skill.ManifestName, err)
	}
	return skill.Parse(content)
}

// lockedSnapshot returns the cached snapshot of a lock, fetching and
// extracting the locked commit when the cache lacks it. The content must
// match the locked digest exactly.
func (e *Engine) lockedSnapshot(ctx context.Context, lock state.Lock) (snapshot, error) {
	key := lock.Package.CacheKey()
	if dir, found, err := e.Cache.Lookup(key, lock.Digest); err != nil {
		return snapshot{}, err
	} else if found {
		metadata, err := readMetadata(dir)
		return snapshot{dir: dir, digest: lock.Digest, metadata: metadata}, err
	}

	repo, err := e.fetchLocked(ctx, lock)
	if err != nil {
		return snapshot{}, err
	}

	snap, err := e.materialize(ctx, lock.Package, repo, lock.Commit)
	if err != nil {
		return snapshot{}, err
	}
	if snap.digest != lock.Digest {
		return snapshot{}, fmt.Errorf("%w for %s at %s: locked %s, got %s",
			ErrDigestMismatch, lock.Name, lock.Commit.Short(), lock.Digest.Short(), snap.digest.Short())
	}
	return snap, nil
}

// fetchLocked makes the locked commit available. A locked tag is fetched by
// name first, which detects an upstream tag that moved since locking; the
// locked commit is then fetched by id, so the installation still
// reproduces exactly what was locked.
func (e *Engine) fetchLocked(ctx context.Context, lock state.Lock) (*vcs.Repo, error) {
	remote := lock.Package.Repo
	repo, err := e.Repos.Open(ctx, remote.CacheKey(), remote.URL(), lock.Commit.Format())
	if err != nil {
		return nil, err
	}

	if lock.Tag == "" {
		return repo, repo.Fetch(ctx, lock.Commit, "")
	}

	err = repo.Fetch(ctx, lock.Commit, vcs.TagPrefix+lock.Tag)
	if moved, isMoved := errors.AsType[*vcs.RefMovedError](err); isMoved {
		e.Logger.WarnContext(ctx, "upstream tag moved; installing the locked commit",
			slog.String("skill", lock.Name),
			slog.String("tag", lock.Tag),
			slog.String("locked", moved.Want.Short()),
			slog.String("upstream", moved.Got.Short()),
		)
		err = repo.Fetch(ctx, lock.Commit, "")
	}
	return repo, err
}

// installName decides the directory name of a skill: the frontmatter name,
// or for skills without one the name of the source directory.
func installName(pkg source.Package, metadata skill.Metadata) (string, error) {
	name := metadata.Name
	if name == "" {
		name = pkg.Path.Base()
	}
	if name == "" {
		return "", fmt.Errorf("%w: %s has no frontmatter name and lies at the repository root",
			skill.ErrInvalidName, pkg)
	}
	return name, skill.ValidateName(name)
}
