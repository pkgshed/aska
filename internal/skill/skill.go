// Package skill reads and validates SKILL.md manifests.
//
// A skill is a directory whose SKILL.md starts with YAML frontmatter that
// describes it. The rules here follow the Agent Skills specification at
// https://agentskills.io/specification. Individual agents apply stricter or
// looser variants of those rules; the agent package encodes the differences.
package skill

import (
	"bytes"
	"errors"
	"fmt"
	"regexp"
	"unicode/utf8"

	"go.yaml.in/yaml/v4"
)

// ManifestName is the file name that marks a directory as a skill.
const ManifestName = "SKILL.md"

// Limits from the Agent Skills specification.
const (
	MaxNameLength          = 64
	MaxDescriptionLength   = 1024
	MaxCompatibilityLength = 500
)

// maxFrontmatterBytes bounds how much YAML is handed to the decoder.
const maxFrontmatterBytes = 64 << 10

var (
	// ErrInvalidManifest reports a SKILL.md that cannot be interpreted.
	ErrInvalidManifest = errors.New("invalid SKILL.md")

	// ErrInvalidName reports a skill name that breaks the naming rules.
	ErrInvalidName = errors.New("invalid skill name")
)

// namePattern matches lowercase alphanumeric words joined by single hyphens.
var namePattern = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

// Metadata is the frontmatter of a SKILL.md file. Fields that the file
// omits are empty. Unknown fields are ignored because agents extend the
// frontmatter with their own keys.
type Metadata struct {
	Name          string
	Description   string
	License       string
	Compatibility string
}

// fields maps frontmatter keys to the metadata fields they fill.
func (m *Metadata) fields() map[string]*string {
	return map[string]*string{
		"name":          &m.Name,
		"description":   &m.Description,
		"license":       &m.License,
		"compatibility": &m.Compatibility,
	}
}

// Parse extracts and decodes the YAML frontmatter of a SKILL.md file.
func Parse(content []byte) (Metadata, error) {
	if !utf8.Valid(content) {
		return Metadata{}, fmt.Errorf("%w: not valid UTF-8", ErrInvalidManifest)
	}

	frontmatter, err := extractFrontmatter(content)
	if err != nil {
		return Metadata{}, fmt.Errorf("%w: %w", ErrInvalidManifest, err)
	}

	var document yaml.Node
	if err := yaml.Load(frontmatter, &document, yaml.WithV4Defaults()); err != nil {
		return Metadata{}, fmt.Errorf("%w: decode frontmatter: %w", ErrInvalidManifest, err)
	}

	metadata, err := decodeMetadata(&document)
	if err != nil {
		return Metadata{}, fmt.Errorf("%w: decode frontmatter: %w", ErrInvalidManifest, err)
	}
	return metadata, nil
}

// decodeMetadata reads the known keys of the frontmatter mapping. Their
// values must be plain strings: YAML would otherwise turn "name: 42" or
// "name: yes" into a number or boolean, which agents reject. Duplicate keys
// are ambiguous across YAML parsers and rejected as well.
func decodeMetadata(document *yaml.Node) (Metadata, error) {
	var metadata Metadata
	if document.Kind == 0 {
		return metadata, nil
	}

	mapping := document
	if document.Kind == yaml.DocumentNode && len(document.Content) == 1 {
		mapping = document.Content[0]
	}
	if mapping.Kind != yaml.MappingNode {
		return Metadata{}, errors.New("frontmatter is not a mapping")
	}

	fields := metadata.fields()
	seen := map[string]bool{}
	for index := 0; index+1 < len(mapping.Content); index += 2 {
		key, value := mapping.Content[index], mapping.Content[index+1]
		if seen[key.Value] {
			return Metadata{}, fmt.Errorf("line %d: duplicate key %q", key.Line, key.Value)
		}
		seen[key.Value] = true

		field, known := fields[key.Value]
		if !known {
			continue
		}
		if value.Kind != yaml.ScalarNode || value.ShortTag() != "!!str" {
			return Metadata{}, fmt.Errorf("line %d: %s must be a string", value.Line, key.Value)
		}
		*field = value.Value
	}
	return metadata, nil
}

// extractFrontmatter returns the YAML between the opening "---" line and
// the next "---" line.
func extractFrontmatter(content []byte) ([]byte, error) {
	content = bytes.TrimPrefix(content, []byte("\uFEFF"))

	first, rest, found := cutLine(content)
	if !found || string(first) != "---" {
		return nil, errors.New("missing frontmatter: the file must start with a --- line")
	}

	var frontmatter []byte
	remaining := rest
	for len(remaining) > 0 {
		var line []byte
		line, remaining, _ = cutLine(remaining)
		if string(line) == "---" {
			return frontmatter, nil
		}

		frontmatter = append(frontmatter, line...)
		frontmatter = append(frontmatter, '\n')
		if len(frontmatter) > maxFrontmatterBytes {
			return nil, fmt.Errorf("frontmatter exceeds %d bytes", maxFrontmatterBytes)
		}
	}

	return nil, errors.New("unterminated frontmatter: missing closing --- line")
}

// cutLine splits off the first line and strips a trailing carriage return.
func cutLine(content []byte) (line, rest []byte, found bool) {
	line, rest, found = bytes.Cut(content, []byte("\n"))
	return bytes.TrimSuffix(line, []byte("\r")), rest, found || len(line) > 0
}

// ValidateName checks a skill name against the portable naming rule: one to
// 64 lowercase letters, digits and single inner hyphens. Names that pass are
// safe to use as directory names on every supported platform.
func ValidateName(name string) error {
	switch {
	case name == "":
		return fmt.Errorf("%w: name is empty", ErrInvalidName)
	case len(name) > MaxNameLength:
		return fmt.Errorf("%w %q: longer than %d characters", ErrInvalidName, name, MaxNameLength)
	case !namePattern.MatchString(name):
		return fmt.Errorf("%w %q: use lowercase letters, digits and single hyphens", ErrInvalidName, name)
	default:
		return nil
	}
}
