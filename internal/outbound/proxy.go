package outbound

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"io"
	"net"
	"net/http"
	"time"
)

// Proxy is an HTTP CONNECT proxy on loopback for git. git resolves names
// and follows redirects itself, so checking a remote's URL when it's added
// can't stop it from reaching a private address later (DNS rebinding, a
// redirect): through here, every connection git opens is checked when it's
// made. It asks for a credential of its own, so other local programs can't
// use it.
type Proxy struct {
	// URL is what git's http.proxy is set to, credential included.
	URL string

	ln   net.Listener
	srv  *http.Server
	auth string
	dial func(ctx context.Context, network, addr string) (net.Conn, error)
}

// StartProxy listens on a loopback port; dial makes the connections
// (Dialer(false).DialContext in production).
func StartProxy(dial func(ctx context.Context, network, addr string) (net.Conn, error)) (*Proxy, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	var b [16]byte
	_, _ = rand.Read(b[:])
	token := hex.EncodeToString(b[:])
	p := &Proxy{URL: "http://kmdn:" + token + "@" + ln.Addr().String(), ln: ln, dial: dial,
		auth: "Basic " + base64.StdEncoding.EncodeToString([]byte("kmdn:"+token))}
	p.srv = &http.Server{Handler: p, ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = p.srv.Serve(ln) }()
	return p, nil
}

// Close stops the proxy.
func (p *Proxy) Close() error { return p.srv.Close() }

func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if subtle.ConstantTimeCompare([]byte(r.Header.Get("Proxy-Authorization")), []byte(p.auth)) != 1 {
		w.Header().Set("Proxy-Authenticate", `Basic realm="kmdn"`)
		http.Error(w, "proxy authentication required", http.StatusProxyAuthRequired)
		return
	}
	if r.Method != http.MethodConnect {
		http.Error(w, "only CONNECT (https remotes)", http.StatusMethodNotAllowed)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	up, err := p.dial(ctx, "tcp", r.Host)
	cancel()
	if err != nil {
		http.Error(w, "kmdn won't connect to "+r.Host+": "+err.Error(), http.StatusForbidden)
		return
	}
	hj, ok := w.(http.Hijacker)
	if !ok {
		_ = up.Close()
		http.Error(w, "can't tunnel", http.StatusInternalServerError)
		return
	}
	down, rw, err := hj.Hijack()
	if err != nil {
		_ = up.Close()
		return
	}
	_, _ = down.Write([]byte("HTTP/1.1 200 Connection established\r\n\r\n"))
	if n := rw.Reader.Buffered(); n > 0 {
		b, _ := rw.Peek(n)
		_, _ = up.Write(b)
	}
	go pipe(up, down)
	pipe(down, up)
}

func pipe(dst, src net.Conn) {
	_, _ = io.Copy(dst, src)
	_ = dst.Close()
	_ = src.Close()
}

// DialStdio connects stdin and stdout to host:port through dial: git's ssh
// ProxyCommand, so ssh remotes are checked like https ones.
func DialStdio(ctx context.Context, dial func(ctx context.Context, network, addr string) (net.Conn, error), hostport string, in io.Reader, out io.Writer) error {
	c, err := dial(ctx, "tcp", hostport)
	if err != nil {
		return err
	}
	done := make(chan struct{})
	go func() {
		_, _ = io.Copy(out, c)
		close(done)
	}()
	_, _ = io.Copy(c, in)
	if tc, ok := c.(*net.TCPConn); ok {
		_ = tc.CloseWrite()
	}
	<-done
	return c.Close()
}
