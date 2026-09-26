// Package jobs is kmdn's in-process, database-backed job queue. Jobs survive
// restarts, retry with exponential backoff, and can carry a unique key so at
// most one pending/running job exists per (kind, key). Postgres claims rows
// with FOR UPDATE SKIP LOCKED; SQLite relies on its single writer.
package jobs

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/kmdn-app/kmdn/internal/ids"
	"github.com/kmdn-app/kmdn/internal/store"
)

// Status values.
const (
	Pending = "pending"
	Running = "running"
	Done    = "done"
	Failed  = "failed"
)

// Job is a unit of work handed to a Handler.
type Job struct {
	ID          string
	Kind        string
	Key         string
	Payload     json.RawMessage
	Status      string
	Attempts    int
	MaxAttempts int
	LastError   string
	Result      json.RawMessage
	RunAt       time.Time
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// Decode unmarshals the payload.
func (j Job) Decode(v any) error { return json.Unmarshal(j.Payload, v) }

// Handler processes a job. The returned value is stored as the job result.
type Handler func(ctx context.Context, j Job) (any, error)

type permanent struct{ err error }

func (p permanent) Error() string { return p.err.Error() }
func (p permanent) Unwrap() error { return p.err }

// Permanent marks an error as not worth retrying.
func Permanent(err error) error { return permanent{err} }

// Options configures a Queue.
type Options struct {
	Workers      int           // default 4
	PollInterval time.Duration // default 1s
	LockFor      time.Duration // default 5m; a crashed worker's job is retried after this
	Logger       *slog.Logger
}

// Queue enqueues and runs jobs.
type Queue struct {
	db       *store.DB
	opts     Options
	workerID string

	mu       sync.RWMutex
	handlers map[string]Handler
	wake     chan struct{}
	now      func() time.Time
}

// New creates a Queue.
func New(db *store.DB, o Options) *Queue {
	if o.Workers <= 0 {
		o.Workers = 4
	}
	if o.PollInterval <= 0 {
		o.PollInterval = time.Second
	}
	if o.LockFor <= 0 {
		o.LockFor = 5 * time.Minute
	}
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
	host, _ := os.Hostname()
	return &Queue{
		db: db, opts: o, handlers: map[string]Handler{},
		wake:     make(chan struct{}, 1),
		workerID: fmt.Sprintf("%s:%d:%s", host, os.Getpid(), ids.New("w")[2:10]),
		now:      time.Now,
	}
}

// Register sets the handler for a job kind.
func (q *Queue) Register(kind string, h Handler) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.handlers[kind] = h
}

// EnqueueOptions tunes a single enqueue.
type EnqueueOptions struct {
	Key         string    // unique while pending/running
	RunAt       time.Time // zero means now
	MaxAttempts int       // default 10
}

// Enqueue adds a job. When Key is set and an active job with the same kind
// and key exists, its id is returned and no new job is created.
func (q *Queue) Enqueue(ctx context.Context, qr store.Querier, kind string, payload any, o EnqueueOptions) (string, error) {
	b, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	now := q.now()
	runAt := o.RunAt
	if runAt.IsZero() {
		runAt = now
	}
	maxA := o.MaxAttempts
	if maxA <= 0 {
		maxA = 10
	}
	var key any
	if o.Key != "" {
		key = o.Key
	}
	id := ids.New(ids.Job)
	_, err = store.Exec(ctx, qr, `INSERT INTO jobs (id, kind, unique_key, payload, status, run_at, max_attempts, created_at, updated_at)
		VALUES (?, ?, ?, ?, 'pending', ?, ?, ?, ?)`, id, kind, key, string(b), store.Millis(runAt), maxA, store.Millis(now), store.Millis(now))
	if err != nil {
		if o.Key != "" && store.IsUniqueViolation(err) {
			var existing string
			if err := store.QueryRow(ctx, qr, `SELECT id FROM jobs WHERE kind = ? AND unique_key = ? AND status IN ('pending','running')`, kind, o.Key).Scan(&existing); err == nil {
				return existing, nil
			}
		}
		return "", err
	}
	q.Notify()
	return id, nil
}

// Notify wakes an idle worker (call after a transaction that enqueued jobs commits).
func (q *Queue) Notify() {
	select {
	case q.wake <- struct{}{}:
	default:
	}
}

// Get returns a job by id.
func (q *Queue) Get(ctx context.Context, id string) (Job, error) {
	row := store.QueryRow(ctx, q.db, `SELECT `+jobCols+` FROM jobs WHERE id = ?`, id)
	j, err := scanJob(row)
	return j, store.NotFound(err)
}

const jobCols = `id, kind, unique_key, payload, status, attempts, max_attempts, last_error, result, run_at, created_at, updated_at`

type scanner interface{ Scan(...any) error }

func scanJob(r scanner) (Job, error) {
	var j Job
	var key, lastErr, result sql.NullString
	var payload string
	var runAt, created, updated int64
	if err := r.Scan(&j.ID, &j.Kind, &key, &payload, &j.Status, &j.Attempts, &j.MaxAttempts, &lastErr, &result, &runAt, &created, &updated); err != nil {
		return j, err
	}
	j.Key, j.LastError = key.String, lastErr.String
	j.Payload = json.RawMessage(payload)
	if result.Valid {
		j.Result = json.RawMessage(result.String)
	}
	j.RunAt, j.CreatedAt, j.UpdatedAt = store.FromMillis(runAt), store.FromMillis(created), store.FromMillis(updated)
	return j, nil
}

