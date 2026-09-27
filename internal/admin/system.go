package admin

import (
	"database/sql"
	"errors"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/kmdn-app/kmdn/internal/api"
	"github.com/kmdn-app/kmdn/internal/audit"
	"github.com/kmdn-app/kmdn/internal/auth"
	"github.com/kmdn-app/kmdn/internal/config"
	"github.com/kmdn-app/kmdn/internal/doctor"
	"github.com/kmdn-app/kmdn/internal/store"
	"github.com/kmdn-app/kmdn/internal/version"
)

// System serves Admin → System: version, database, jobs (with retry) and
// the doctor's checks (docs/specs/13-operations.md#admin-console).
type System struct {
	DB      *store.DB
	Config  config.Config
	Started time.Time
}

func (s *System) Routes(r chi.Router) {
	r.Group(func(r chi.Router) {
		r.Use(auth.RequireAdmin)
		r.Get("/admin/system", s.get)
		r.Get("/admin/system/doctor", s.doctor)
		r.Post("/admin/jobs/{id}/retry", s.retry)
	})
}

// FailedJob is a job that gave up.
type FailedJob struct {
	ID        string    `json:"id"`
	Kind      string    `json:"kind"`
	Attempts  int       `json:"attempts"`
	LastError string    `json:"last_error"`
	UpdatedAt time.Time `json:"updated_at"`
}

func (s *System) get(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	dialect := "sqlite"
	if s.DB.Dialect == store.Postgres {
		dialect = "postgres"
	}
	counts := map[string]map[string]int{}
	rows, err := store.Query(ctx, s.DB, `SELECT kind, status, COUNT(*) FROM jobs WHERE status IN ('pending', 'running', 'failed') GROUP BY kind, status`)
	if err != nil {
		api.Error(w, r, err)
		return
	}
	for rows.Next() {
		var kind, status string
		var n int
		if err := rows.Scan(&kind, &status, &n); err != nil {
			rows.Close()
			api.Error(w, r, err)
			return
		}
		if counts[kind] == nil {
			counts[kind] = map[string]int{}
		}
		counts[kind][status] = n
	}
	rows.Close()
	failed := []FailedJob{}
	rows, err = store.Query(ctx, s.DB, `SELECT id, kind, attempts, COALESCE(last_error, ''), updated_at FROM jobs WHERE status = 'failed' ORDER BY updated_at DESC LIMIT 50`)
	if err != nil {
		api.Error(w, r, err)
		return
	}
	for rows.Next() {
		var j FailedJob
		var at int64
		if err := rows.Scan(&j.ID, &j.Kind, &j.Attempts, &j.LastError, &at); err != nil {
			rows.Close()
			api.Error(w, r, err)
			return
		}
		j.UpdatedAt = store.FromMillis(at)
		failed = append(failed, j)
	}
	rows.Close()
	var repos, usersN int
	_ = store.QueryRow(ctx, s.DB, `SELECT COUNT(*) FROM repos`).Scan(&repos)
	_ = store.QueryRow(ctx, s.DB, `SELECT COUNT(*) FROM users`).Scan(&usersN)
	api.JSON(w, http.StatusOK, map[string]any{
		"version": version.Get(), "started_at": s.Started, "db": dialect, "data_dir": s.Config.DataDir,
		"repos": repos, "users": usersN, "jobs": counts, "failed_jobs": failed,
	})
}

func (s *System) doctor(w http.ResponseWriter, r *http.Request) {
	checks := (&doctor.Doctor{Config: s.Config, DB: s.DB, Network: true}).Run(r.Context())
	api.JSON(w, http.StatusOK, map[string]any{"checks": checks})
}

// retry puts a failed job back in the queue with a fresh attempt budget.
func (s *System) retry(w http.ResponseWriter, r *http.Request) {
	p, _ := auth.FromContext(r.Context())
	id := chi.URLParam(r, "id")
	var kind string
	err := store.QueryRow(r.Context(), s.DB, `SELECT kind FROM jobs WHERE id = ? AND status = 'failed'`, id).Scan(&kind)
	if errors.Is(err, sql.ErrNoRows) {
		api.Error(w, r, api.Err(http.StatusConflict, "not_failed", "This job isn't failed (it may have been retried already)."))
		return
	}
	if err != nil {
		api.Error(w, r, err)
		return
	}
	now := store.Millis(time.Now())
	if _, err := store.Exec(r.Context(), s.DB, `UPDATE jobs SET status = 'pending', attempts = 0, run_at = ?, updated_at = ? WHERE id = ? AND status = 'failed'`, now, now, id); err != nil {
		if store.IsUniqueViolation(err) {
			api.Error(w, r, api.Err(http.StatusConflict, "already_queued", "The same job is already queued."))
			return
		}
		api.Error(w, r, err)
		return
	}
	_ = audit.Write(r.Context(), s.DB, audit.Entry{ActorType: audit.ActorUser, ActorID: p.User.ID, Action: "job.retried", TargetType: "job", TargetID: id, Data: map[string]any{"kind": kind}})
	w.WriteHeader(http.StatusNoContent)
}
