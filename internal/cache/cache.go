// Package cache stores immutable, content-addressed skill snapshots.
//
// Each snapshot lives at "<root>/<package key>/<digest>" and holds exactly
// the files of one skill at one content digest. Snapshots are built in a
// staging directory and renamed into place, so a reader never observes a
// partial snapshot, and concurrent writers of the same digest are harmless:
// the first rename wins and later ones discard their copy.
package cache

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/pkgshed/aska/internal/digest"
)

// stagingDir is the directory below the root that holds snapshots under
// construction. It cannot collide with a package key, which never starts
// with a dot.
const stagingDir = ".staging"

// ErrCorruptSnapshot reports a cached snapshot whose contents no longer
// match its digest.
var ErrCorruptSnapshot = errors.New("corrupt snapshot")

// Store is a snapshot cache rooted at a directory.
type Store struct {
	Root string
}

// Dir returns where the snapshot of a package at a digest is stored.
func (s *Store) Dir(packageKey string, sum digest.Digest) string {
	return filepath.Join(s.Root, packageKey, sum.String())
}

// Lookup returns the directory of a cached snapshot after verifying that
// its contents still match the digest. It reports false when the snapshot
// is absent. A corrupt snapshot is removed so that it can be rebuilt.
func (s *Store) Lookup(packageKey string, sum digest.Digest) (string, bool, error) {
	dir := s.Dir(packageKey, sum)
	if _, err := os.Lstat(dir); errors.Is(err, fs.ErrNotExist) {
		return "", false, nil
	} else if err != nil {
		return "", false, fmt.Errorf("inspect snapshot: %w", err)
	}

	actual, err := digest.OfDir(dir)
	if err == nil && actual == sum {
		return dir, true, nil
	}

	if removeErr := os.RemoveAll(dir); removeErr != nil {
		return "", false, fmt.Errorf("%w %s: %w", ErrCorruptSnapshot, dir, removeErr)
	}
	return "", false, nil
}

// Contains reports whether a snapshot with the digest exists for the
// package, without verifying its contents.
func (s *Store) Contains(packageKey string, sum digest.Digest) bool {
	info, err := os.Lstat(s.Dir(packageKey, sum))
	return err == nil && info.IsDir()
}

// Stage creates an empty staging directory for a new snapshot.
func (s *Store) Stage() (string, error) {
	staging := filepath.Join(s.Root, stagingDir)
	if err := os.MkdirAll(staging, 0o755); err != nil {
		return "", fmt.Errorf("create staging directory: %w", err)
	}

	dir, err := os.MkdirTemp(staging, "snapshot-*")
	if err != nil {
		return "", fmt.Errorf("create staging directory: %w", err)
	}
	return dir, nil
}

// Commit moves a staged snapshot into place and returns its final
// directory. When another writer committed the same digest first, the
// staged copy is discarded in favour of the existing snapshot.
func (s *Store) Commit(staged, packageKey string, sum digest.Digest) (string, error) {
	final := s.Dir(packageKey, sum)
	if err := os.MkdirAll(filepath.Dir(final), 0o755); err != nil {
		return "", fmt.Errorf("create package directory: %w", err)
	}

	err := os.Rename(staged, final)
	if err == nil {
		return final, nil
	}

	if !s.Contains(packageKey, sum) {
		return "", fmt.Errorf("commit snapshot: %w", err)
	}
	if err := os.RemoveAll(staged); err != nil {
		return "", fmt.Errorf("discard duplicate snapshot: %w", err)
	}
	return final, nil
}

// PruneStaging removes staging directories that were last modified before
// cutoff. Builds take seconds, so a directory that old belongs to an
// interrupted process; a recent one may belong to a concurrent build and is
// left alone.
func (s *Store) PruneStaging(cutoff time.Time) error {
	staging := filepath.Join(s.Root, stagingDir)
	entries, err := os.ReadDir(staging)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	} else if err != nil {
		return fmt.Errorf("list staging directory: %w", err)
	}

	var errs []error
	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil || !info.ModTime().Before(cutoff) {
			continue
		}
		errs = append(errs, os.RemoveAll(filepath.Join(staging, entry.Name())))
	}

	if err := errors.Join(errs...); err != nil {
		return fmt.Errorf("prune staging directory: %w", err)
	}
	return nil
}
