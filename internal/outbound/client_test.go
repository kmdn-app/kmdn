package outbound

import (
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestClientBlocksPrivateAddresses(t *testing.T) {
	var reached atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reached.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()
	_, port, _ := net.SplitHostPort(srv.Listener.Addr().String())
	for _, host := range []string{"127.0.0.1", "localhost", "::1", "10.0.0.1", "169.254.169.254"} {
		client := Client(false)
		_, err := client.Get("http://" + net.JoinHostPort(host, port))
		client.CloseIdleConnections()
		if !errors.Is(err, ErrPrivate) {
			t.Errorf("%s: wanted private-address refusal, got %v", host, err)
		}
	}
	if reached.Load() != 0 {
		t.Fatal("a denied destination received a request")
	}
	client := Client(true)
	defer client.CloseIdleConnections()
	res, err := client.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusNoContent || reached.Load() != 1 {
		t.Fatal("explicitly allowed private delivery failed")
	}
}

func TestClientDoesNotFollowRedirects(t *testing.T) {
	var reached atomic.Int32
	private := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reached.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer private.Close()
	endpoint := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/redirect" {
			http.Redirect(w, r, private.URL, http.StatusFound)
			return
		}
		w.WriteHeader(http.StatusCreated)
	}))
	defer endpoint.Close()
	client := Client(false)
	// This trusted TLS transport represents the public push service in the test.
	client.Transport = endpoint.Client().Transport
	defer client.CloseIdleConnections()
	for path, want := range map[string]int{"/": http.StatusCreated, "/redirect": http.StatusFound} {
		res, err := client.Get(endpoint.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode != want {
			t.Errorf("%s: status %d, want %d", path, res.StatusCode, want)
		}
	}
	if reached.Load() != 0 {
		t.Fatal("redirect reached a private HTTP service")
	}
}
