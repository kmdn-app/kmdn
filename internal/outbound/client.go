// Package outbound limits requests to user-configured destinations.
package outbound

import (
	"errors"
	"net"
	"net/http"
	"net/netip"
	"syscall"
	"time"
)

// ErrPrivate means a destination resolved to a non-public address.
var ErrPrivate = errors.New("the address is on a private network")

// Client checks the resolved address at dial time, including after DNS changes.
// Redirects and environment proxies cannot bypass that check.
func Client(allowPrivate bool) *http.Client {
	d := Dialer(allowPrivate)
	return &http.Client{
		Transport:     &http.Transport{DialContext: d.DialContext, TLSHandshakeTimeout: 10 * time.Second},
		Timeout:       20 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

// Dialer connects only to public addresses unless allowPrivate: the check
// runs on the address actually dialed, after resolution.
func Dialer(allowPrivate bool) *net.Dialer {
	d := &net.Dialer{Timeout: 10 * time.Second}
	if !allowPrivate {
		d.Control = func(_, address string, _ syscall.RawConn) error {
			host, _, err := net.SplitHostPort(address)
			if err != nil {
				return err
			}
			if !publicAddress(host) {
				return ErrPrivate
			}
			return nil
		}
	}
	return d
}

// Public reports whether an IP address (as text) is publicly routable.
func Public(host string) bool { return publicAddress(host) }

func publicAddress(host string) bool {
	ip, err := netip.ParseAddr(host)
	if err != nil {
		return false
	}
	ip = ip.Unmap().WithZone("")
	if nat64Prefix.Contains(ip) {
		// The standard NAT64 prefix routes to the embedded IPv4 destination.
		v6 := ip.As16()
		ip = netip.AddrFrom4([4]byte{v6[12], v6[13], v6[14], v6[15]})
	}
	if !ip.IsGlobalUnicast() || ip.IsPrivate() {
		return false
	}
	for _, prefix := range publicExceptions {
		if prefix.Contains(ip) {
			return true
		}
	}
	for _, prefix := range nonpublicPrefixes {
		if prefix.Contains(ip) {
			return false
		}
	}
	return true
}

var nat64Prefix = netip.MustParsePrefix("64:ff9b::/96")

// IsGlobalUnicast includes special-purpose addresses that are not globally
// reachable. These IANA blocks supplement Go's private/local address checks:
// https://www.iana.org/assignments/iana-ipv4-special-registry/
// https://www.iana.org/assignments/iana-ipv6-special-registry/
var nonpublicPrefixes = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("192.88.99.2/32"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("64:ff9b:1::/48"),
	netip.MustParsePrefix("100::/64"),
	netip.MustParsePrefix("100:0:0:1::/64"),
	netip.MustParsePrefix("2001::/23"),
	netip.MustParsePrefix("2001:db8::/32"),
	netip.MustParsePrefix("3fff::/20"),
	netip.MustParsePrefix("5f00::/16"),
}

// More specific allocations remain reachable within the reserved parent blocks.
// Teredo is not classified as non-global and must not be blocked with its parent.
var publicExceptions = []netip.Prefix{
	netip.MustParsePrefix("192.0.0.9/32"),
	netip.MustParsePrefix("192.0.0.10/32"),
	netip.MustParsePrefix("2001::/32"),
	netip.MustParsePrefix("2001:1::1/128"),
	netip.MustParsePrefix("2001:1::2/128"),
	netip.MustParsePrefix("2001:1::3/128"),
	netip.MustParsePrefix("2001:3::/32"),
	netip.MustParsePrefix("2001:4:112::/48"),
	netip.MustParsePrefix("2001:20::/28"),
	netip.MustParsePrefix("2001:30::/28"),
}
