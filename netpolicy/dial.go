package netpolicy

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/netip"
)

// HTTPClient returns a client that can only reach what the policy allows.
//
// Builtins are given this client rather than the policy itself: a command that
// holds no client has no way to the network at all, and one that holds this
// client cannot ask for a destination the policy has not approved.
func (p *Policy) HTTPClient() *http.Client {
	return &http.Client{
		Transport: &http.Transport{
			// Proxy is deliberately left nil. The usual default,
			// http.ProxyFromEnvironment, would send every request to whatever
			// HTTP_PROXY names, and the only address checked would be the
			// proxy's: the allow list would decide nothing.
			Proxy:             nil,
			DialContext:       p.dialContext,
			ForceAttemptHTTP2: true,
		},
	}
}

// dialContext resolves the destination itself, checks the addresses it got
// back, and then connects to one of those addresses directly.
//
// Resolving here, rather than letting the dialer do it, is what makes the check
// meaningful: the connection is opened to the very address that was approved,
// so a name that answers differently the second time around cannot be used to
// land somewhere else.
func (p *Policy) dialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}

	hostAllowed := false
	var addrs []netip.Addr
	if ip, parseErr := netip.ParseAddr(host); parseErr == nil {
		// A literal address stands for itself; there is no name to match.
		addrs = []netip.Addr{ip}
	} else {
		hostAllowed = p.hostListed(host)
		// A lookup is itself a request that leaves the host, carrying the name
		// to the resolver and on to whoever answers for it. With no address
		// entry there is no address the name could be authorized by, so
		// resolving it would send it out to learn nothing.
		if !hostAllowed && len(p.nets) == 0 {
			return nil, fmt.Errorf("%w: %s is not in the allow list", ErrNotAllowed, host)
		}
		addrs, err = p.resolver.LookupNetIP(ctx, "ip", host)
		if err != nil {
			return nil, err
		}
	}

	var dialer net.Dialer
	var firstErr error
	for _, ip := range addrs {
		ip = ip.Unmap()
		if err := p.authorize(ip, hostAllowed); err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		conn, err := dialer.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
		if err == nil {
			return conn, nil
		}
		if firstErr == nil {
			firstErr = err
		}
	}
	if firstErr == nil {
		firstErr = fmt.Errorf("%w: %s resolved to no addresses", ErrNotAllowed, host)
	}
	return nil, firstErr
}
