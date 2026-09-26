package api

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestDecodeRejectsUnknownAndLarge(t *testing.T) {
	var v struct{ Email string }
	r := httptest.NewRequest("POST", "/", strings.NewReader(`{"Email":"a","extra":1}`))
	if err := Decode(r, &v); err == nil {
		t.Fatal("unknown field accepted")
	}
	r = httptest.NewRequest("POST", "/", strings.NewReader(`{"Email":"`+strings.Repeat("x", 100)+`"}`))
	if err := DecodeLimit(r, &v, 20); err == nil {
		t.Fatal("oversize body accepted")
	}
	r = httptest.NewRequest("POST", "/", strings.NewReader(`{"Email":"a@b"}`))
	r.Header.Set("Content-Type", "application/json; charset=utf-8")
	if err := Decode(r, &v); err != nil || v.Email != "a@b" {
		t.Fatal(err)
	}
}

func TestErrorWritesProblem(t *testing.T) {
	rec := httptest.NewRecorder()
	Error(rec, httptest.NewRequest("GET", "/", nil), Invalid("email", "Enter a valid email address."))
	if rec.Code != 422 || rec.Header().Get("Content-Type") != "application/problem+json" || !strings.Contains(rec.Body.String(), `"field":"email"`) {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	rec = httptest.NewRecorder()
	Error(rec, httptest.NewRequest("GET", "/", nil), errors.New("db exploded"))
	if rec.Code != 500 || strings.Contains(rec.Body.String(), "exploded") {
		t.Fatalf("internal error leaked: %s", rec.Body)
	}
}

func TestClientIP(t *testing.T) {
	nets, _ := ParseCIDRs([]string{"10.0.0.0/8"})
	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "10.1.2.3:5000"
	r.Header.Set("X-Forwarded-For", "203.0.113.9, 10.0.0.2")
	if got := ClientIP(r, nets); got != "203.0.113.9" {
		t.Fatal(got)
	}
	r.RemoteAddr = "198.51.100.1:5000"
	if got := ClientIP(r, nets); got != "198.51.100.1" {
		t.Fatalf("untrusted peer should not use XFF: %s", got)
	}
	_ = http.MethodGet
}

func TestLimiter(t *testing.T) {
	l := NewLimiter(2, time.Minute)
	now := time.Now()
	l.now = func() time.Time { return now }
	first, second, third := l.Allow("a"), l.Allow("a"), l.Allow("a")
	if !first || !second || third {
		t.Fatal("limit not enforced")
	}
	if !l.Allow("b") {
		t.Fatal("keys must be independent")
	}
	now = now.Add(61 * time.Second)
	if !l.Allow("a") {
		t.Fatal("window did not slide")
	}
}
