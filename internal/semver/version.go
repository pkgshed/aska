// Package semver parses, compares and constrains semantic versions.
//
// Release tags use the canonical "vMAJOR.MINOR.PATCH[-PRERELEASE][+BUILD]"
// form described by Semantic Versioning 2.0.0, with the leading "v" that
// Go modules require. Constraints accept the familiar caret, tilde and
// comparison operators so that users can express compatible ranges.
package semver

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// ErrInvalidVersion reports text that is not a canonical semantic version.
var ErrInvalidVersion = errors.New("invalid semantic version")

// Version is a parsed semantic version. The zero value represents the
// absence of a version and is distinct from v0.0.0.
type Version struct {
	major      uint64
	minor      uint64
	patch      uint64
	prerelease string
	build      string
	valid      bool
}

// New returns the release version vMAJOR.MINOR.PATCH.
func New(major, minor, patch uint64) Version {
	return Version{major: major, minor: minor, patch: patch, valid: true}
}

// Parse parses a canonical version such as "v1.2.3-rc.1+build.5". The
// leading "v" is required, and all three numeric components must be present.
func Parse(text string) (Version, error) {
	rest, hasPrefix := strings.CutPrefix(text, "v")
	if !hasPrefix {
		return Version{}, fmt.Errorf("%w %q: missing leading v", ErrInvalidVersion, text)
	}

	version, err := parseCore(rest)
	if err != nil {
		return Version{}, fmt.Errorf("%w %q: %w", ErrInvalidVersion, text, err)
	}

	return version, nil
}

// parseCore parses "MAJOR.MINOR.PATCH[-PRERELEASE][+BUILD]" without a prefix.
func parseCore(text string) (Version, error) {
	rest, build, hasBuild := strings.Cut(text, "+")
	if hasBuild {
		if err := checkIdentifiers(build, false); err != nil {
			return Version{}, fmt.Errorf("build metadata: %w", err)
		}
	}

	numbers, prerelease, hasPrerelease := strings.Cut(rest, "-")
	if hasPrerelease {
		if err := checkIdentifiers(prerelease, true); err != nil {
			return Version{}, fmt.Errorf("prerelease: %w", err)
		}
	}

	components := strings.Split(numbers, ".")
	if len(components) != 3 {
		return Version{}, errors.New("want MAJOR.MINOR.PATCH")
	}

	var parsed [3]uint64
	for index, component := range components {
		value, err := parseNumber(component)
		if err != nil {
			return Version{}, err
		}
		parsed[index] = value
	}

	return Version{
		major:      parsed[0],
		minor:      parsed[1],
		patch:      parsed[2],
		prerelease: prerelease,
		build:      build,
		valid:      true,
	}, nil
}

// parseNumber parses a numeric identifier, which must not have leading zeros.
func parseNumber(text string) (uint64, error) {
	if text == "" {
		return 0, errors.New("empty numeric component")
	}
	if len(text) > 1 && text[0] == '0' {
		return 0, fmt.Errorf("numeric component %q has a leading zero", text)
	}
	if !isDigits(text) {
		return 0, fmt.Errorf("numeric component %q is not a number", text)
	}

	value, err := strconv.ParseUint(text, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("numeric component %q: %w", text, err)
	}

	return value, nil
}

// checkIdentifiers validates dot-separated prerelease or build identifiers.
func checkIdentifiers(text string, rejectLeadingZero bool) error {
	for identifier := range strings.SplitSeq(text, ".") {
		if identifier == "" {
			return errors.New("empty identifier")
		}
		if !isAlphanumericOrHyphen(identifier) {
			return fmt.Errorf("identifier %q has characters outside [0-9A-Za-z-]", identifier)
		}

		isNumeric := isDigits(identifier)
		if rejectLeadingZero && isNumeric && len(identifier) > 1 && identifier[0] == '0' {
			return fmt.Errorf("numeric identifier %q has a leading zero", identifier)
		}
	}

	return nil
}

func isDigits(text string) bool {
	if text == "" {
		return false
	}
	for _, character := range []byte(text) {
		if character < '0' || character > '9' {
			return false
		}
	}
	return true
}

func isAlphanumericOrHyphen(text string) bool {
	for _, character := range []byte(text) {
		isLetter := (character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z')
		isDigit := character >= '0' && character <= '9'
		if !isLetter && !isDigit && character != '-' {
			return false
		}
	}
	return true
}

// IsValid reports whether v holds a parsed version rather than the zero value.
func (v Version) IsValid() bool { return v.valid }

// IsPrerelease reports whether v carries prerelease identifiers.
func (v Version) IsPrerelease() bool { return v.prerelease != "" }

// Major returns the major version number.
func (v Version) Major() uint64 { return v.major }

// Minor returns the minor version number.
func (v Version) Minor() uint64 { return v.minor }

// Patch returns the patch version number.
func (v Version) Patch() uint64 { return v.patch }

// String returns the canonical text form, or "" for the zero value.
func (v Version) String() string {
	if !v.valid {
		return ""
	}

	var builder strings.Builder
	fmt.Fprintf(&builder, "v%d.%d.%d", v.major, v.minor, v.patch)
	if v.prerelease != "" {
		builder.WriteString("-" + v.prerelease)
	}
	if v.build != "" {
		builder.WriteString("+" + v.build)
	}
	return builder.String()
}

// Compare returns -1, 0 or +1 depending on the precedence of a and b.
// Build metadata does not take part in precedence, as the specification
// requires, so two versions that differ only in build metadata compare equal.
func Compare(a, b Version) int {
	if order := compareNumbers(a.major, b.major); order != 0 {
		return order
	}
	if order := compareNumbers(a.minor, b.minor); order != 0 {
		return order
	}
	if order := compareNumbers(a.patch, b.patch); order != 0 {
		return order
	}
	return comparePrerelease(a.prerelease, b.prerelease)
}

func compareNumbers(a, b uint64) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	default:
		return 0
	}
}

// comparePrerelease orders prerelease strings. A version without a
// prerelease has higher precedence than one with a prerelease.
func comparePrerelease(a, b string) int {
	switch {
	case a == b:
		return 0
	case a == "":
		return 1
	case b == "":
		return -1
	}

	left := strings.Split(a, ".")
	right := strings.Split(b, ".")
	for index := range min(len(left), len(right)) {
		if order := compareIdentifier(left[index], right[index]); order != 0 {
			return order
		}
	}
	return compareNumbers(uint64(len(left)), uint64(len(right)))
}

// compareIdentifier orders numeric identifiers numerically and below
// alphanumeric identifiers, which compare in ASCII order.
func compareIdentifier(a, b string) int {
	aNumeric := isDigits(a)
	bNumeric := isDigits(b)

	switch {
	case aNumeric && bNumeric:
		if order := compareNumbers(uint64(len(a)), uint64(len(b))); order != 0 {
			return order
		}
		return strings.Compare(a, b)
	case aNumeric:
		return -1
	case bNumeric:
		return 1
	default:
		return strings.Compare(a, b)
	}
}
