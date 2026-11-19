// Package source models where skills come from: canonical git repositories,
// skill paths inside them and the package identity formed by the two.
//
// Values of these types are always valid. They are created by parsing
// untrusted text, which rejects anything that could confuse git, escape a
// cache directory or downgrade the transport security.
package source

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"path"
	"strings"
)

// sourcePrefix marks a repository URL in manifests and lockfiles.
const sourcePrefix = "git+"

var (
	// ErrInvalidRepo reports a repository URL that cannot be used.
	ErrInvalidRepo = errors.New("invalid repository URL")

	// ErrInsecureScheme reports a transport that is not allowed by policy.
	ErrInsecureScheme = errors.New("insecure repository scheme")
)

// Policy decides which repository transports are acceptable.
type Policy struct {
	// AllowInsecure permits the unauthenticated http and git transports.
	AllowInsecure bool
}

func (p Policy) allows(scheme string) bool {
	switch scheme {
	case "https", "ssh":
		return true
	case "http", "git":
		return p.AllowInsecure
	default:
		return false
	}
}

// Repo is the canonical URL of a git repository. It holds only strings so
// that two values compare equal exactly when they name the same URL.
type Repo struct {
	scheme string
	user   string
	host   string
	path   string
}

// ParseRepoURL parses and canonicalizes a repository URL. The host is lower
// cased, default ports and trailing slashes are dropped, and the scheme
// must be allowed by policy.
func ParseRepoURL(raw string, policy Policy) (Repo, error) {
	parsed, err := url.Parse(raw)
	if err != nil {
		return Repo{}, fmt.Errorf("%w %q: %w", ErrInvalidRepo, raw, err)
	}

	scheme := strings.ToLower(parsed.Scheme)
	if !policy.allows(scheme) {
		return Repo{}, fmt.Errorf("%w %q: the %q transport is not allowed", ErrInsecureScheme, raw, scheme)
	}

	repo, err := canonicalRepo(parsed)
	if err != nil {
		return Repo{}, fmt.Errorf("%w %q: %w", ErrInvalidRepo, raw, err)
	}
	return repo, nil
}

// ParseSource parses the "git+<url>" form used in manifests and lockfiles.
func ParseSource(text string, policy Policy) (Repo, error) {
	raw, found := strings.CutPrefix(text, sourcePrefix)
	if !found {
		return Repo{}, fmt.Errorf("%w %q: want a %s<url> source", ErrInvalidRepo, text, sourcePrefix)
	}
	return ParseRepoURL(raw, policy)
}

func canonicalRepo(parsed *url.URL) (Repo, error) {
	switch {
	case parsed.Opaque != "":
		return Repo{}, errors.New("opaque URLs are not supported")
	case parsed.RawQuery != "" || parsed.ForceQuery:
		return Repo{}, errors.New("query strings are not supported")
	case parsed.Fragment != "":
		return Repo{}, errors.New("fragments are not supported")
	case parsed.User != nil && hasPassword(parsed.User):
		return Repo{}, errors.New("embedded passwords are not supported")
	}

	host, err := canonicalHost(parsed)
	if err != nil {
		return Repo{}, err
	}

	repoPath, err := canonicalRepoPath(parsed.Path)
	if err != nil {
		return Repo{}, err
	}

	repo := Repo{scheme: strings.ToLower(parsed.Scheme), host: host, path: repoPath}
	if parsed.User != nil {
		repo.user = parsed.User.Username()
	}
	return repo, nil
}

func hasPassword(user *url.Userinfo) bool {
	_, set := user.Password()
	return set
}

var defaultPorts = map[string]string{"https": "443", "http": "80", "ssh": "22", "git": "9418"}

func canonicalHost(parsed *url.URL) (string, error) {
	hostname := strings.ToLower(parsed.Hostname())
	if err := checkHostname(hostname); err != nil {
		return "", err
	}

	port := parsed.Port()
	if port == "" || port == defaultPorts[strings.ToLower(parsed.Scheme)] {
		return hostname, nil
	}
	return net.JoinHostPort(hostname, port), nil
}

