package vcs

import (
	"errors"
	"strings"
)

// CheckRefName applies the rules of git check-ref-format that matter for
// names typed by users, advertised by remotes or read from lockfiles. Names
// starting with "-" are rejected so that they can never be mistaken for
// options, and control characters are rejected so that a hostile remote
// cannot smuggle terminal escape sequences into output.
func CheckRefName(name string) error {
	switch {
	case name == "" || name == "@":
		return errors.New("empty ref name")
	case strings.HasPrefix(name, "-"), strings.HasPrefix(name, "/"), strings.HasSuffix(name, "/"):
		return errors.New("ref names cannot start with - or / or end with /")
	case strings.HasSuffix(name, ".") || strings.HasSuffix(name, ".lock"):
		return errors.New("ref names cannot end with . or .lock")
	case strings.Contains(name, "..") || strings.Contains(name, "@{") || strings.Contains(name, "//"):
		return errors.New("ref names cannot contain .., @{ or //")
	case strings.ContainsFunc(name, isForbiddenRefRune):
		return errors.New("ref names cannot contain spaces, control characters or any of ~^:?*[\\")
	}

	for component := range strings.SplitSeq(name, "/") {
		if strings.HasPrefix(component, ".") {
			return errors.New("ref name components cannot start with a dot")
		}
	}
	return nil
}

func isForbiddenRefRune(r rune) bool {
	return r <= ' ' || r == 0x7f || strings.ContainsRune("~^:?*[\\", r)
}
