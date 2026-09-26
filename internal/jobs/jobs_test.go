package jobs

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kmdn-app/kmdn/internal/store"
	"github.com/kmdn-app/kmdn/internal/storetest"
)

func newQ(t *testing.T) (*Queue, *store.DB) {
	db := storetest.Open(t)
	return New(db, Options{PollInterval: 10 * time.Millisecond, Workers: 2}), db
}

func TestRunSuccessStoresResult(t *testing.T) {
	q, _ := newQ(t)
	ctx := context.Background()
	q.Register("echo", func(_ context.Context, j Job) (any, error) {
		var p struct{ N int }
		if err := j.Decode(&p); err != nil {
			return nil, err
		}
		return map[string]int{"n": p.N * 2}, nil
	})
	id, err := q.Enqueue(ctx, q.db, "echo", map[string]int{"N": 21}, EnqueueOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if ran, err := q.RunOnce(ctx); !ran || err != nil {
		t.Fatalf("ran=%v err=%v", ran, err)
	}
	j, err := q.Get(ctx, id)
	if err != nil || j.Status != Done || string(j.Result) != `{"n":42}` || j.Attempts != 1 {
		t.Fatalf("job: %+v err %v", j, err)
	}
}

func TestUniqueKeyDedupes(t *testing.T) {
	q, _ := newQ(t)
	ctx := context.Background()
	a, err := q.Enqueue(ctx, q.db, "sync", nil, EnqueueOptions{Key: "rev_1"})
	if err != nil {
		t.Fatal(err)
	}
	b, err := q.Enqueue(ctx, q.db, "sync", nil, EnqueueOptions{Key: "rev_1"})
	if err != nil || a != b {
		t.Fatalf("expected dedupe: %s %s %v", a, b, err)
	}
	c, _ := q.Enqueue(ctx, q.db, "sync", nil, EnqueueOptions{Key: "rev_2"})
	if c == a {
		t.Fatal("different keys must not dedupe")
	}
}

func TestRetryThenFail(t *testing.T) {
	q, _ := newQ(t)
	ctx := context.Background()
	clock := time.Now()
	q.now = func() time.Time { return clock }
	q.Register("flaky", func(context.Context, Job) (any, error) { return nil, errors.New("nope") })
	id, _ := q.Enqueue(ctx, q.db, "flaky", nil, EnqueueOptions{MaxAttempts: 2})
	if _, err := q.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	j, _ := q.Get(ctx, id)
	if j.Status != Pending || j.LastError != "nope" || !j.RunAt.After(clock) {
		t.Fatalf("after first failure: %+v", j)
	}
	if ran, _ := q.RunOnce(ctx); ran {
		t.Fatal("job should wait for its backoff")
	}
	clock = clock.Add(time.Hour)
	if ran, _ := q.RunOnce(ctx); !ran {
		t.Fatal("job should run after backoff")
	}
	if j, _ = q.Get(ctx, id); j.Status != Failed || j.Attempts != 2 {
		t.Fatalf("expected failed after max attempts: %+v", j)
	}
}

func TestPermanentAndPanic(t *testing.T) {
	q, _ := newQ(t)
	ctx := context.Background()
	q.Register("perm", func(context.Context, Job) (any, error) { return nil, Permanent(errors.New("bad input")) })
	q.Register("panic", func(context.Context, Job) (any, error) { panic("kaboom") })
	p, _ := q.Enqueue(ctx, q.db, "perm", nil, EnqueueOptions{})
	k, _ := q.Enqueue(ctx, q.db, "panic", nil, EnqueueOptions{MaxAttempts: 1})
	for i := 0; i < 2; i++ {
		if _, err := q.RunOnce(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if j, _ := q.Get(ctx, p); j.Status != Failed {
		t.Fatalf("permanent: %+v", j)
	}
	if j, _ := q.Get(ctx, k); j.Status != Failed || j.LastError != "panic: kaboom" {
		t.Fatalf("panic: %+v", j)
	}
}

func TestExpiredLockIsReclaimed(t *testing.T) {
	q, _ := newQ(t)
	ctx := context.Background()
	clock := time.Now()
	q.now = func() time.Time { return clock }
	q.Register("slow", func(context.Context, Job) (any, error) { return "ok", nil })
	id, _ := q.Enqueue(ctx, q.db, "slow", nil, EnqueueOptions{})
	// Simulate a worker that crashed mid-job.
	if _, err := store.Exec(ctx, q.db, `UPDATE jobs SET status='running', locked_by='dead', locked_until=? WHERE id=?`, store.Millis(clock.Add(-time.Second)), id); err != nil {
		t.Fatal(err)
	}
	if ran, err := q.RunOnce(ctx); !ran || err != nil {
		t.Fatalf("reclaim: %v %v", ran, err)
	}
	if j, _ := q.Get(ctx, id); j.Status != Done {
		t.Fatalf("%+v", j)
	}
}

func TestRunWorkers(t *testing.T) {
	q, _ := newQ(t)
	ctx, cancel := context.WithCancel(context.Background())
	var n atomic.Int32
	q.Register("count", func(context.Context, Job) (any, error) { n.Add(1); return nil, nil })
	for i := 0; i < 20; i++ {
		if _, err := q.Enqueue(ctx, q.db, "count", i, EnqueueOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	done := make(chan struct{})
	go func() { q.Run(ctx); close(done) }()
	deadline := time.After(10 * time.Second)
	for n.Load() < 20 {
		select {
		case <-deadline:
			t.Fatalf("only %d jobs ran", n.Load())
		case <-time.After(10 * time.Millisecond):
		}
	}
	cancel()
	<-done
}

func TestBackoffBounds(t *testing.T) {
	for a := 1; a < 20; a++ {
		if d := Backoff(a); d <= 0 || d > time.Hour+time.Hour/5 {
			t.Fatalf("attempt %d: %v", a, d)
		}
	}
}