// checkHostname accepts DNS names made of letters, digits, hyphens and
// dots. A leading hyphen would be read as an option by ssh.
func checkHostname(hostname string) error {
	if hostname == "" {
		return errors.New("missing host")
	}
	for label := range strings.SplitSeq(hostname, ".") {
		if label == "" || label[0] == '-' || label[len(label)-1] == '-' {
			return fmt.Errorf("invalid host %q", hostname)
		}
		for _, character := range []byte(label) {
			isLetter := character >= 'a' && character <= 'z'
			isDigit := character >= '0' && character <= '9'
			if !isLetter && !isDigit && character != '-' {
				return fmt.Errorf("invalid host %q", hostname)
			}
		}
	}
	return nil
}

func canonicalRepoPath(raw string) (string, error) {
	trimmed := strings.TrimRight(raw, "/")
	if trimmed == "" {
		return "", errors.New("missing repository path")
	}
	if path.Clean(trimmed) != trimmed || !strings.HasPrefix(trimmed, "/") {
		return "", fmt.Errorf("repository path %q is not canonical", raw)
	}
	for element := range strings.SplitSeq(strings.TrimPrefix(trimmed, "/"), "/") {
		if strings.ContainsFunc(element, isUnsafeURLRune) {
			return "", fmt.Errorf("repository path %q contains unsupported characters", raw)
		}
	}
	return trimmed, nil
}

func isUnsafeURLRune(r rune) bool {
	return r < 0x21 || r == 0x7f || r == '\\'
}

// URL returns the URL handed to git.
func (r Repo) URL() string {
	canonical := url.URL{Scheme: r.scheme, Host: r.host, Path: r.path}
	if r.user != "" {
		canonical.User = url.User(r.user)
	}
	return canonical.String()
}

// String returns the "git+<url>" source form.
func (r Repo) String() string { return sourcePrefix + r.URL() }

// Host returns the host name, including a non-default port.
func (r Repo) Host() string { return r.host }

// RepoPath returns the slash-separated path on the host, without a leading
// slash.
func (r Repo) RepoPath() string { return strings.TrimPrefix(r.path, "/") }

// IsZero reports whether r is the zero value.
func (r Repo) IsZero() bool { return r.host == "" }

// CacheKey returns the relative directory that stores the repository, for
// example "github.com/owner/repo.git". Two URLs that differ only in scheme
// or user share a key; their objects are content addressed and identical.
func (r Repo) CacheKey() string {
	elements := []string{escapeElement(strings.ReplaceAll(r.host, ":", "+"))}
	for element := range strings.SplitSeq(r.RepoPath(), "/") {
		elements = append(elements, escapeElement(element))
	}

	last := len(elements) - 1
	if !strings.HasSuffix(elements[last], ".git") {
		elements[last] += ".git"
	}
	return strings.Join(elements, "/")
}

// escapeElement maps a path element to a portable directory name. Lower
// case letters, digits and "-._" pass through, upper case letters become
// "!" plus the lower case letter, as in the go command's module cache, and
// every other byte is percent-encoded. The mapping is injective and safe on
// case-insensitive filesystems.
func escapeElement(element string) string {
	var builder strings.Builder
	for _, character := range []byte(element) {
		switch {
		case character >= 'a' && character <= 'z', character >= '0' && character <= '9',
			character == '-', character == '.', character == '_':
			builder.WriteByte(character)
		case character >= 'A' && character <= 'Z':
			builder.WriteByte('!')
			builder.WriteByte(character + ('a' - 'A'))
		default:
			fmt.Fprintf(&builder, "%%%02X", character)
		}
	}

	escaped := builder.String()
	if escaped == "." || escaped == ".." {
		return strings.ReplaceAll(escaped, ".", "%2E")
	}
	return escaped
}
