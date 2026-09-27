// Package doctor checks an instance's environment: git, the data dir, the
// database and its migrations, the secret key, SMTP, forges and the AI
// provider (docs/specs/13-operations.md#cli).
package doctor

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/kmdn-app/kmdn/internal/config"
	"github.com/kmdn-app/kmdn/internal/gitmirror"
	"github.com/kmdn-app/kmdn/internal/llm"
	"github.com/kmdn-app/kmdn/internal/mail"
	"github.com/kmdn-app/kmdn/internal/secrets"
	"github.com/kmdn-app/kmdn/internal/store"
)

// Statuses.
const (
	OK   = "ok"
	Warn = "warn"
	Fail = "fail"
)

// Check is one result.
type Check struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	Detail string `json:"detail"`
}

// Doctor runs the checks. DB may be nil (it's opened from the config).
type Doctor struct {
	Config config.Config
	DB     *store.DB
	HTTP   *http.Client
	Git    *gitmirror.Git
	// Network skips forge, SMTP and AI reachability when false.
	Network bool
}

// Failed reports whether any check failed.
func Failed(cs []Check) bool {
	for _, c := range cs {
		if c.Status == Fail {
			return true
		}
	}
	return false
}

// Run runs every check, in a stable order.
func (d *Doctor) Run(ctx context.Context) []Check {
	if d.HTTP == nil {
		d.HTTP = &http.Client{Timeout: 5 * time.Second}
	}
	if d.Git == nil {
		d.Git = &gitmirror.Git{}
	}
	out := []Check{d.git(ctx), d.dataDir(), d.baseURL()}
	db := d.DB
	if db == nil {
		var err error
		db, err = store.Open(ctx, d.Config.DB.URL)
		if err != nil {
			return append(out, Check{"database", Fail, err.Error()})
		}
		defer db.Close()
	}
	out = append(out, d.database(ctx, db))
	sec, c := d.secretKey(ctx, db)
	out = append(out, c, d.jobs(ctx, db))
	if d.Network {
		out = append(out, d.smtp(ctx, db, sec))
		out = append(out, d.forges(ctx, db)...)
		out = append(out, d.ai(ctx, db, sec))
	}
	return out
}

func (d *Doctor) git(ctx context.Context) Check {
	v, err := d.Git.Version(ctx)
	if err != nil {
		return Check{"git", Fail, "git isn't on PATH: kmdn needs git 2.40 or later."}
	}
	if err := d.Git.CheckVersion(ctx); err != nil {
		return Check{"git", Fail, err.Error()}
	}
	return Check{"git", OK, "git " + v}
}

func (d *Doctor) dataDir() Check {
	dir := d.Config.DataDir
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return Check{"data_dir", Fail, fmt.Sprintf("%s can't be created: %v", dir, err)}
	}
	f, err := os.CreateTemp(dir, ".doctor-*")
	if err != nil {
		return Check{"data_dir", Fail, fmt.Sprintf("%s isn't writable: %v", dir, err)}
	}
	_ = f.Close()
	_ = os.Remove(f.Name())
	abs, _ := filepath.Abs(dir)
	free, ok := freeBytes(dir)
	if !ok {
		return Check{"data_dir", OK, abs + " is writable"}
	}
	msg := fmt.Sprintf("%s is writable, %s free", abs, humanBytes(free))
	if free < 1<<30 {
		return Check{"data_dir", Warn, msg + ": less than 1 GB left"}
	}
	return Check{"data_dir", OK, msg}
}

func humanBytes(n uint64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.0f MB", float64(n)/(1<<20))
	default:
		return fmt.Sprintf("%d KB", n>>10)
	}
}

func (d *Doctor) baseURL() Check {
	u, err := url.Parse(d.Config.Server.BaseURL)
	if err != nil || u.Host == "" {
		return Check{"base_url", Fail, "server.base_url isn't a URL: sign-in links and webhooks need it."}
	}
	host := u.Hostname()
	local := host == "localhost" || strings.HasSuffix(host, ".localhost") || net.ParseIP(host).IsLoopback()
	if u.Scheme != "https" && !local {
		return Check{"base_url", Warn, d.Config.Server.BaseURL + " isn't https: cookies and sign-in links travel in clear text."}
	}
	return Check{"base_url", OK, d.Config.Server.BaseURL}
}

func (d *Doctor) database(ctx context.Context, db *store.DB) Check {
	if err := db.PingContext(ctx); err != nil {
		return Check{"database", Fail, err.Error()}
	}
	kind := "SQLite"
	if db.Dialect == store.Postgres {
		kind = "Postgres"
	}
	st, err := db.Status(ctx)
	if err != nil {
		return Check{"database", Fail, err.Error()}
	}
	pending := 0
	for _, m := range st {
		if m.AppliedAt == nil {
			pending++
		}
	}
	if pending > 0 {
		return Check{"database", Warn, fmt.Sprintf("%s, %d migration(s) pending: run kmdn migrate up (serve applies them too).", kind, pending)}
	}
	return Check{"database", OK, fmt.Sprintf("%s, %d migrations applied", kind, len(st))}
}

