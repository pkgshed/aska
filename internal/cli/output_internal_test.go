package cli

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestSanitizeNeutralizesControlCharacters(t *testing.T) {
	tests := map[string]string{
		"plain text":            "plain text",
		"tab\tkept":             "tab\tkept",
		"\x1b[2Jclear":          `\x1b[2Jclear`,
		"title\x1b]0;pwned\x07": `title\x1b]0;pwned\x07`,
		"carriage\rreturn":      `carriage\x0dreturn`,
	}

	for input, want := range tests {
		t.Run(want, func(t *testing.T) {
			// Act
			got := sanitize(input)

			// Assert
			assert.Equal(t, want, got)
		})
	}
}

func TestFormatErrorKeepsLineBreaks(t *testing.T) {
	// Arrange
	err := errors.New("git fetch: exit status 128\nremote: \x1b[31mdenied\x1b[0m")

	// Act
	formatted := FormatError(err)

	// Assert
	assert.Equal(t, "git fetch: exit status 128\nremote: \\x1b[31mdenied\\x1b[0m", formatted)
}

func TestParseSelection(t *testing.T) {
	tests := []struct {
		answer  string
		indices []int
		abort   bool
	}{
		{answer: "\n", indices: []int{0, 1, 2, 3}},
		{answer: "A\n", indices: []int{0, 1, 2, 3}},
		{answer: "none\n", indices: nil},
		{answer: "Ab\n", abort: true},
		{answer: "2\n", indices: []int{1}},
		{answer: "1 3-4\n", indices: []int{0, 2, 3}},
		{answer: "4,1, 1\n", indices: []int{3, 0}},
	}

	for _, test := range tests {
		t.Run(test.answer, func(t *testing.T) {
			// Act
			indices, abort, err := parseSelection(test.answer, 4)

			// Assert
			assert.NoError(t, err)
			assert.Equal(t, test.indices, indices)
			assert.Equal(t, test.abort, abort)
		})
	}
}

func TestParseSelectionRejectsInvalidAnswers(t *testing.T) {
	for _, answer := range []string{"0", "5", "3-2", "x", "1-", "-1", "y"} {
		t.Run(answer, func(t *testing.T) {
			// Act
			_, _, err := parseSelection(answer, 4)

			// Assert
			assert.ErrorIs(t, err, errInvalidSelection)
		})
	}
}

func TestFormatSize(t *testing.T) {
	tests := map[int64]string{0: "0 B", 1023: "1023 B", 1024: "1.0 KiB", 1536: "1.5 KiB", 5 << 20: "5.0 MiB"}

	for size, want := range tests {
		// Act and assert
		assert.Equal(t, want, formatSize(size))
	}
}
