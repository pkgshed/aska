package semver_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pkgshed/aska/internal/semver"
)

func TestParse(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{input: "v0.0.0", want: "v0.0.0"},
		{input: "v1.2.3", want: "v1.2.3"},
		{input: "v1.2.3-rc.1", want: "v1.2.3-rc.1"},
		{input: "v1.2.3-alpha-1.0a", want: "v1.2.3-alpha-1.0a"},
		{input: "v1.2.3+build.5", want: "v1.2.3+build.5"},
		{input: "v1.2.3-rc.1+build.007", want: "v1.2.3-rc.1+build.007"},
	}

	for _, test := range tests {
		t.Run(test.input, func(t *testing.T) {
			// Act
			version, err := semver.Parse(test.input)

			// Assert
			require.NoError(t, err)
			assert.Equal(t, test.want, version.String())
			assert.True(t, version.IsValid())
		})
	}
}

func TestParseRejectsNonCanonical(t *testing.T) {
	inputs := []string{
		"", "1.2.3", "v1.2", "v1", "v01.2.3", "v1.02.3", "v1.2.03",
		"v1.2.3-", "v1.2.3-01", "v1.2.3-rc..1", "v1.2.3+", "v1.2.3-rc_1",
		"v1.2.3.4", "va.b.c", "v-1.2.3", "v1.2.3 ", "v99999999999999999999.0.0",
	}

	for _, input := range inputs {
		t.Run(input, func(t *testing.T) {
			// Act
			_, err := semver.Parse(input)

			// Assert
			require.ErrorIs(t, err, semver.ErrInvalidVersion)
		})
	}
}

func TestCompareFollowsSpecificationPrecedence(t *testing.T) {
	// Arrange: the precedence example from semver.org section 11, in order.
	ordered := []string{
		"v1.0.0-alpha", "v1.0.0-alpha.1", "v1.0.0-alpha.beta", "v1.0.0-beta",
		"v1.0.0-beta.2", "v1.0.0-beta.11", "v1.0.0-rc.1", "v1.0.0",
		"v1.0.1", "v1.1.0", "v2.0.0", "v10.0.0",
	}
	versions := make([]semver.Version, len(ordered))
	for index, text := range ordered {
		versions[index] = mustParse(t, text)
	}

	for index := range len(versions) - 1 {
		lower, higher := versions[index], versions[index+1]
		t.Run(lower.String()+"<"+higher.String(), func(t *testing.T) {
			// Act and assert
			assert.Equal(t, -1, semver.Compare(lower, higher))
			assert.Equal(t, 1, semver.Compare(higher, lower))
		})
	}
}

func TestCompareIgnoresBuildMetadata(t *testing.T) {
	// Arrange
	plain := mustParse(t, "v1.2.3")
	withBuild := mustParse(t, "v1.2.3+ci.42")

	// Act
	order := semver.Compare(plain, withBuild)

	// Assert
	assert.Equal(t, 0, order)
}

func TestConstraintAllows(t *testing.T) {
	tests := []struct {
		constraint string
		allowed    []string
		denied     []string
	}{
		{"^1.2.3", []string{"v1.2.3", "v1.9.0"}, []string{"v1.2.2", "v2.0.0"}},
		{"^1.2", []string{"v1.2.0", "v1.99.0"}, []string{"v1.1.9", "v2.0.0"}},
		{"^0.2.3", []string{"v0.2.3", "v0.2.9"}, []string{"v0.3.0", "v0.2.2"}},
		{"^0.0.3", []string{"v0.0.3"}, []string{"v0.0.4", "v0.0.2"}},
		{"^0", []string{"v0.0.0", "v0.9.9"}, []string{"v1.0.0"}},
		{"~1.2.3", []string{"v1.2.3", "v1.2.9"}, []string{"v1.3.0", "v1.2.2"}},
		{"~1.2", []string{"v1.2.0", "v1.2.9"}, []string{"v1.3.0"}},
		{"~1", []string{"v1.0.0", "v1.9.0"}, []string{"v2.0.0"}},
		{"v1", []string{"v1.0.0", "v1.5.2"}, []string{"v2.0.0", "v0.9.0"}},
		{"1.2", []string{"v1.2.0", "v1.2.7"}, []string{"v1.3.0"}},
		{"=1.2.3", []string{"v1.2.3"}, []string{"v1.2.4"}},
		{">1.2", []string{"v1.3.0"}, []string{"v1.2.9"}},
		{">1.2.3", []string{"v1.2.4"}, []string{"v1.2.3"}},
		{"<=1.2", []string{"v1.2.9"}, []string{"v1.3.0"}},
		{"<2", []string{"v1.99.0"}, []string{"v2.0.0"}},
		{">=1.2.0, <2", []string{"v1.2.0", "v1.9.9"}, []string{"v1.1.0", "v2.0.0"}},
		{">= 1.2 < 2", []string{"v1.5.0"}, []string{"v2.1.0"}},
	}

	for _, test := range tests {
		t.Run(test.constraint, func(t *testing.T) {
			// Act
			constraint, err := semver.ParseConstraint(test.constraint)

			// Assert
			require.NoError(t, err)
			for _, text := range test.allowed {
				assert.True(t, constraint.Allows(mustParse(t, text)), "%s should allow %s (%s)",
					test.constraint, text, constraint.Canonical())
			}
			for _, text := range test.denied {
				assert.False(t, constraint.Allows(mustParse(t, text)), "%s should deny %s (%s)",
					test.constraint, text, constraint.Canonical())
			}
		})
	}
}

func TestParseConstraintRejectsMalformedInput(t *testing.T) {
	inputs := []string{"", "^", ">=", "^1.2.3.4", "~x", "1.2-rc", "^01.2", ">= <2"}

	for _, input := range inputs {
		t.Run(input, func(t *testing.T) {
			// Act
			_, err := semver.ParseConstraint(input)

			// Assert
			require.ErrorIs(t, err, semver.ErrInvalidConstraint)
		})
	}
}

func mustParse(t *testing.T, text string) semver.Version {
	t.Helper()
	version, err := semver.Parse(text)
	require.NoError(t, err)
	return version
}
