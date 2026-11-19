package cache_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pkgshed/aska/internal/cache"
	"github.com/pkgshed/aska/internal/digest"
)

const packageKey = "example.test+skills.git@skills+review"

// stageSnapshot writes a one-file snapshot into a new staging directory.
func stageSnapshot(t *testing.T, store *cache.Store, content string) (string, digest.Digest) {
	t.Helper()
	staged, err := store.Stage()
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(staged, "SKILL.md"), []byte(content), 0o644))
	sum, err := digest.OfDir(staged)
	require.NoError(t, err)
	return staged, sum
}

func TestCommitAndLookup(t *testing.T) {
	// Arrange
	store := &cache.Store{Root: t.TempDir()}
	staged, sum := stageSnapshot(t, store, "v1")

	// Act
	dir, commitErr := store.Commit(staged, packageKey, sum)
	found, ok, lookupErr := store.Lookup(packageKey, sum)

	// Assert
	require.NoError(t, commitErr)
	require.NoError(t, lookupErr)
	assert.True(t, ok)
	assert.Equal(t, dir, found)
	assert.Equal(t, filepath.Join(store.Root, packageKey, sum.String()), dir)
	assert.NoDirExists(t, staged)
}

func TestCommitKeepsFirstOfConcurrentDuplicates(t *testing.T) {
	// Arrange
	store := &cache.Store{Root: t.TempDir()}
	first, sum := stageSnapshot(t, store, "same")
	second, _ := stageSnapshot(t, store, "same")
	_, err := store.Commit(first, packageKey, sum)
	require.NoError(t, err)

	// Act
	dir, err := store.Commit(second, packageKey, sum)

	// Assert
	require.NoError(t, err)
	assert.DirExists(t, dir)
	assert.NoDirExists(t, second, "the duplicate is discarded")
}

func TestLookupDiscardsCorruptSnapshots(t *testing.T) {
	// Arrange
	store := &cache.Store{Root: t.TempDir()}
	staged, sum := stageSnapshot(t, store, "original")
	dir, err := store.Commit(staged, packageKey, sum)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("tampered"), 0o644))

	// Act
	_, ok, err := store.Lookup(packageKey, sum)

	// Assert
	require.NoError(t, err)
	assert.False(t, ok)
	assert.NoDirExists(t, dir)
}

func TestLookupReportsAbsentSnapshots(t *testing.T) {
	// Arrange
	store := &cache.Store{Root: t.TempDir()}
	_, sum := stageSnapshot(t, store, "never committed")

	// Act
	_, ok, err := store.Lookup(packageKey, sum)

	// Assert
	require.NoError(t, err)
	assert.False(t, ok)
	assert.False(t, store.Contains(packageKey, sum))
}

func TestPruneStagingKeepsRecentWork(t *testing.T) {
	// Arrange
	store := &cache.Store{Root: t.TempDir()}
	abandoned, _ := stageSnapshot(t, store, "old")
	recent, _ := stageSnapshot(t, store, "new")
	past := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	require.NoError(t, os.Chtimes(abandoned, past, past))

	// Act
	err := store.PruneStaging(time.Date(2021, 1, 1, 0, 0, 0, 0, time.UTC))

	// Assert
	require.NoError(t, err)
	assert.NoDirExists(t, abandoned)
	assert.DirExists(t, recent)
}
