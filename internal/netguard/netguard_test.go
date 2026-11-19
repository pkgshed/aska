package netguard_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pkgshed/aska/internal/netguard"
)

func TestIsRestricted(t *testing.T) {
	tests := map[string]bool{
		"127.0.0.1":            true,
		"10.1.2.3":             true,
		"172.16.0.1":           true,
		"192.168.1.1":          true,
		"169.254.169.254":      true,
		"100.64.0.1":           true,
		"0.0.0.0":              true,
		"255.255.255.255":      true,
		"224.0.0.1":            true,
		"::1":                  true,
		"fe80::1":              true,
		"fd00::1":              true,
		"::ffff:127.0.0.1":     true,
		"64:ff9b::a00:1":       true,
		"2001:db8::1":          true,
		"1.1.1.1":              false,
		"140.82.121.4":         false,
		"2606:4700:4700::1111": false,
		"64:ff9b::101:101":     false,
	}

	for text, want := range tests {
		t.Run(text, func(t *testing.T) {
			// Act
			restricted := netguard.IsRestricted(netip.MustParseAddr(text))

			// Assert
			assert.Equal(t, want, restricted)
		})
	}
}

func TestNewClientRefusesLoopbackServers(t *testing.T) {
	// Arrange: a real server listening on the loopback interface.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(server.Close)
	client := netguard.NewClient(netguard.Policy{AllowInsecure: true}, 5*time.Second)

	request, err := http.NewRequestWithContext(context.Background(), http.MethodGet, server.URL, nil)
	require.NoError(t, err)

	// Act
	response, err := client.Do(request)
	if response != nil {
		response.Body.Close()
	}

	// Assert
	require.ErrorIs(t, err, netguard.ErrRestrictedAddress)
}

func TestNewClientAllowsPrivateNetworksByPolicy(t *testing.T) {
	// Arrange
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	}))
	t.Cleanup(server.Close)
	client := netguard.NewClient(netguard.Policy{AllowPrivateNetworks: true, AllowInsecure: true}, 5*time.Second)

	request, err := http.NewRequestWithContext(context.Background(), http.MethodGet, server.URL, nil)
	require.NoError(t, err)

	// Act
	response, err := client.Do(request)

	// Assert
	require.NoError(t, err)
	defer response.Body.Close()
	assert.Equal(t, http.StatusTeapot, response.StatusCode)
}

func TestHardenLimitsRedirects(t *testing.T) {
	// Arrange: every response redirects to itself.
	server := httptest.NewTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "https://loop.test/again", http.StatusFound)
	}))
	client := netguard.Harden(server.Client(), netguard.Policy{}, 5*time.Second)

	request, err := http.NewRequestWithContext(context.Background(), http.MethodGet, "https://loop.test/", nil)
	require.NoError(t, err)

	// Act
	response, err := client.Do(request)
	if response != nil {
		response.Body.Close()
	}

	// Assert
	require.ErrorIs(t, err, netguard.ErrTooManyRedirects)
}
