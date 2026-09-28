package app

import (
	"context"
	"net"
	"runtime"
	"strings"
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
		// Run's background work must be over when it returns: a goroutine
		// still enqueueing writes into the data dir while the test removes it.
		if left := runGoroutines(); left != "" {
			t.Fatalf("goroutines outlive Run:\n%s", left)
		}
	case <-time.After(time.Second):
		cancel()
		<-done
		t.Fatal("Run hung after listener failed until caller canceled the context")
	}
}

// runGoroutines returns the stacks of goroutines Run starts in the background.
func runGoroutines() string {
	buf := make([]byte, 1<<20)
	buf = buf[:runtime.Stack(buf, true)]
	var left []string
	for _, g := range strings.Split(string(buf), "\n\n") {
		if strings.Contains(g, "internal/app.(*App).periodic") || strings.Contains(g, "internal/llm.(*Service).CheckEnv") {
			left = append(left, g)
		}
	}
	return strings.Join(left, "\n\n")
}