// claim atomically takes the next runnable job for a registered kind.
func (q *Queue) claim(ctx context.Context) (Job, bool, error) {
	q.mu.RLock()
	kinds := make([]any, 0, len(q.handlers))
	for k := range q.handlers {
		kinds = append(kinds, k)
	}
	q.mu.RUnlock()
	if len(kinds) == 0 {
		return Job{}, false, nil
	}
	now := store.Millis(q.now())
	lock := ""
	if q.db.Dialect == store.Postgres {
		lock = " FOR UPDATE SKIP LOCKED"
	}
	in := strings.TrimSuffix(strings.Repeat("?,", len(kinds)), ",")
	query := `UPDATE jobs SET status = 'running', attempts = attempts + 1, locked_by = ?, locked_until = ?, updated_at = ?
		WHERE id = (SELECT id FROM jobs WHERE kind IN (` + in + `)
			AND ((status = 'pending' AND run_at <= ?) OR (status = 'running' AND locked_until < ?))
			ORDER BY run_at LIMIT 1` + lock + `)
		RETURNING ` + jobCols
	args := []any{q.workerID, now + q.opts.LockFor.Milliseconds(), now}
	args = append(args, kinds...)
	args = append(args, now, now)
	j, err := scanJob(store.QueryRow(ctx, q.db, query, args...))
	if errors.Is(err, sql.ErrNoRows) {
		return Job{}, false, nil
	}
	if err != nil {
		return Job{}, false, err
	}
	return j, true, nil
}

// Backoff returns the delay before retry number attempt (1-based): 2^n
// seconds with ±20% jitter, capped at one hour.
func Backoff(attempt int) time.Duration {
	if attempt > 12 {
		attempt = 12
	}
	d := time.Duration(1<<attempt) * time.Second
	if d > time.Hour {
		d = time.Hour
	}
	jitter := time.Duration(rand.Int64N(int64(d)/5+1)) - d/10
	return d + jitter
}

func (q *Queue) finish(ctx context.Context, j Job, result any, herr error) error {
	now := q.now()
	if herr == nil {
		b, err := json.Marshal(result)
		if err != nil {
			b = []byte("null")
		}
		_, err = store.Exec(ctx, q.db, `UPDATE jobs SET status = 'done', result = ?, locked_by = NULL, locked_until = NULL, last_error = NULL, updated_at = ? WHERE id = ? AND locked_by = ?`,
			string(b), store.Millis(now), j.ID, q.workerID)
		return err
	}
	var p permanent
	if errors.As(herr, &p) || j.Attempts >= j.MaxAttempts {
		_, err := store.Exec(ctx, q.db, `UPDATE jobs SET status = 'failed', last_error = ?, locked_by = NULL, locked_until = NULL, updated_at = ? WHERE id = ? AND locked_by = ?`,
			herr.Error(), store.Millis(now), j.ID, q.workerID)
		return err
	}
	_, err := store.Exec(ctx, q.db, `UPDATE jobs SET status = 'pending', last_error = ?, run_at = ?, locked_by = NULL, locked_until = NULL, updated_at = ? WHERE id = ? AND locked_by = ?`,
		herr.Error(), store.Millis(now.Add(Backoff(j.Attempts))), store.Millis(now), j.ID, q.workerID)
	return err
}

// RunOnce claims and runs a single job. It reports whether a job ran.
func (q *Queue) RunOnce(ctx context.Context) (bool, error) {
	j, ok, err := q.claim(ctx)
	if err != nil || !ok {
		return false, err
	}
	q.mu.RLock()
	h := q.handlers[j.Kind]
	q.mu.RUnlock()
	jctx, cancel := context.WithTimeout(ctx, q.opts.LockFor)
	result, herr := safeRun(jctx, h, j)
	cancel()
	if herr != nil {
		q.opts.Logger.Warn("job failed", "job_id", j.ID, "kind", j.Kind, "attempt", j.Attempts, "error", herr)
	}
	return true, q.finish(context.WithoutCancel(ctx), j, result, herr)
}

func safeRun(ctx context.Context, h Handler, j Job) (res any, err error) {
	defer func() {
		if p := recover(); p != nil {
			err = fmt.Errorf("panic: %v", p)
		}
	}()
	return h(ctx, j)
}

// Run starts the workers and blocks until ctx is cancelled.
func (q *Queue) Run(ctx context.Context) {
	var wg sync.WaitGroup
	for i := 0; i < q.opts.Workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			t := time.NewTicker(q.opts.PollInterval)
			defer t.Stop()
			for {
				ran, err := q.RunOnce(ctx)
				if err != nil && ctx.Err() == nil {
					q.opts.Logger.Error("job queue", "error", err)
				}
				if ran {
					continue
				}
				select {
				case <-ctx.Done():
					return
				case <-q.wake:
				case <-t.C:
				}
			}
		}()
	}
	wg.Wait()
}
