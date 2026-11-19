package semver

import (
	"errors"
	"fmt"
	"math"
	"strings"
)

// ErrInvalidConstraint reports text that is not a valid version constraint.
var ErrInvalidConstraint = errors.New("invalid version constraint")

type operator int

const (
	opEqual operator = iota
	opGreater
	opGreaterOrEqual
	opLess
	opLessOrEqual
)

func (o operator) String() string {
	switch o {
	case opGreater:
		return ">"
	case opGreaterOrEqual:
		return ">="
	case opLess:
		return "<"
	case opLessOrEqual:
		return "<="
	default:
		return "="
	}
}

type comparator struct {
	op      operator
	version Version
}

func (c comparator) allows(v Version) bool {
	order := Compare(v, c.version)
	switch c.op {
	case opGreater:
		return order > 0
	case opGreaterOrEqual:
		return order >= 0
	case opLess:
		return order < 0
	case opLessOrEqual:
		return order <= 0
	default:
		return order == 0
	}
}

// Constraint is a conjunction of version comparators, such as "^1.2" or
// ">=1.2.0, <2". The zero value allows every version.
type Constraint struct {
	text        string
	comparators []comparator
}

// ParseConstraint parses a constraint expression. Comparators are separated
// by whitespace or commas and must all hold. Each comparator is an optional
// operator followed by a full or partial version, with or without a leading
// "v":
//
//	^1.2.3  >=1.2.3 <2.0.0      ~1.2.3  >=1.2.3 <1.3.0
//	^0.2.3  >=0.2.3 <0.3.0      ~1.2    >=1.2.0 <1.3.0
//	1.2     >=1.2.0 <1.3.0      >1.2    >=1.3.0
//	<=1.2   <1.3.0              =1.2.3  exactly 1.2.3
//
// A partial version without an operator selects every version with that
// prefix, mirroring the "@v1.2" query of the go command.
func ParseConstraint(text string) (Constraint, error) {
	terms, err := splitTerms(text)
	if err != nil {
		return Constraint{}, fmt.Errorf("%w %q: %w", ErrInvalidConstraint, text, err)
	}

	var comparators []comparator
	for _, term := range terms {
		expanded, err := expandTerm(term)
		if err != nil {
			return Constraint{}, fmt.Errorf("%w %q: %w", ErrInvalidConstraint, text, err)
		}
		comparators = append(comparators, expanded...)
	}

	return Constraint{text: strings.TrimSpace(text), comparators: comparators}, nil
}

// Allows reports whether v satisfies every comparator of the constraint.
func (c Constraint) Allows(v Version) bool {
	for _, comparator := range c.comparators {
		if !comparator.allows(v) {
			return false
		}
	}
	return true
}

// String returns the constraint as the user wrote it.
func (c Constraint) String() string { return c.text }

// Canonical returns the expanded comparators, for example ">=v1.2.0 <v2.0.0".
func (c Constraint) Canonical() string {
	parts := make([]string, 0, len(c.comparators))
	for _, comparator := range c.comparators {
		parts = append(parts, comparator.op.String()+comparator.version.String())
	}
	return strings.Join(parts, " ")
}

// splitTerms splits on whitespace and commas and attaches a lone operator
// to the version that follows it, so that ">= 1.2" reads as ">=1.2".
func splitTerms(text string) ([]string, error) {
	fields := strings.FieldsFunc(text, func(r rune) bool {
		return r == ',' || r == ' ' || r == '\t'
	})
	if len(fields) == 0 {
		return nil, errors.New("empty constraint")
	}

	var terms []string
	pending := ""
	for _, field := range fields {
		if isOperatorOnly(field) {
			if pending != "" {
				return nil, fmt.Errorf("operator %q is missing a version", pending)
			}
			pending = field
			continue
		}
		terms = append(terms, pending+field)
		pending = ""
	}
	if pending != "" {
		return nil, fmt.Errorf("operator %q is missing a version", pending)
	}

	return terms, nil
}

func isOperatorOnly(field string) bool {
	switch field {
	case "=", ">", ">=", "<", "<=", "^", "~":
		return true
	default:
		return false
	}
}

// partial is a version whose minor and patch components may be omitted.
type partial struct {
	version    Version
	components int
}

