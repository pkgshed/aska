// Package vcs maintains bare git repositories and reads objects from them.
//
// The package drives the git command line in the spirit of the go command's
// direct VCS fetching: repositories are bare, shared by every version and
// skill they contain, fetched shallowly whenever possible and never checked
// out. Nothing fetched from a remote is ever executed.
package vcs

import (
	"encoding/hex"
	"errors"
	"fmt"
)

// ErrInvalidHash reports text that is not a full git object id.
var ErrInvalidHash = errors.New("invalid object id")

// ObjectFormat is the hash algorithm of a repository.
type ObjectFormat string

const (
	// FormatUnknown means the format has not been determined yet.
	FormatUnknown ObjectFormat = ""
	// FormatSHA1 is git's default object format.
	FormatSHA1 ObjectFormat = "sha1"
	// FormatSHA256 is git's newer object format.
	FormatSHA256 ObjectFormat = "sha256"
)

// Hash is a full, lowercase hexadecimal git object id.
type Hash string

// ParseHash validates a full SHA-1 or SHA-256 object id.
func ParseHash(text string) (Hash, error) {
	if !IsFullHash(text) {
		return "", fmt.Errorf("%w %q: want 40 or 64 lowercase hex characters", ErrInvalidHash, text)
	}
	return Hash(text), nil
}

// IsFullHash reports whether text is a full, lowercase object id.
func IsFullHash(text string) bool {
	if len(text) != 40 && len(text) != 64 {
		return false
	}
	return isLowerHex(text)
}

// IsHashPrefix reports whether text could abbreviate an object id.
func IsHashPrefix(text string) bool {
	if len(text) < 7 || len(text) > 64 {
		return false
	}
	return isLowerHex(text)
}

func isLowerHex(text string) bool {
	if _, err := hex.DecodeString(padEven(text)); err != nil {
		return false
	}
	for _, character := range []byte(text) {
		if character >= 'A' && character <= 'F' {
			return false
		}
	}
	return true
}

func padEven(text string) string {
	if len(text)%2 == 1 {
		return text + "0"
	}
	return text
}

// Format returns the object format implied by the hash length.
func (h Hash) Format() ObjectFormat {
	switch len(h) {
	case 40:
		return FormatSHA1
	case 64:
		return FormatSHA256
	default:
		return FormatUnknown
	}
}

// Short returns an abbreviated form for display.
func (h Hash) Short() string {
	return string(h)[:min(len(h), 12)]
}

// String returns the full object id.
func (h Hash) String() string { return string(h) }
