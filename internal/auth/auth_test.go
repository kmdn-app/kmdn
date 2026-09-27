package auth

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/kmdn-app/kmdn/internal/mail"
	"github.com/kmdn-app/kmdn/internal/storetest"
	"github.com/kmdn-app/kmdn/internal/users"
)

type harness struct {
	svc  *Service
	mail *mail.Capture
	srv  *httptest.Server
	now  time.Time
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	db := storetest.Open(t)
	h := &harness{mail: &mail.Capture{}, now: time.Now()}
	h.svc = &Service{DB: db, Mail: h.mail, BaseURL: "https://docs.example.com", Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	h.svc.now = func() time.Time { return h.now }
	hh := NewHTTP(h.svc, false, nil)
	r := chi.NewRouter()
	r.Use(hh.Middleware)
	hh.Routes(r)
	h.srv = httptest.NewServer(r)
	t.Cleanup(h.srv.Close)
	return h
}

type client struct {
	t       *testing.T
	base    string
	cookies map[string]string
}

func (h *harness) client(t *testing.T) *client {
	return &client{t: t, base: h.srv.URL, cookies: map[string]string{}}
}

func (c *client) do(method, path, body string, csrf bool) (*http.Response, string) {
	c.t.Helper()
	req, _ := http.NewRequest(method, c.base+path, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range c.cookies {
		req.AddCookie(&http.Cookie{Name: k, Value: v})
	}
	if csrf {
		req.Header.Set(CSRFHeader, c.cookies[CSRFCookie])
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		c.t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	for _, ck := range res.Cookies() {
		if ck.MaxAge < 0 {
			delete(c.cookies, ck.Name)
		} else {
			c.cookies[ck.Name] = ck.Value
		}
	}
	return res, string(b)
}

var tokenRe = regexp.MustCompile(`token=([A-Za-z0-9_-]+)`)
var codeRe = regexp.MustCompile(`code on the sign-in page: (\d{6})`)

func TestMagicLinkFlow(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	if _, err := users.Create(ctx, h.svc.DB, "maya@northwind.dev", "Maya", true); err != nil {
		t.Fatal(err)
	}
	c := h.client(t)
	res, _ := c.do("POST", "/auth/magic-link", `{"email":" Maya@Northwind.dev "}`, false)
	if res.StatusCode != 202 {
		t.Fatalf("request: %d", res.StatusCode)
	}
	m, ok := h.mail.Last()
	if !ok || m.To != "maya@northwind.dev" {
		t.Fatalf("no email: %+v", m)
	}
	tok := tokenRe.FindStringSubmatch(m.Text)[1]
	res, body := c.do("POST", "/auth/magic-link/verify", `{"token":"`+tok+`"}`, false)
	if res.StatusCode != 200 || c.cookies[SessionCookie] == "" || c.cookies[CSRFCookie] == "" {
		t.Fatalf("verify: %d %s %v", res.StatusCode, body, c.cookies)
	}
	// Token is single use.
	c2 := h.client(t)
	if res, _ := c2.do("POST", "/auth/magic-link/verify", `{"token":"`+tok+`"}`, false); res.StatusCode != 401 {
		t.Fatalf("reuse: %d", res.StatusCode)
	}
	res, body = c.do("GET", "/me", "", false)
	var me struct {
		Email string `json:"email"`
		CSRF  string `json:"csrf_token"`
	}
	_ = json.Unmarshal([]byte(body), &me)
	if res.StatusCode != 200 || me.Email != "maya@northwind.dev" || me.CSRF != c.cookies[CSRFCookie] {
		t.Fatalf("me: %d %s", res.StatusCode, body)
	}
	// CSRF required for unsafe methods.
	if res, _ := c.do("PATCH", "/me", `{"name":"Maya Chen"}`, false); res.StatusCode != 403 {
		t.Fatalf("csrf not enforced: %d", res.StatusCode)
	}
	if res, body := c.do("PATCH", "/me", `{"name":"Maya Chen","theme":"dark"}`, true); res.StatusCode != 200 || !strings.Contains(body, "Maya Chen") {
		t.Fatalf("patch: %d %s", res.StatusCode, body)
	}
	// Appearance is per user: palette and interface size persist on the account.
	if res, body := c.do("GET", "/me", "", false); res.StatusCode != 200 || !strings.Contains(body, `"palette":"default"`) || !strings.Contains(body, `"ui_scale":120`) {
		t.Fatalf("default appearance: %d %s", res.StatusCode, body)
	}
	if res, body := c.do("PATCH", "/me", `{"palette":"nord","ui_scale":135}`, true); res.StatusCode != 200 || !strings.Contains(body, `"palette":"nord"`) || !strings.Contains(body, `"ui_scale":135`) || !strings.Contains(body, `"theme":"dark"`) {
		t.Fatalf("appearance: %d %s", res.StatusCode, body)
	}
	if res, _ := c.do("PATCH", "/me", `{"palette":"solarized"}`, true); res.StatusCode != 422 {
		t.Fatalf("unknown palette: %d", res.StatusCode)
	}
	if res, _ := c.do("PATCH", "/me", `{"ui_scale":300}`, true); res.StatusCode != 422 {
		t.Fatalf("bad size: %d", res.StatusCode)
	}
	if res, _ := c.do("POST", "/auth/logout", "", true); res.StatusCode != 204 {
		t.Fatalf("logout: %d", res.StatusCode)
	}
	if res, _ := c.do("GET", "/me", "", false); res.StatusCode != 401 {
		t.Fatalf("after logout: %d", res.StatusCode)
	}
}

func TestUnknownEmailGetsSameAnswerAndNoMail(t *testing.T) {
	h := newHarness(t)
	c := h.client(t)
	res, _ := c.do("POST", "/auth/magic-link", `{"email":"stranger@example.com"}`, false)
	if res.StatusCode != 202 || len(h.mail.Sent) != 0 {
		t.Fatalf("%d sent=%d", res.StatusCode, len(h.mail.Sent))
	}
	if res, _ := c.do("POST", "/auth/magic-link", `{"email":"nope"}`, false); res.StatusCode != 422 {
		t.Fatalf("invalid email: %d", res.StatusCode)
	}
}

func TestCodeFlowAttemptsAndExpiry(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	_, _ = users.Create(ctx, h.svc.DB, "tom@northwind.dev", "Tom", false)
	c := h.client(t)
	c.do("POST", "/auth/magic-link", `{"email":"tom@northwind.dev"}`, false)
	m, _ := h.mail.Last()
	code := codeRe.FindStringSubmatch(m.Text)[1]
	wrong := "000000"
	if code == wrong {
		wrong = "111111"
	}
	for i := 0; i < MaxCodeAttempts; i++ {
		if res, _ := c.do("POST", "/auth/magic-link/verify", `{"email":"tom@northwind.dev","code":"`+wrong+`"}`, false); res.StatusCode != 401 {
			t.Fatalf("wrong code accepted: %d", res.StatusCode)
		}
	}
	// Locked after too many attempts, even with the right code.
	if res, _ := c.do("POST", "/auth/magic-link/verify", `{"email":"tom@northwind.dev","code":"`+code+`"}`, false); res.StatusCode != 401 {
		t.Fatalf("lockout failed: %d", res.StatusCode)
	}
	// Two new links in the same millisecond: the newest email's code works.
	c.do("POST", "/auth/magic-link", `{"email":"tom@northwind.dev"}`, false)
	c.do("POST", "/auth/magic-link", `{"email":"tom@northwind.dev"}`, false)
	m, _ = h.mail.Last()
	code = codeRe.FindStringSubmatch(m.Text)[1]
	if res, _ := c.do("POST", "/auth/magic-link/verify", `{"email":"tom@northwind.dev","code":"`+code[:3]+" "+code[3:]+`"}`, false); res.StatusCode != 200 {
		t.Fatalf("code with space: %d", res.StatusCode)
	}
	// Expired links fail.
	c.do("POST", "/auth/magic-link", `{"email":"tom@northwind.dev"}`, false)
	m, _ = h.mail.Last()
	tok := tokenRe.FindStringSubmatch(m.Text)[1]
	h.now = h.now.Add(LinkTTL + time.Second)
	if _, err := h.svc.VerifyToken(ctx, tok); !errors.Is(err, ErrInvalidLink) {
		t.Fatalf("expired link: %v", err)
	}
}

func TestAutoJoinDomainCreatesAccount(t *testing.T) {
	h := newHarness(t)
	h.svc.AutoJoinDomains = []string{"northwind.dev"}
	c := h.client(t)
	c.do("POST", "/auth/magic-link", `{"email":"new.hire@northwind.dev"}`, false)
	m, ok := h.mail.Last()
	if !ok {
		t.Fatal("auto-join should send")
	}
	tok := tokenRe.FindStringSubmatch(m.Text)[1]
	if res, body := c.do("POST", "/auth/magic-link/verify", `{"token":"`+tok+`"}`, false); res.StatusCode != 200 || !strings.Contains(body, "new.hire") {
		t.Fatalf("%d %s", res.StatusCode, body)
	}
}

func TestSessionsListRevokeAndRolling(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	u, _ := users.Create(ctx, h.svc.DB, "a@b.co", "A", false)
	tokA, sa, _ := h.svc.CreateSession(ctx, h.svc.DB, u.ID, "1.1.1.1", "Firefox")
	_, sb, _ := h.svc.CreateSession(ctx, h.svc.DB, u.ID, "2.2.2.2", "Safari")
	list, _ := h.svc.Sessions(ctx, u.ID, sa.ID)
	if len(list) != 2 {
		t.Fatalf("sessions %d", len(list))
	}
	h.now = h.now.Add(2 * time.Hour)
	s, _, err := h.svc.Lookup(ctx, tokA)
	if err != nil || !s.ExpiresAt.After(sa.ExpiresAt) {
		t.Fatalf("session did not roll: %v %v %v", err, s.ExpiresAt, sa.ExpiresAt)
	}
	if err := h.svc.RevokeUser(ctx, u.ID, sa.ID); err != nil {
		t.Fatal(err)
	}
	list, _ = h.svc.Sessions(ctx, u.ID, sa.ID)
	if len(list) != 1 || list[0].ID != sa.ID || !list[0].Current || list[0].ID == sb.ID {
		t.Fatalf("revoke others: %+v", list)
	}
	h.now = h.now.Add(31 * 24 * time.Hour)
	if _, _, err := h.svc.Lookup(ctx, tokA); err == nil {
		t.Fatal("expired session accepted")
	}
}
