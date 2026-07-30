// Package netpolicy decides which network destinations a sandbox may reach.
//
// A policy is an allow list. Each entry is one of:
//
//   - a host name, e.g. "example.com"
//   - a host name with a leading "*." wildcard, e.g. "*.github.com", which
//     covers subdomains at any depth but not the domain itself
//   - an IP address, e.g. "192.168.1.1"
//   - a CIDR block, e.g. "10.0.1.1/24"
//
// A destination is reachable when its host name matches a host entry or the
// address it resolves to matches an address entry. Names are checked when a
// request is made; addresses are checked when a connection is opened, on the
// address the connection actually uses, so a name that is reachable only
// because an address entry covers where it points cannot be moved elsewhere
// between the check and the connection.
//
// Where a name matching a host entry resolves is not checked. Listing a name
// grants what that name resolves to, including a private or loopback address;
// see the network section of the README.
//
// The package depends only on the standard library and knows nothing about the
// sandbox.
package netpolicy

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"strings"
)

// Policy is a parsed allow list together with the resolver used to find out
// where a name points.
type Policy struct {
	hosts    []hostRule
	nets     []netip.Prefix
	resolver Resolver
}

// Resolver looks up the addresses a host name points to. [net.DefaultResolver]
// implements it; tests supply their own so that a policy decision never depends
// on live DNS.
type Resolver interface {
	LookupNetIP(ctx context.Context, network, host string) ([]netip.Addr, error)
}

// Option configures a Policy.
type Option func(*Policy)

// WithResolver replaces the resolver used to turn a host name into addresses.
func WithResolver(r Resolver) Option {
	return func(p *Policy) { p.resolver = r }
}

// hostRule is one host entry. A wildcard rule covers the subdomains of name;
// an exact rule covers name itself.
type hostRule struct {
	name     string
	wildcard bool
}

func (r hostRule) matches(host string) bool {
	if r.wildcard {
		return strings.HasSuffix(host, "."+r.name)
	}
	return host == r.name
}

// New parses entries into a policy. It reports an error on the first entry it
// cannot make sense of, so that a caller fails at startup rather than running
// with a policy that was only partly understood.
func New(entries []string, opts ...Option) (*Policy, error) {
	p := &Policy{resolver: net.DefaultResolver}
	for _, e := range entries {
		if err := p.add(e); err != nil {
			return nil, fmt.Errorf("network allow entry %q: %w", e, err)
		}
	}
	for _, opt := range opts {
		opt(p)
	}
	return p, nil
}

func (p *Policy) add(entry string) error {
	if entry == "" {
		return errors.New("empty entry")
	}
	if strings.Contains(entry, "/") {
		prefix, err := netip.ParsePrefix(entry)
		if err != nil {
			return err
		}
		p.nets = append(p.nets, prefix.Masked())
		return nil
	}
	if addr, err := netip.ParseAddr(entry); err == nil {
		addr = addr.Unmap()
		p.nets = append(p.nets, netip.PrefixFrom(addr, addr.BitLen()))
		return nil
	}
	return p.addHost(entry)
}

func (p *Policy) addHost(entry string) error {
	name := strings.ToLower(entry)

	wildcard := strings.HasPrefix(name, "*.")
	if wildcard {
		name = strings.TrimPrefix(name, "*.")
	}
	if strings.Contains(name, "*") {
		return errors.New(`"*" is only allowed as a leading "*." label`)
	}

	name = strings.TrimSuffix(name, ".")
	if err := validateHostName(name); err != nil {
		return err
	}

	p.hosts = append(p.hosts, hostRule{name: name, wildcard: wildcard})
	return nil
}

// validateHostName rejects anything that is not a plain dotted name, so that a
// URL, a host:port pair or a typo is reported instead of being stored as a name
// that can never match.
func validateHostName(name string) error {
	if name == "" {
		return errors.New("empty host name")
	}
	for _, label := range strings.Split(name, ".") {
		if label == "" {
			return errors.New("empty label in host name")
		}
		for _, r := range label {
			switch {
			case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '_':
			default:
				return fmt.Errorf("invalid character %q in host name", r)
			}
		}
	}
	return nil
}

// hostListed reports whether a host entry covers this name.
//
// Address entries never match a name; what they allow is settled on the address
// the connection resolves to.
func (p *Policy) hostListed(host string) bool {
	h := strings.TrimSuffix(strings.ToLower(host), ".")
	if h == "" {
		return false
	}
	for _, r := range p.hosts {
		if r.matches(h) {
			return true
		}
	}
	return false
}

// addrListed reports whether an address entry covers this address.
func (p *Policy) addrListed(ip netip.Addr) bool {
	ip = ip.Unmap()
	for _, n := range p.nets {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

// ErrNotAllowed is the sentinel behind every refusal, so that callers can tell
// a policy decision from a network failure.
var ErrNotAllowed = errors.New("network policy")

// authorize decides whether a connection to ip may proceed. hostAllowed says
// whether the name the connection was made in the name of matched a host entry;
// it is false when the destination was given as a literal address.
//
// Where an allowed name points is not this package's decision. An entry names a
// destination; the mapping from that name to an address is settled by whoever
// runs its DNS, and refusing a name because it lands on a private address would
// also refuse the internal service an operator allowed on purpose. An address
// entry is how a caller pins the destination when that mapping is not one to
// trust.
func (p *Policy) authorize(ip netip.Addr, hostAllowed bool) error {
	if hostAllowed {
		return nil
	}
	if p.addrListed(ip) {
		return nil
	}
	return fmt.Errorf("%w: %s is not in the allow list", ErrNotAllowed, ip.Unmap())
}
