package netpolicy

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeResolver answers from a fixed table so that no test depends on live DNS.
type fakeResolver map[string][]netip.Addr

func (f fakeResolver) LookupNetIP(_ context.Context, _, host string) ([]netip.Addr, error) {
	addrs, ok := f[host]
	if !ok {
		return nil, &net.DNSError{Err: "no such host", Name: host, IsNotFound: true}
	}
	return addrs, nil
}

// recordingResolver answers like the resolver it wraps and records the names it
// was asked about, so that a test can pin which lookups happen at all.
type recordingResolver struct {
	inner fakeResolver
	asked []string
}

func (r *recordingResolver) LookupNetIP(ctx context.Context, network, host string) ([]netip.Addr, error) {
	r.asked = append(r.asked, host)
	return r.inner.LookupNetIP(ctx, network, host)
}

// serverPointingAt starts a server on loopback and returns it together with a
// resolver that maps every given name to its address, so a test can drive a
// request for any host name at a server it controls.
func serverPointingAt(t *testing.T, names ...string) (*httptest.Server, fakeResolver, string) {
	t.Helper()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, "reached")
	}))
	t.Cleanup(srv.Close)

	u, err := url.Parse(srv.URL)
	require.NoError(t, err)
	addr, err := netip.ParseAddr(u.Hostname())
	require.NoError(t, err)

	resolver := fakeResolver{}
	for _, n := range names {
		resolver[n] = []netip.Addr{addr}
	}
	return srv, resolver, u.Port()
}

func TestPolicy_HTTPClient(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		entries []string
		// host is the name the request is made for. Empty means the server's
		// own loopback address is used.
		host      string
		wantReach bool
	}{
		"a literal address covered by an entry is reachable": {
			entries:   []string{"127.0.0.1/32"},
			wantReach: true,
		},
		"a literal address with no entry is refused": {
			entries: []string{"example.com"},
		},
		"an allowed name is reachable wherever it resolves": {
			entries:   []string{"example.com"},
			host:      "example.com",
			wantReach: true,
		},
		"a name no host entry covers is reachable when an address entry covers where it points": {
			entries:   []string{"127.0.0.1/32"},
			host:      "example.com",
			wantReach: true,
		},
		"a name outside the allow list is refused": {
			entries: []string{"example.com", "127.0.0.1/32"},
			host:    "evil.com",
		},
		"a wildcard entry covers a subdomain": {
			entries:   []string{"*.example.com"},
			host:      "api.example.com",
			wantReach: true,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			srv, resolver, port := serverPointingAt(t, "example.com", "api.example.com")
			// evil.com points somewhere no entry covers. Were it to point at
			// the server like the others, the entry for the server's own
			// address would allow it and the name would never be consulted.
			resolver["evil.com"] = []netip.Addr{netip.MustParseAddr("203.0.113.1")}

			p, err := New(tc.entries, WithResolver(resolver))
			require.NoError(t, err)

			target := srv.URL
			if tc.host != "" {
				target = "http://" + net.JoinHostPort(tc.host, port)
			}

			res, err := p.HTTPClient().Get(target)
			if !tc.wantReach {
				require.Error(t, err)
				assert.ErrorIs(t, err, ErrNotAllowed)
				return
			}
			require.NoError(t, err)
			defer res.Body.Close()

			body, err := io.ReadAll(res.Body)
			require.NoError(t, err)
			assert.Equal(t, "reached", string(body))
		})
	}
}