func parsePartial(text string) (partial, error) {
	text = strings.TrimPrefix(text, "v")
	numbers, _, _ := strings.Cut(text, "-")
	numbers, _, _ = strings.Cut(numbers, "+")
	components := strings.Count(numbers, ".") + 1

	switch components {
	case 3:
		version, err := parseCore(text)
		if err != nil {
			return partial{}, err
		}
		return partial{version: version, components: 3}, nil
	case 1, 2:
		if numbers != text {
			return partial{}, fmt.Errorf("partial version %q cannot carry a prerelease or build", text)
		}
		padded := numbers + strings.Repeat(".0", 3-components)
		version, err := parseCore(padded)
		if err != nil {
			return partial{}, err
		}
		return partial{version: version, components: components}, nil
	default:
		return partial{}, fmt.Errorf("version %q has too many components", text)
	}
}

func expandTerm(term string) ([]comparator, error) {
	op, rest := splitOperator(term)
	bound, err := parsePartial(rest)
	if err != nil {
		return nil, err
	}

	switch op {
	case "^":
		return caretRange(bound)
	case "~":
		return tildeRange(bound)
	case ">":
		return greaterThan(bound)
	case ">=":
		return []comparator{{op: opGreaterOrEqual, version: bound.version}}, nil
	case "<":
		return []comparator{{op: opLess, version: bound.version}}, nil
	case "<=":
		return lessOrEqual(bound)
	default:
		return prefixRange(bound)
	}
}

func splitOperator(term string) (string, string) {
	for _, op := range []string{">=", "<=", ">", "<", "=", "^", "~"} {
		if rest, found := strings.CutPrefix(term, op); found {
			return op, rest
		}
	}
	return "", term
}

// caretRange allows changes that do not modify the left-most non-zero
// component of the bound.
func caretRange(bound partial) ([]comparator, error) {
	v := bound.version
	var upper Version
	var err error

	switch {
	case v.major > 0 || bound.components == 1:
		upper, err = bumpMajor(v)
	case v.minor > 0 || bound.components == 2:
		upper, err = bumpMinor(v)
	default:
		upper, err = bumpPatch(v)
	}
	if err != nil {
		return nil, err
	}

	return between(v, upper), nil
}

// tildeRange allows patch-level changes, or minor-level changes when only a
// major version is given.
func tildeRange(bound partial) ([]comparator, error) {
	v := bound.version
	var upper Version
	var err error

	if bound.components == 1 {
		upper, err = bumpMajor(v)
	} else {
		upper, err = bumpMinor(v)
	}
	if err != nil {
		return nil, err
	}

	return between(v, upper), nil
}

// prefixRange matches exactly for full versions and every version sharing
// the given components for partial ones.
func prefixRange(bound partial) ([]comparator, error) {
	v := bound.version
	switch bound.components {
	case 1:
		upper, err := bumpMajor(v)
		if err != nil {
			return nil, err
		}
		return between(v, upper), nil
	case 2:
		upper, err := bumpMinor(v)
		if err != nil {
			return nil, err
		}
		return between(v, upper), nil
	default:
		return []comparator{{op: opEqual, version: v}}, nil
	}
}

func greaterThan(bound partial) ([]comparator, error) {
	v := bound.version
	switch bound.components {
	case 1:
		upper, err := bumpMajor(v)
		if err != nil {
			return nil, err
		}
		return []comparator{{op: opGreaterOrEqual, version: upper}}, nil
	case 2:
		upper, err := bumpMinor(v)
		if err != nil {
			return nil, err
		}
		return []comparator{{op: opGreaterOrEqual, version: upper}}, nil
	default:
		return []comparator{{op: opGreater, version: v}}, nil
	}
}

func lessOrEqual(bound partial) ([]comparator, error) {
	v := bound.version
	switch bound.components {
	case 1:
		upper, err := bumpMajor(v)
		if err != nil {
			return nil, err
		}
		return []comparator{{op: opLess, version: upper}}, nil
	case 2:
		upper, err := bumpMinor(v)
		if err != nil {
			return nil, err
		}
		return []comparator{{op: opLess, version: upper}}, nil
	default:
		return []comparator{{op: opLessOrEqual, version: v}}, nil
	}
}

func between(lower, upper Version) []comparator {
	return []comparator{
		{op: opGreaterOrEqual, version: lower},
		{op: opLess, version: upper},
	}
}

var errComponentOverflow = errors.New("version component too large")

func bumpMajor(v Version) (Version, error) {
	if v.major == math.MaxUint64 {
		return Version{}, errComponentOverflow
	}
	return New(v.major+1, 0, 0), nil
}

func bumpMinor(v Version) (Version, error) {
	if v.minor == math.MaxUint64 {
		return Version{}, errComponentOverflow
	}
	return New(v.major, v.minor+1, 0), nil
}

func bumpPatch(v Version) (Version, error) {
	if v.patch == math.MaxUint64 {
		return Version{}, errComponentOverflow
	}
	return New(v.major, v.minor, v.patch+1), nil
}
