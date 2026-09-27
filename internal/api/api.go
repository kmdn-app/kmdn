// Package api holds HTTP helpers shared by kmdn's REST handlers: JSON
// decoding with limits, RFC 9457 problem responses and request metadata.
package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
)

// Problem is an RFC 9457 problem detail with a stable machine-readable code.
type Problem struct {
	Type   string         `json:"type"`
	Title  string         `json:"title"`
	Status int            `json:"status"`
	Code   string         `json:"code"`
	Detail string         `json:"detail,omitempty"`
	Params map[string]any `json:"params,omitempty"`
}

func (p *Problem) Error() string {
	if p.Detail != "" {
		return p.Code + ": " + p.Detail
	}
	return p.Code
}

// Err builds a problem.
func Err(status int, code, detail string) *Problem {
	return &Problem{Type: "about:blank", Title: http.StatusText(status), Status: status, Code: code, Detail: detail}
}

// Common problems.
var (
	ErrUnauthorized = Err(http.StatusUnauthorized, "unauthorized", "Sign in to continue.")
	ErrForbidden    = Err(http.StatusForbidden, "forbidden", "You don't have permission to do this.")
	ErrNotFound     = Err(http.StatusNotFound, "not_found", "")
	ErrCSRF         = Err(http.StatusForbidden, "csrf", "Missing or invalid X-Kmdn-CSRF header. Reload the page and try again.")
	ErrRateLimited  = Err(http.StatusTooManyRequests, "rate_limited", "Too many attempts. Wait a few minutes and try again.")
)

// WithParam returns a copy of p with a param set.
func (p *Problem) WithParam(k string, v any) *Problem {
	c := *p
	c.Params = map[string]any{}
	for kk, vv := range p.Params {
		c.Params[kk] = vv
	}
	c.Params[k] = v
	return &c
}

// JSON writes v with status code.
func JSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if v != nil {
		_ = json.NewEncoder(w).Encode(v)
	}
}

// Error writes err as a problem. Non-problem errors become 500s and are logged.
func Error(w http.ResponseWriter, r *http.Request, err error) {
	var p *Problem
	if !errors.As(err, &p) {
		slog.ErrorContext(r.Context(), "internal error", "path", r.URL.Path, "error", err)
		p = Err(http.StatusInternalServerError, "internal", "Something went wrong on the server. Try again, and check the server logs if it keeps happening.")
	}
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(p.Status)
	_ = json.NewEncoder(w).Encode(p)
}

// Decode reads a JSON body (max 1 MiB) into v, rejecting unknown fields.
func Decode(r *http.Request, v any) error {
	return DecodeLimit(r, v, 1<<20)
}

// DecodeLimit is Decode with an explicit size limit.
func DecodeLimit(r *http.Request, v any, limit int64) error {
	ct := r.Header.Get("Content-Type")
	if ct != "" && !strings.HasPrefix(ct, "application/json") {
		return Err(http.StatusUnsupportedMediaType, "unsupported_media_type", "Send JSON with Content-Type: application/json.")
	}
	dec := json.NewDecoder(io.LimitReader(r.Body, limit+1))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		if errors.Is(err, io.EOF) {
			return Err(http.StatusBadRequest, "invalid_body", "The request body is empty.")
		}
		return Err(http.StatusBadRequest, "invalid_body", fmt.Sprintf("The request body isn't valid JSON: %v", err))
	}
	return nil
}

// Invalid is a 422 for a specific field.
func Invalid(field, detail string) *Problem {
	return Err(http.StatusUnprocessableEntity, "invalid_field", detail).WithParam("field", field)
}

// ClientIP returns the request's client IP. X-Forwarded-For is honored only
// when the direct peer is in trusted.
func ClientIP(r *http.Request, trusted []*net.IPNet) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	peer := net.ParseIP(host)
	if peer == nil || !inNets(peer, trusted) {
		return host
	}
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		parts := strings.Split(xff, ",")
		for i := len(parts) - 1; i >= 0; i-- {
			ip := net.ParseIP(strings.TrimSpace(parts[i]))
			if ip != nil && !inNets(ip, trusted) {
				return ip.String()
			}
		}
	}
	return host
}

func inNets(ip net.IP, nets []*net.IPNet) bool {
	for _, n := range nets {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

// ParseCIDRs parses trusted proxy ranges; bare IPs become /32 or /128.
func ParseCIDRs(list []string) ([]*net.IPNet, error) {
	var out []*net.IPNet
	for _, s := range list {
		if !strings.Contains(s, "/") {
			if strings.Contains(s, ":") {
				s += "/128"
			} else {
				s += "/32"
			}
		}
		_, n, err := net.ParseCIDR(s)
		if err != nil {
			return nil, fmt.Errorf("trusted proxy %q: %w", s, err)
		}
		out = append(out, n)
	}
	return out, nil
}
