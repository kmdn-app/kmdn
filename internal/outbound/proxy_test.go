package outbound

import (
	"bufio"
	"bytes"
	"context"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

func echoServer(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() { _, _ = io.Copy(c, c); _ = c.Close() }()
		}
	}()
	return ln.Addr().String()
}

// connect sends CONNECT target through the proxy, with its credential or not.
func connect(t *testing.T, p *Proxy, target string, auth bool) (int, net.Conn) {
	t.Helper()
	u, _ := url.Parse(p.URL)
	c, err := net.Dial("tcp", u.Host)
	if err != nil {
		t.Fatal(err)
	}
	req := "CONNECT " + target + " HTTP/1.1\r\nHost: " + target + "\r\n"
	if auth {
		req += "Proxy-Authorization: " + p.auth + "\r\n"
	}
	_, _ = c.Write([]byte(req + "\r\n"))
	res, err := http.ReadResponse(bufio.NewReader(c), nil)
	if err != nil {
		t.Fatal(err)
	}
	return res.StatusCode, c
}

// git's connections through the proxy: its credential only, CONNECT only,
// and the guarded dialer decides where.
func TestProxy(t *testing.T) {
	echo := echoServer(t)
	guarded, err := StartProxy(Dialer(false).DialContext)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = guarded.Close() }()
	if code, _ := connect(t, guarded, echo, false); code != http.StatusProxyAuthRequired {
		t.Fatalf("no credential: %d", code)
	}
	if code, _ := connect(t, guarded, echo, true); code != http.StatusForbidden {
		t.Fatalf("a private address: %d", code)
	}
	u, _ := url.Parse(guarded.URL)
	req, _ := http.NewRequest("GET", "http://"+u.Host+"/", nil)
	req.Header.Set("Proxy-Authorization", guarded.auth)
	if res, err := http.DefaultClient.Do(req); err != nil || res.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("plain request: %v %v", res, err)
	}

	open, _ := StartProxy((&net.Dialer{}).DialContext)
	defer func() { _ = open.Close() }()
	code, c := connect(t, open, echo, true)
	if code != 200 {
		t.Fatalf("tunnel: %d", code)
	}
	_, _ = c.Write([]byte("ping"))
	b := make([]byte, 4)
	if _, err := io.ReadFull(c, b); err != nil || string(b) != "ping" {
		t.Fatalf("through the tunnel: %q %v", b, err)
	}
	_ = c.Close()
}

func TestDialStdio(t *testing.T) {
	echo := echoServer(t)
	var out bytes.Buffer
	if err := DialStdio(context.Background(), Dialer(false).DialContext, echo, strings.NewReader("x"), &out); err == nil {
		t.Fatal("dialed a private address")
	}
	if err := DialStdio(context.Background(), (&net.Dialer{}).DialContext, echo, strings.NewReader("hello"), &out); err != nil || out.String() != "hello" {
		t.Fatalf("stdio: %q %v", out.String(), err)
	}
}
