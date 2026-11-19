// Package netguard builds HTTP clients for talking to untrusted hosts.
//
// The clients refuse to connect to loopback, private, link-local and other
// non-public addresses, which protects against server-side request forgery
// through crafted import paths or redirects. The check runs on the address
// actually dialed, after name resolution, so DNS rebinding cannot bypass it.
// Redirects are limited in number and may not downgrade to plain HTTP.
package netguard

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"syscall"
	"time"
)

// maxRedirects bounds the length of a redirect chain.
const maxRedirects = 5

var (
	// ErrRestrictedAddress reports a connection to a non-public address.
	ErrRestrictedAddress = errors.New("connection to non-public address refused")

	// ErrInsecureRedirect reports a redirect to an insecure scheme.
	ErrInsecureRedirect = errors.New("redirect to insecure scheme refused")

	// ErrTooManyRedirects reports an overlong redirect chain.
	ErrTooManyRedirects = errors.New("too many redirects")
)

// Policy relaxes the default restrictions.
type Policy struct {
	// AllowPrivateNetworks permits connections to non-public addresses,
	// for example to reach a self-hosted forge on a local network.
	AllowPrivateNetworks bool
	// AllowInsecure permits plain HTTP.
	AllowInsecure bool
}

// NewClient returns a client that enforces policy on every connection and
// redirect. It connects directly and ignores proxy environment variables,
// because a proxy would hide the destination address from the check.
func NewClient(policy Policy, timeout time.Duration) *http.Client {
	dialer := &net.Dialer{Timeout: 10 * time.Second, Control: policy.control}
	transport := &http.Transport{
		DialContext:            dialer.DialContext,
		ForceAttemptHTTP2:      true,
		TLSHandshakeTimeout:    10 * time.Second,
		ResponseHeaderTimeout:  timeout,
		MaxResponseHeaderBytes: 64 << 10,
	}
	return Harden(&http.Client{Transport: transport}, policy, timeout)
}

// Harden returns a copy of client with the redirect policy and timeout
// applied. It leaves the transport alone, which lets tests substitute an
// in-memory one.
func Harden(client *http.Client, policy Policy, timeout time.Duration) *http.Client {
	hardened := *client
	hardened.Timeout = timeout
	hardened.CheckRedirect = policy.checkRedirect
	return &hardened
}

func (p Policy) checkRedirect(request *http.Request, via []*http.Request) error {
	if len(via) >= maxRedirects {
		return ErrTooManyRedirects
	}

	scheme := request.URL.Scheme
	isPermitted := scheme == "https" || (p.AllowInsecure && scheme == "http")
	if !isPermitted {
		return fmt.Errorf("%w: %s", ErrInsecureRedirect, request.URL.Redacted())
	}
	return nil
}

func (p Policy) control(_, address string, _ syscall.RawConn) error {
	if p.AllowPrivateNetworks {
		return nil
	}

	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return fmt.Errorf("parse dial address %q: %w", address, err)
	}
	addr, err := netip.ParseAddr(host)
	if err != nil {
		return fmt.Errorf("parse dial address %q: %w", address, err)
	}

	if IsRestricted(addr) {
		return fmt.Errorf("%w: %s", ErrRestrictedAddress, addr)
	}
	return nil
}

// restrictedPrefixes lists special-purpose ranges that the netip predicates
// do not cover, following the IANA special-purpose address registries.
var restrictedPrefixes = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("100::/64"),
	netip.MustParsePrefix("2001:db8::/32"),
	netip.MustParsePrefix("fec0::/10"),
}

// embeddingPrefixes carry an IPv4 address in their low 32 bits, which must
// be checked as well.
var embeddingPrefixes = []netip.Prefix{
	netip.MustParsePrefix("64:ff9b::/96"),
	netip.MustParsePrefix("64:ff9b:1::/48"),
}

// IsRestricted reports whether addr is not a public unicast address.
func IsRestricted(addr netip.Addr) bool {
	addr = addr.Unmap()

	isSpecial := addr.IsLoopback() || addr.IsPrivate() || addr.IsUnspecified() ||
		addr.IsLinkLocalUnicast() || addr.IsLinkLocalMulticast() ||
		addr.IsInterfaceLocalMulticast() || addr.IsMulticast()
	if isSpecial {
		return true
	}

	for _, prefix := range restrictedPrefixes {
		if prefix.Contains(addr) {
			return true
		}
	}

	for _, prefix := range embeddingPrefixes {
		if prefix.Contains(addr) {
			bytes := addr.As16()
			embedded := netip.AddrFrom4([4]byte(bytes[12:16]))
			return IsRestricted(embedded)
		}
	}

	return false
}
