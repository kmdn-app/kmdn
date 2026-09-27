// Package outbound limits requests to user-configured destinations.
package outbound

import (
	"errors"
	"net"
	"net/http"
	"syscall"
	"time"
)

// ErrPrivate means a destination resolved to a non-public address.
var ErrPrivate = errors.New("the address is on a private network")

// Client checks the resolved address at dial time, including after DNS changes.
// Redirects and environment proxies cannot bypass that check.
func Client(allowPrivate bool) *http.Client {
	d := &net.Dialer{Timeout: 10 * time.Second}
	if !allowPrivate {
		d.Control = func(_, address string, _ syscall.RawConn) error {
			host, _, err := net.SplitHostPort(address)
			if err != nil {
				return err
			}
			ip := net.ParseIP(host)
			if ip == nil || !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() {
				return ErrPrivate
			}
			return nil
		}
	}
	return &http.Client{
		Transport:     &http.Transport{DialContext: d.DialContext, TLSHandshakeTimeout: 10 * time.Second},
		Timeout:       20 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}
