package source

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"path"
	"strings"
)

// ErrInvalidPath reports a skill path that cannot be used.
var ErrInvalidPath = errors.New("invalid skill path")

// RootPath is the skill path of a skill at the repository root.
const RootPath Path = "."

// maxPackageKeyLength keeps package cache directory names well below the
// 255-byte limit of common filesystems.
const maxPackageKeyLength = 160

// Path is the slash-separated location of a skill directory inside a
// repository, or "." for the repository root.
type Path string

// ParsePath validates a skill path. Elements consist of ASCII letters,
// digits and "-._~", may start with a dot, as in ".claude/skills/x", and
// must not be "." or "..".
func ParsePath(text string) (Path, error) {
	if text == "" || text == "." {
		return RootPath, nil
	}
	if path.Clean(text) != text || strings.HasPrefix(text, "/") {
		return "", fmt.Errorf("%w %q: not a clean relative path", ErrInvalidPath, text)
	}

	for element := range strings.SplitSeq(text, "/") {
		if err := checkPathElement(element); err != nil {
			return "", fmt.Errorf("%w %q: %w", ErrInvalidPath, text, err)
		}
	}
	return Path(text), nil
}

func checkPathElement(element string) error {
	switch {
	case element == "." || element == "..":
		return fmt.Errorf("%q is not allowed", element)
	case strings.EqualFold(element, ".git"):
		return fmt.Errorf("%q is reserved", element)
	}

	for _, character := range []byte(element) {
		if !isPathCharacter(character) {
			return fmt.Errorf("element %q contains %q", element, character)
		}
	}
	return nil
}

func isPathCharacter(character byte) bool {
	isLetter := (character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z')
	isDigit := character >= '0' && character <= '9'
	return isLetter || isDigit || strings.IndexByte("-._~", character) >= 0
}

// IsRoot reports whether the path is the repository root.
func (p Path) IsRoot() bool { return p == RootPath }

// Base returns the last element of the path, or "" for the root.
func (p Path) Base() string {
	if p.IsRoot() {
		return ""
	}
	return path.Base(string(p))
}

// TagPrefix returns the prefix of per-skill version tags, such as
// "skills/code-review/", or "" for the root.
func (p Path) TagPrefix() string {
	if p.IsRoot() {
		return ""
	}
	return string(p) + "/"
}

// String returns the path text.
func (p Path) String() string { return string(p) }

// Package identifies a skill by its repository and its path inside it.
type Package struct {
	Repo Repo
	Path Path
}

// ID returns the stable textual identity "git+<url>//<path>".
func (p Package) ID() string {
	return p.Repo.String() + "//" + string(p.Path)
}

// String returns a readable name, such as
// "github.com/owner/repo/skills/code-review".
func (p Package) String() string {
	name := p.Repo.Host() + "/" + p.Repo.RepoPath()
	if isWellKnownHost(p.Repo.Host()) {
		name = strings.TrimSuffix(name, ".git")
	}
	if p.Path.IsRoot() {
		return name
	}
	return name + "/" + string(p.Path)
}

// CacheKey returns a single directory name that identifies the package in
// the snapshot cache, for example
// "github.com+owner+repo.git@skills+code-review". Overlong keys keep a
// readable prefix and end in a hash of the full key.
func (p Package) CacheKey() string {
	repoKey := strings.ReplaceAll(p.Repo.CacheKey(), "/", "+")

	var skillElements []string
	if !p.Path.IsRoot() {
		for element := range strings.SplitSeq(string(p.Path), "/") {
			skillElements = append(skillElements, escapeElement(element))
		}
	}

	key := repoKey + "@" + strings.Join(skillElements, "+")
	if len(key) <= maxPackageKeyLength {
		return key
	}

	sum := sha256.Sum256([]byte(key))
	return key[:maxPackageKeyLength-17] + "~" + hex.EncodeToString(sum[:8])
}
