package app

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/kmdn-app/kmdn/internal/config"
)

func TestRunReturnsListenError(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	a, _ := newApp(t, func(c *config.Config) { c.Server.Listen = ln.Addr().String() })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- a.Run(ctx) }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected listen error")
		}
	case <-time.After(time.Second):
		cancel()
		<-done
		t.Fatal("Run hung after listener failed until caller canceled the context")
	}
}
