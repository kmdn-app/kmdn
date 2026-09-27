// Package policy decides what organizations may do on an instance
// (docs/specs/16-organizations.md#policy). The zero Policy is permissive:
// self-hosted admins are trusted with configuration (docs/specs/15-security.md
// T10). Strict is for instances whose org admins aren't the operator (T20).
package policy

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"

	"github.com/kmdn-app/kmdn/internal/api"
	"github.com/kmdn-app/kmdn/internal/outbound"
)

// Policy is the instance's rules for orgs.
type Policy struct {
	// Strict limits org-supplied destinations: git over https or ssh to
	// public hosts only, and outbound requests for org-owned forges and
	// webhooks to public addresses only.
	Strict bool
	// NoOrgForges stops org admins from adding their own forge hosts (they
	// use the instance's shared ones).
	NoOrgForges bool
	// Limits returns an org's limits; nil means none.
	Limits func(ctx context.Context, orgID string) Limits
}

// Limits are an org's caps; 0 means no cap.
type Limits struct {
	Members int // people in the org, counting pending invitations
	Repos   int
	// UploadMaxMB lowers the instance's upload size limit.
	UploadMaxMB int
}

// ErrLimit is returned when an org is at one of its limits.
type ErrLimit struct {
	What  string // members | repos
	Limit int
}

func (e *ErrLimit) Error() string {
	return fmt.Sprintf("this organization can have up to %d %s", e.Limit, e.What)
}

// For returns an org's limits.
func (p *Policy) For(ctx context.Context, orgID string) Limits {
	if p == nil || p.Limits == nil {
		return Limits{}
	}
	return p.Limits(ctx, orgID)
}

// ErrGitURL describes why a clone URL was refused.
type ErrGitURL struct{ Reason string }

func (e *ErrGitURL) Error() string { return e.Reason }

// GitProtocols is what git may use (GIT_ALLOW_PROTOCOL); "" allows its
// defaults.
func (p *Policy) GitProtocols() string {
	if p == nil || !p.Strict {
		return ""
	}
	return "https:ssh"
}

// GitURL checks a plain-git clone URL an org admin gave. In strict mode only
// https:// and ssh:// URLs (or user@host:path) to hosts that resolve to
// public addresses pass; local paths, file://, ext:: and other transports
// never do.
func (p *Policy) GitURL(ctx context.Context, raw string) error {
	if p == nil || !p.Strict {
		return nil
	}
	raw = strings.TrimSpace(raw)
	if strings.HasPrefix(raw, "-") {
		return &ErrGitURL{"Use an https:// or ssh:// URL."}
	}
	host := ""
	switch {
	case strings.HasPrefix(raw, "https://"), strings.HasPrefix(raw, "ssh://"):
		u, err := url.Parse(raw)
		if err != nil || u.Hostname() == "" {
			return &ErrGitURL{"Enter a repository URL like https://git.example.com/team/docs.git."}
		}
		host = u.Hostname()
	case strings.Contains(raw, "://"):
		return &ErrGitURL{"Use an https:// or ssh:// URL."}
	default:
		// scp-like: user@host:path. Anything else is a local path.
		at, colon := strings.Index(raw, "@"), strings.Index(raw, ":")
		if at <= 0 || colon < at+2 || strings.ContainsAny(raw[:colon], "/\\") {
			return &ErrGitURL{"Use an https:// or ssh:// URL; local paths aren't allowed."}
		}
		host = raw[at+1 : colon]
	}
	if strings.HasPrefix(host, "-") {
		return &ErrGitURL{"That host name isn't valid."}
	}
	return PublicHost(ctx, host)
}

// ErrPrivateHost means a host is (or resolves to) a non-public address.
var ErrPrivateHost = &ErrGitURL{"That host is on a private network."}

// PublicHost checks that host is a public address or resolves only to
// public addresses.
func PublicHost(ctx context.Context, host string) error {
	if ip, err := netip.ParseAddr(strings.Trim(host, "[]")); err == nil {
		if !outbound.Public(ip.String()) {
			return ErrPrivateHost
		}
		return nil
	}
	addrs, err := net.DefaultResolver.LookupHost(ctx, host)
	if err != nil || len(addrs) == 0 {
		return &ErrGitURL{"That host can't be found."}
	}
	for _, a := range addrs {
		if !outbound.Public(a) {
			return ErrPrivateHost
		}
	}
	return nil
}

// OrgForgesAllowed reports whether org admins can add their own forges.
func (p *Policy) OrgForgesAllowed() bool { return p == nil || !p.NoOrgForges }

// AllowPrivateOutbound reports whether requests the instance makes for an
// org (its forges, its webhooks) may reach private addresses.
func (p *Policy) AllowPrivateOutbound(configured bool) bool {
	if p != nil && p.Strict {
		return false
	}
	return configured
}

// Problem is the API error for an org at a limit (409 limit_reached, with
// the limit and what it caps as params).
func Problem(l *ErrLimit) *api.Problem {
	return api.Err(http.StatusConflict, "limit_reached", "This organization has reached its limit of "+strconv.Itoa(l.Limit)+" "+l.What+".").
		WithParam("what", l.What).WithParam("limit", l.Limit)
}

// IsLimit reports whether err is an ErrLimit.
func IsLimit(err error) (*ErrLimit, bool) {
	var l *ErrLimit
	return l, errors.As(err, &l)
}