// TestPolicy_HTTPClientDoesNotResolveUnlistedNames pins when a name reaches the
// resolver. A lookup leaves the host: the query carries whatever the name
// encodes to the resolver and on to the name's authoritative server, so a name
// that no entry could approve must be refused before it is resolved.
//
// With an address entry present the lookup is what the decision needs, and the
// cases below pin that too rather than leaving it to follow from the first.
func TestPolicy_HTTPClientDoesNotResolveUnlistedNames(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		entries   []string
		host      string
		wantAsked []string
		wantReach bool
	}{
		"an unlisted name is refused before it is resolved": {
			entries: []string{"example.com"},
			host:    "secret.evil.com",
		},
		"an address entry is what makes resolving an unlisted name necessary": {
			entries:   []string{"example.com", "127.0.0.1/32"},
			host:      "secret.evil.com",
			wantAsked: []string{"secret.evil.com"},
		},
		"a listed name is resolved": {
			entries:   []string{"example.com"},
			host:      "example.com",
			wantAsked: []string{"example.com"},
			wantReach: true,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			_, resolver, port := serverPointingAt(t, "example.com")
			resolver["secret.evil.com"] = []netip.Addr{netip.MustParseAddr("203.0.113.1")}
			recorder := &recordingResolver{inner: resolver}

			p, err := New(tc.entries, WithResolver(recorder))
			require.NoError(t, err)

			res, err := p.HTTPClient().Get("http://" + net.JoinHostPort(tc.host, port))
			if tc.wantReach {
				require.NoError(t, err)
				res.Body.Close()
			} else {
				require.Error(t, err)
				assert.ErrorIs(t, err, ErrNotAllowed)
			}

			assert.Equal(t, tc.wantAsked, recorder.asked)
		})
	}
}

// TestPolicy_HTTPClientChecksRedirects pins that a redirect is subject to the
// same decision as the request that caused it. Nothing in the client inspects
// redirects: each hop opens a connection of its own, keyed by the name it was
// made for, so the check at connect time is reached again.
func TestPolicy_HTTPClientChecksRedirects(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		redirectTo string
		wantReach  bool
	}{
		"to an allowed name":      {redirectTo: "api.example.com", wantReach: true},
		"to a name with no entry": {redirectTo: "evil.com"},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			var port string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/start" {
					http.Redirect(w, r, "http://"+net.JoinHostPort(tc.redirectTo, port)+"/end", http.StatusFound)
					return
				}
				io.WriteString(w, "reached")
			}))
			t.Cleanup(srv.Close)

			u, err := url.Parse(srv.URL)
			require.NoError(t, err)
			port = u.Port()
			serverAddr := netip.MustParseAddr(u.Hostname())

			resolver := fakeResolver{
				"start.example.com": {serverAddr},
				"api.example.com":   {serverAddr},
				"evil.com":          {netip.MustParseAddr("203.0.113.1")},
			}

			p, err := New([]string{"start.example.com", "api.example.com"}, WithResolver(resolver))
			require.NoError(t, err)

			res, err := p.HTTPClient().Get("http://" + net.JoinHostPort("start.example.com", port) + "/start")
			if !tc.wantReach {
				require.Error(t, err)
				assert.ErrorIs(t, err, ErrNotAllowed)
				return
			}
			require.NoError(t, err)
			defer res.Body.Close()

			body, err := io.ReadAll(res.Body)
			require.NoError(t, err)
			assert.Equal(t, "reached", string(body))
		})
	}
}

// TestPolicy_HTTPClientIgnoresProxyEnvironment pins that the allow list is not
// bypassed by the ambient proxy configuration. With the default transport the
// request would go to the proxy named here, and the only address checked would
// be the proxy's.
func TestPolicy_HTTPClientIgnoresProxyEnvironment(t *testing.T) {
	// t.Setenv forbids t.Parallel.
	srv, resolver, _ := serverPointingAt(t)

	t.Setenv("HTTP_PROXY", "http://192.0.2.1:3128")
	t.Setenv("HTTPS_PROXY", "http://192.0.2.1:3128")

	p, err := New([]string{"127.0.0.1/32"}, WithResolver(resolver))
	require.NoError(t, err)

	res, err := p.HTTPClient().Get(srv.URL)
	require.NoError(t, err)
	defer res.Body.Close()

	body, err := io.ReadAll(res.Body)
	require.NoError(t, err)
	assert.Equal(t, "reached", string(body))
}

func TestPolicy_HTTPClientReportsResolutionFailure(t *testing.T) {
	t.Parallel()

	p, err := New([]string{"example.com"}, WithResolver(fakeResolver{}))
	require.NoError(t, err)

	_, err = p.HTTPClient().Get("http://example.com/")
	require.Error(t, err)

	var dnsErr *net.DNSError
	assert.ErrorAs(t, err, &dnsErr)
}
