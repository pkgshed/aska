package discovery_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pkgshed/aska/internal/discovery"
	"github.com/pkgshed/aska/internal/netguard"
	"github.com/pkgshed/aska/internal/source"
)

// pages maps request paths to fixture files; anything else is a 404.
type pages map[string]string

func newResolver(t *testing.T, routes pages) (*discovery.Resolver, *[]string) {
	t.Helper()

	var requested []string
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requested = append(requested, r.Host+r.URL.RequestURI())
		fixture, found := routes[r.URL.Path]
		if !found || r.URL.Query().Get("aska-get") != "1" {
			http.NotFound(w, r)
			return
		}
		content, err := os.ReadFile(filepath.Join("testdata", fixture))
		if err != nil {
			t.Errorf("read fixture %s: %v", fixture, err)
		}
		_, _ = w.Write(content)
	})

	// The in-memory test server receives every request regardless of host.
	server := httptest.NewTestServer(t, handler)
	client := netguard.Harden(server.Client(), netguard.Policy{}, 5*time.Second)
	return &discovery.Resolver{Client: client}, &requested
}

func parse(t *testing.T, text string) source.ImportPath {
	t.Helper()
	importPath, err := source.ParseImportPath(text)
	require.NoError(t, err)
	return importPath
}

func TestResolveUsesStaticRulesWithoutNetwork(t *testing.T) {
	// Arrange
	resolver, requested := newResolver(t, pages{})

	// Act
	pkg, err := resolver.Resolve(context.Background(), parse(t, "github.com/acme/skills/skills/x"))

	// Assert
	require.NoError(t, err)
	assert.Equal(t, "https://github.com/acme/skills.git", pkg.Repo.URL())
	assert.Empty(t, *requested)
}

func TestResolveReadsMetaTagAtRoot(t *testing.T) {
	// Arrange
	resolver, _ := newResolver(t, pages{"/skills": "root.html"})

	// Act
	pkg, err := resolver.Resolve(context.Background(), parse(t, "vanity.test/skills"))

	// Assert
	require.NoError(t, err)
	assert.Equal(t, "https://git.vanity.test/skills.git", pkg.Repo.URL())
	assert.Equal(t, source.RootPath, pkg.Path)
}

func TestResolveConfirmsPrefixAtRepositoryRoot(t *testing.T) {
	// Arrange
	resolver, requested := newResolver(t, pages{
		"/skills/tools/code-review": "root.html",
		"/skills":                   "root.html",
	})

	// Act
	pkg, err := resolver.Resolve(context.Background(), parse(t, "vanity.test/skills/tools/code-review"))

	// Assert
	require.NoError(t, err)
	assert.Equal(t, "https://git.vanity.test/skills.git", pkg.Repo.URL())
	assert.Equal(t, source.Path("tools/code-review"), pkg.Path)
	assert.Equal(t, []string{
		"vanity.test/skills/tools/code-review?aska-get=1",
		"vanity.test/skills?aska-get=1",
	}, *requested)
}

func TestResolveAppliesSubdirectory(t *testing.T) {
	// Arrange
	resolver, _ := newResolver(t, pages{"/mono/code-review": "subdir.html", "/mono": "subdir.html"})

	// Act
	pkg, err := resolver.Resolve(context.Background(), parse(t, "vanity.test/mono/code-review"))

	// Assert
	require.NoError(t, err)
	assert.Equal(t, "https://git.vanity.test/mono.git", pkg.Repo.URL())
	assert.Equal(t, source.Path("agents/skills/code-review"), pkg.Path)
}

func TestResolveRejectsBadMetaTags(t *testing.T) {
	tests := []struct {
		name   string
		routes pages
		path   string
		want   error
	}{
		{
			name:   "ambiguous",
			routes: pages{"/skills/code-review": "ambiguous.html"},
			path:   "vanity.test/skills/code-review",
			want:   discovery.ErrAmbiguousMetaTag,
		},
		{
			name:   "root disagrees",
			routes: pages{"/skills/x": "root.html", "/skills": "mismatch.html"},
			path:   "vanity.test/skills/x",
			want:   discovery.ErrMetaTagMismatch,
		},
		{
			name:   "not git",
			routes: pages{"/skills": "mercurial.html"},
			path:   "vanity.test/skills",
			want:   discovery.ErrUnsupportedVCS,
		},
		{
			name:   "insecure repository",
			routes: pages{"/skills": "insecure.html"},
			path:   "vanity.test/skills",
			want:   source.ErrInsecureScheme,
		},
		{
			name:   "no tag",
			routes: pages{"/skills": "none.html"},
			path:   "vanity.test/skills",
			want:   discovery.ErrNoMetaTag,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			// Arrange
			resolver, _ := newResolver(t, test.routes)

			// Act
			_, err := resolver.Resolve(context.Background(), parse(t, test.path))

			// Assert
			require.ErrorIs(t, err, test.want)
		})
	}
}

func TestResolveReportsHTTPErrors(t *testing.T) {
	// Arrange
	resolver, _ := newResolver(t, pages{})

	// Act
	_, err := resolver.Resolve(context.Background(), parse(t, "vanity.test/missing"))

	// Assert
	require.Error(t, err)
	assert.Contains(t, err.Error(), "404")
}

func TestResolveRefusesRedirectToPlainHTTP(t *testing.T) {
	// Arrange
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://vanity.test/elsewhere?aska-get=1", http.StatusFound)
	})
	server := httptest.NewTestServer(t, handler)
	resolver := &discovery.Resolver{Client: netguard.Harden(server.Client(), netguard.Policy{}, 5*time.Second)}

	// Act
	_, err := resolver.Resolve(context.Background(), parse(t, "vanity.test/skills"))

	// Assert
	require.ErrorIs(t, err, netguard.ErrInsecureRedirect)
}