func (d *Doctor) secretKey(ctx context.Context, db *store.DB) (*secrets.Store, Check) {
	kek, err := d.Config.SecretKeyBytes()
	if err != nil {
		return nil, Check{"secret_key", Fail, err.Error()}
	}
	sec, err := secrets.New(db, kek)
	if err != nil {
		return nil, Check{"secret_key", Fail, err.Error()}
	}
	rows, err := store.Query(ctx, db, `SELECT id, kind FROM secrets ORDER BY created_at`)
	if err != nil {
		return sec, Check{"secret_key", Fail, err.Error()}
	}
	type ref struct{ id, kind string }
	var all []ref
	for rows.Next() {
		var r ref
		if err := rows.Scan(&r.id, &r.kind); err != nil {
			rows.Close()
			return sec, Check{"secret_key", Fail, err.Error()}
		}
		all = append(all, r)
	}
	rows.Close()
	if len(all) == 0 {
		return sec, Check{"secret_key", OK, "valid (no stored secrets yet)"}
	}
	var bad []string
	for _, r := range all {
		if _, err := sec.Get(ctx, db, r.id); err != nil {
			bad = append(bad, r.kind)
		}
	}
	if len(bad) == len(all) {
		return sec, Check{"secret_key", Fail, "no stored secret can be decrypted: is this the secret_key the instance was set up with?"}
	}
	if len(bad) > 0 {
		return sec, Check{"secret_key", Fail, fmt.Sprintf("%d of %d stored secrets can't be decrypted (%s): re-enter them in the admin console", len(bad), len(all), strings.Join(bad, ", "))}
	}
	return sec, Check{"secret_key", OK, fmt.Sprintf("decrypts all %d stored secrets", len(all))}
}

func (d *Doctor) jobs(ctx context.Context, db *store.DB) Check {
	rows, err := store.Query(ctx, db, `SELECT kind, COUNT(*) FROM jobs WHERE status = 'failed' GROUP BY kind ORDER BY kind`)
	if err != nil {
		return Check{"jobs", Fail, err.Error()}
	}
	defer rows.Close()
	var parts []string
	for rows.Next() {
		var k string
		var n int
		if err := rows.Scan(&k, &n); err != nil {
			return Check{"jobs", Fail, err.Error()}
		}
		parts = append(parts, fmt.Sprintf("%s ×%d", k, n))
	}
	if len(parts) > 0 {
		return Check{"jobs", Warn, "failed jobs: " + strings.Join(parts, ", ") + " (retry them in Admin → System)"}
	}
	return Check{"jobs", OK, "no failed jobs"}
}

func (d *Doctor) smtp(ctx context.Context, db *store.DB, sec *secrets.Store) Check {
	st, _, err := mail.NewService(d.Config, db, sec, slog.New(slog.DiscardHandler)).Resolve(ctx)
	if errors.Is(err, mail.ErrNotConfigured) {
		return Check{"smtp", Warn, "SMTP isn't set up: sign-in links can't be emailed."}
	}
	if err != nil {
		return Check{"smtp", Fail, err.Error()}
	}
	if st.Host == "log" {
		return Check{"smtp", Warn, "smtp.host is log: emails are written to the log, not sent."}
	}
	addr := net.JoinHostPort(st.Host, strconv.Itoa(st.Port))
	conn, err := (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, "tcp", addr)
	if err != nil {
		return Check{"smtp", Fail, fmt.Sprintf("%s isn't reachable: %v", addr, err)}
	}
	_ = conn.Close()
	return Check{"smtp", OK, addr + " is reachable"}
}

func (d *Doctor) reach(ctx context.Context, u string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	res, err := d.HTTP.Do(req)
	if err != nil {
		return err
	}
	res.Body.Close()
	// Any answer means the host is up, except a proxy saying it isn't.
	switch res.StatusCode {
	case http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return fmt.Errorf("HTTP %d", res.StatusCode)
	}
	return nil
}

func (d *Doctor) forges(ctx context.Context, db *store.DB) []Check {
	rows, err := store.Query(ctx, db, `SELECT kind, display_name, base_url, api_url FROM forge_hosts ORDER BY display_name`)
	if err != nil {
		return []Check{{"forges", Fail, err.Error()}}
	}
	type host struct{ kind, name, base, api string }
	var hosts []host
	for rows.Next() {
		var h host
		if err := rows.Scan(&h.kind, &h.name, &h.base, &h.api); err != nil {
			rows.Close()
			return []Check{{"forges", Fail, err.Error()}}
		}
		hosts = append(hosts, h)
	}
	rows.Close()
	var out []Check
	for _, h := range hosts {
		target := h.api
		if target == "" {
			target = h.base
		}
		if h.kind == "github" && target == "" {
			target = "https://api.github.com"
		}
		if target == "" || !strings.HasPrefix(target, "http") {
			continue // plain git remotes are checked by their fetches
		}
		name := "forge: " + h.name
		if err := d.reach(ctx, target); err != nil {
			out = append(out, Check{name, Fail, fmt.Sprintf("%s isn't reachable: %v", target, err)})
		} else {
			out = append(out, Check{name, OK, target + " is reachable"})
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func (d *Doctor) ai(ctx context.Context, db *store.DB, sec *secrets.Store) Check {
	st, err := (&llm.Service{DB: db, Secrets: sec}).Settings(ctx)
	if err != nil {
		return Check{"ai", Fail, err.Error()}
	}
	if st.Provider == "" {
		return Check{"ai", OK, "no AI provider (the assistant is off)"}
	}
	target := st.BaseURL
	if target == "" && st.Provider == llm.ProviderAnthropic {
		target = "https://api.anthropic.com"
	}
	if err := d.reach(ctx, target); err != nil {
		return Check{"ai", Fail, fmt.Sprintf("%s isn't reachable: %v", target, err)}
	}
	if st.Check == nil || !st.Check.OK {
		msg := "the last capability check didn't pass"
		if st.Check != nil {
			msg += ": " + st.Check.Message
		}
		return Check{"ai", Warn, msg + " (Admin → AI provider → Save and check)"}
	}
	return Check{"ai", OK, fmt.Sprintf("%s reachable; last check passed %s", target, st.Check.At.Format(time.DateOnly))}
}
