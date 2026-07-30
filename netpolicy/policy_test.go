package netpolicy

import (
	"net/netip"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNew_RejectsMalformedEntry(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		entry string
	}{
		"empty":                {entry: ""},
		"bare wildcard":        {entry: "*"},
		"wildcard mid-label":   {entry: "foo*.example.com"},
		"wildcard mid-pattern": {entry: "*.*.example.com"},
		"prefix out of range":  {entry: "10.0.0.0/33"},
		"url":                  {entry: "https://example.com"},
		"whitespace":           {entry: "exa mple.com"},
		"port":                 {entry: "example.com:443"},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			_, err := New([]string{tc.entry})
			assert.Error(t, err)
		})
	}
}

func TestPolicy_hostListed(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		entries []string
		allowed []string
		denied  []string
	}{
		"exact host": {
			entries: []string{"example.com"},
			allowed: []string{"example.com", "EXAMPLE.COM", "example.com."},
			denied:  []string{"other.example.com", "notexample.com", "evil.com"},
		},
		"wildcard covers subdomains at any depth but not the domain itself": {
			entries: []string{"*.example.com"},
			allowed: []string{"a.example.com", "a.b.example.com"},
			denied:  []string{"example.com", "xexample.com", "example.com.evil.com"},
		},
		"several entries": {
			entries: []string{"example.com", "*.github.com"},
			allowed: []string{"example.com", "api.github.com"},
			denied:  []string{"github.com", "api.example.com"},
		},
		"address entries never allow a host by name": {
			entries: []string{"192.168.1.1", "10.0.1.1/24"},
			denied:  []string{"example.com"},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			p, err := New(tc.entries)
			require.NoError(t, err)

			for _, h := range tc.allowed {
				assert.True(t, p.hostListed(h), "%q should be allowed", h)
			}
			for _, h := range tc.denied {
				assert.False(t, p.hostListed(h), "%q should be denied", h)
			}
		})
	}
}

func TestPolicy_authorize(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		entries     []string
		hostAllowed bool
		ip          string
		wantAllowed bool
	}{
		"allowed name resolving to a public address": {
			entries:     []string{"example.com"},
			hostAllowed: true,
			ip:          "93.184.216.34",
			wantAllowed: true,
		},
		// Where an allowed name points is out of scope; see the network section
		// of the README. The four cases below pin that decision rather than
		// merely record what the code happens to do.
		"allowed name resolving to cloud metadata": {
			entries:     []string{"example.com"},
			hostAllowed: true,
			ip:          "169.254.169.254",
			wantAllowed: true,
		},
		"allowed name resolving to loopback": {
			entries:     []string{"example.com"},
			hostAllowed: true,
			ip:          "127.0.0.1",
			wantAllowed: true,
		},
		"allowed name resolving to a private address": {
			entries:     []string{"example.com"},
			hostAllowed: true,
			ip:          "10.0.1.5",
			wantAllowed: true,
		},
		"allowed name resolving to IPv6 link-local": {
			entries:     []string{"example.com"},
			hostAllowed: true,
			ip:          "fe80::1",
			wantAllowed: true,
		},
		"an address entry covers a literal loopback address": {
			entries:     []string{"127.0.0.1/32"},
			ip:          "127.0.0.1",
			wantAllowed: true,
		},
		"an address entry covers a literal private address": {
			entries:     []string{"10.0.1.1/24"},
			ip:          "10.0.1.5",
			wantAllowed: true,
		},
		"an address outside every entry is refused": {
			entries: []string{"10.0.1.1/24"},
			ip:      "10.0.2.5",
		},
		"a name that matched nothing is refused even on a public address": {
			entries: []string{"example.com"},
			ip:      "93.184.216.34",
		},
		"an empty policy allows nothing": {
			ip: "93.184.216.34",
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			p, err := New(tc.entries)
			require.NoError(t, err)

			err = p.authorize(netip.MustParseAddr(tc.ip), tc.hostAllowed)
			if tc.wantAllowed {
				assert.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.ErrorIs(t, err, ErrNotAllowed)
		})
	}
}

func TestPolicy_addrListed(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		entries []string
		allowed []string
		denied  []string
	}{
		"single address": {
			entries: []string{"192.168.1.1"},
			allowed: []string{"192.168.1.1"},
			denied:  []string{"192.168.1.2"},
		},
		"prefix with host bits set still covers its block": {
			entries: []string{"10.0.1.1/24"},
			allowed: []string{"10.0.1.0", "10.0.1.1", "10.0.1.255"},
			denied:  []string{"10.0.2.1"},
		},
		"IPv4-mapped IPv6 is matched as IPv4": {
			entries: []string{"192.168.1.1"},
			allowed: []string{"::ffff:192.168.1.1"},
		},
		"IPv6 prefix": {
			entries: []string{"2001:db8::/32"},
			allowed: []string{"2001:db8::1"},
			denied:  []string{"2001:db9::1"},
		},
		"host entries never allow an address": {
			entries: []string{"example.com"},
			denied:  []string{"93.184.216.34"},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			p, err := New(tc.entries)
			require.NoError(t, err)

			for _, s := range tc.allowed {
				assert.True(t, p.addrListed(netip.MustParseAddr(s)), "%q should be allowed", s)
			}
			for _, s := range tc.denied {
				assert.False(t, p.addrListed(netip.MustParseAddr(s)), "%q should be denied", s)
			}
		})
	}
}
