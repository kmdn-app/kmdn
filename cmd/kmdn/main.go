// Command kmdn runs the kmdn server and its maintenance commands.
package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/kmdn-app/kmdn/internal/app"
	"github.com/kmdn-app/kmdn/internal/backup"
	"github.com/kmdn-app/kmdn/internal/config"
	"github.com/kmdn-app/kmdn/internal/doctor"
	"github.com/kmdn-app/kmdn/internal/gitmirror"
	"github.com/kmdn-app/kmdn/internal/lifecycle"
	"github.com/kmdn-app/kmdn/internal/orgs"
	"github.com/kmdn-app/kmdn/internal/secrets"
	"github.com/kmdn-app/kmdn/internal/store"
	"github.com/kmdn-app/kmdn/internal/telemetry"
	"github.com/kmdn-app/kmdn/internal/version"
)

const usage = `kmdn: collaborative markdown editing with a git backend

Usage:
  kmdn serve   [-config kmdn.yaml]   run the server
  kmdn init    [-config kmdn.yaml]   write a config skeleton with a new secret key
  kmdn migrate [status|up]           show or apply database migrations
  kmdn admin rotate-secret-key -new KEY
  kmdn admin rotate-org-key -org SLUG
                                     re-encrypt stored credentials with a new key
  kmdn admin resync-repos            queue a sync of every repository, cloning
                                     missing mirrors (after restoring the
                                     database some other way)
  kmdn doctor  [-offline]            check git, the data dir, the database, the
                                     secret key, SMTP, forges and the AI provider
  kmdn backup  [-out FILE.tar.zst] [-skip-db] [-include-secrets]
                                     back up the database, uploads and config
                                     (safe while the server runs)
  kmdn restore -in FILE [-force]     restore a backup (stop the server first)
  kmdn org export -org SLUG -out FILE.kmdn.tar.gz
                                     write an organization's archive
  kmdn org import -in FILE [-slug SLUG] [-into-default]
                                     load an organization's archive (from this
                                     or another kmdn); reconnect its repositories
  kmdn version                       print version information

Every config key can be set with an env var, e.g. KMDN_SERVER_BASE_URL.
`

func main() {
	// git invokes the kmdn binary as GIT_ASKPASS to read mirror credentials.
	if gitmirror.RunAskpass(os.Args[1:], os.Stdout) {
		return
	}
	if err := run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "kmdn:", err)
		os.Exit(1)
	}
}

func run(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return errors.New("missing command")
	}
	cmd, rest := args[0], args[1:]
	fs := flag.NewFlagSet(cmd, flag.ContinueOnError)
	fs.SetOutput(stderr)
	cfgPath := fs.String("config", "", "path to kmdn.yaml (default: ./kmdn.yaml if present)")
	switch cmd {
	case "serve":
		if err := fs.Parse(rest); err != nil {
			return err
		}
		return serve(*cfgPath, stderr)
	case "init":
		force := fs.Bool("force", false, "overwrite an existing file")
		if err := fs.Parse(rest); err != nil {
			return err
		}
		p := *cfgPath
		if p == "" {
			p = "kmdn.yaml"
		}
		return initConfig(p, *force, stdout)
	case "migrate":
		if err := fs.Parse(rest); err != nil {
			return err
		}
		sub := "up"
		if fs.NArg() > 0 {
			sub = fs.Arg(0)
		}
		return migrate(*cfgPath, sub, stdout)
	case "admin":
		switch {
		case len(rest) > 0 && rest[0] == "rotate-secret-key":
			newKey := fs.String("new", "", "new secret key (32 bytes, base64)")
			if err := fs.Parse(rest[1:]); err != nil {
				return err
			}
			return rotateSecretKey(*cfgPath, *newKey, stdout)
		case len(rest) > 0 && rest[0] == "rotate-org-key":
			slug := fs.String("org", "", "the organization's slug")
			if err := fs.Parse(rest[1:]); err != nil {
				return err
			}
			return rotateOrgKey(*cfgPath, *slug, stdout)
		case len(rest) > 0 && rest[0] == "resync-repos":
			if err := fs.Parse(rest[1:]); err != nil {
				return err
			}
			return resyncRepos(*cfgPath, stdout)
		}
		return errors.New("usage: kmdn admin rotate-secret-key -new <base64 32-byte key> | kmdn admin rotate-org-key -org <slug> | kmdn admin resync-repos")
	case "doctor":
		offline := fs.Bool("offline", false, "skip network checks (SMTP, forges, AI provider)")
		if err := fs.Parse(rest); err != nil {
			return err
		}
		return runDoctor(*cfgPath, !*offline, stdout)
	case "backup":
		out := fs.String("out", "", "archive to write (.tar.zst, or .tar.gz); default kmdn-backup-<time>.tar.zst")
		skipDB := fs.Bool("skip-db", false, "leave the database out (Postgres: back it up with pg_dump)")
		secretsToo := fs.Bool("include-secrets", false, "keep secret_key and passwords in the config copy")
		if err := fs.Parse(rest); err != nil {
			return err
		}
		return runBackup(*cfgPath, *out, *skipDB, *secretsToo, stdout)
	case "restore":
		in := fs.String("in", "", "archive to restore")
		force := fs.Bool("force", false, "replace an existing database")
		if err := fs.Parse(rest); err != nil {
			return err
		}
		if *in == "" {
			return errors.New("usage: kmdn restore -in <backup.tar.zst> [-force]")
		}
		return runRestore(*cfgPath, *in, *force, stdout)
	case "org":
		if len(rest) == 0 || (rest[0] != "export" && rest[0] != "import") {
			return errors.New("usage: kmdn org export -org SLUG -out FILE | kmdn org import -in FILE [-slug SLUG] [-into-default]")
		}
		slug := fs.String("org", "", "the organization's slug (export)")
		file := fs.String("out", "", "archive to write (export)")
		in := fs.String("in", "", "archive to read (import)")
		newSlug := fs.String("slug", "", "the slug to give it (import)")
		intoDefault := fs.Bool("into-default", false, "load it into the default org (single mode, import)")
		if err := fs.Parse(rest[1:]); err != nil {
			return err
		}
		return orgArchive(*cfgPath, rest[0], *slug, *file, *in, *newSlug, *intoDefault, stdout)
	case "version", "--version", "-v":
		v := version.Get()
		fmt.Fprintf(stdout, "kmdn %s (%s) %s\n", v.Version, v.Commit, v.Go)
		return nil
	case "help", "-h", "--help":
		fmt.Fprint(stdout, usage)
		return nil
	default:
		fmt.Fprint(stderr, usage)
		return fmt.Errorf("unknown command %q", cmd)
	}
}

func serve(cfgPath string, logOut io.Writer) error {
	cfg, err := config.Load(cfgPath, os.LookupEnv)
	if err != nil {
		return err
	}
	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("invalid configuration:\n%w", err)
	}
	log := telemetry.NewLogger(logOut, cfg.Telemetry.LogFormat, cfg.Telemetry.LogLevel)
	if err := os.MkdirAll(cfg.DataDir, 0o750); err != nil {
		return fmt.Errorf("data_dir %s: %w", cfg.DataDir, err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	a, err := app.New(ctx, cfg, log)
	if err != nil {
		return err
	}
	defer func() { _ = a.Close() }()
	return a.Run(ctx)
}

func migrate(cfgPath, sub string, out io.Writer) error {
	cfg, err := config.Load(cfgPath, os.LookupEnv)
	if err != nil {
		return err
	}
	ctx := context.Background()
	db, err := store.Open(ctx, cfg.DB.URL)
	if err != nil {
		return err
	}
	defer db.Close()
	switch sub {
	case "up":
		n, err := db.Migrate(ctx)
		if err != nil {
			return err
		}
		fmt.Fprintf(out, "Applied %d migration(s).\n", n)
		return nil
	case "status":
		st, err := db.Status(ctx)
		if err != nil {
			return err
		}
		for _, m := range st {
			state := "pending"
			if m.AppliedAt != nil {
				state = "applied " + m.AppliedAt.Format("2006-01-02 15:04:05Z")
			}
			fmt.Fprintf(out, "%04d  %-32s %s\n", m.Version, m.Name, state)
		}
		return nil
	default:
		return fmt.Errorf("unknown migrate subcommand %q (use status or up)", sub)
	}
}

// configPath is the -config flag, or ./kmdn.yaml when it exists.
func configPath(p string) string {
	if p != "" {
		return p
	}
	if _, err := os.Stat("kmdn.yaml"); err == nil {
		return "kmdn.yaml"
	}
	return ""
}

func runDoctor(cfgPath string, network bool, out io.Writer) error {
	cfg, err := config.Load(cfgPath, os.LookupEnv)
	if err != nil {
		return err
	}
	checks := (&doctor.Doctor{Config: cfg, Network: network}).Run(context.Background())
	marks := map[string]string{doctor.OK: "ok  ", doctor.Warn: "warn", doctor.Fail: "FAIL"}
	for _, c := range checks {
		fmt.Fprintf(out, "%s  %-22s %s\n", marks[c.Status], c.Name, c.Detail)
	}
	if doctor.Failed(checks) {
		return errors.New("some checks failed")
	}
	return nil
}

func runBackup(cfgPath, out string, skipDB, withSecrets bool, w io.Writer) error {
	cfg, err := config.Load(cfgPath, os.LookupEnv)
	if err != nil {
		return err
	}
	if out == "" {
		out = "kmdn-backup-" + time.Now().UTC().Format("20060102-150405") + ".tar.zst"
	}
	m, err := backup.Backup(context.Background(), backup.Options{Config: cfg, ConfigPath: configPath(cfgPath), Out: out, SkipDB: skipDB, IncludeSecrets: withSecrets})
	if err != nil {
		return err
	}
	fmt.Fprintf(w, "Wrote %s: database %s, %d upload(s), config %v (secrets included: %v).\n", out, orNone(m.DB), m.Uploads, m.Config, m.Secrets)
	if !withSecrets {
		fmt.Fprintln(w, "The secret key isn't in the backup: keep it safe, stored credentials can't be decrypted without it.")
	}
	return nil
}

func orNone(s string) string {
	if s == "" {
		return "skipped"
	}
	return s
}

func runRestore(cfgPath, in string, force bool, w io.Writer) error {
	cfg, err := config.Load(cfgPath, os.LookupEnv)
	if err != nil {
		return err
	}
	r, err := backup.Restore(context.Background(), backup.RestoreOptions{Config: cfg, In: in, Force: force})
	if err != nil {
		return err
	}
	fmt.Fprintf(w, "Restored %s (kmdn %s, %s): database %s, %d upload(s).\n", in, r.Manifest.Version, r.Manifest.CreatedAt.Format(time.RFC3339), orNone(r.Manifest.DB), r.Manifest.Uploads)
	if r.ConfigCopy != "" {
		fmt.Fprintf(w, "The backed-up config is in %s (your current config is untouched).\n", r.ConfigCopy)
	}
	fmt.Fprintf(w, "%d repository mirror(s) will be cloned again when kmdn starts.\n", r.Repos)
	return nil
}

func rotateSecretKey(cfgPath, newKey string, out io.Writer) error {
	cfg, err := config.Load(cfgPath, os.LookupEnv)
	if err != nil {
		return err
	}
	kek, err := cfg.SecretKeyBytes()
	if err != nil {
		return err
	}
	next, err := base64.StdEncoding.DecodeString(newKey)
	if err != nil || len(next) != 32 {
		return errors.New("-new must be 32 random bytes in base64, e.g. from `head -c32 /dev/urandom | base64`")
	}
	ctx := context.Background()
	db, err := store.Open(ctx, cfg.DB.URL)
	if err != nil {
		return err
	}
	defer db.Close()
	sec, err := secrets.New(db, kek)
	if err != nil {
		return err
	}
	n, err := sec.RotateKEK(ctx, next)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "Re-encrypted %d secret(s). Set secret_key (or %s) to the new key before restarting kmdn.\n", n, config.EnvName("secret_key"))
	return nil
}

// rotateOrgKey gives one org a new key and re-wraps its secrets.
// resyncRepos queues a sync of every repository (the running server picks
// the jobs up).
func resyncRepos(cfgPath string, out io.Writer) error {
	cfg, err := config.Load(cfgPath, os.LookupEnv)
	if err != nil {
		return err
	}
	ctx := context.Background()
	db, err := store.Open(ctx, cfg.DB.URL)
	if err != nil {
		return err
	}
	defer db.Close()
	n, err := backup.ResyncRepos(ctx, db)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "Queued a sync of %d repositories; missing mirrors are cloned again as they run.\n", n)
	return nil
}

func rotateOrgKey(cfgPath, slug string, out io.Writer) error {
	if slug == "" {
		return errors.New("-org is required")
	}
	cfg, err := config.Load(cfgPath, os.LookupEnv)
	if err != nil {
		return err
	}
	kek, err := cfg.SecretKeyBytes()
	if err != nil {
		return err
	}
	ctx := context.Background()
	db, err := store.Open(ctx, cfg.DB.URL)
	if err != nil {
		return err
	}
	defer db.Close()
	o, err := orgs.BySlug(ctx, db, slug)
	if err != nil {
		return fmt.Errorf("organization %q: %w", slug, err)
	}
	sec, err := secrets.New(db, kek)
	if err != nil {
		return err
	}
	n, err := sec.RotateOrgKey(ctx, o.ID)
	if errors.Is(err, secrets.ErrNoOrgKey) {
		fmt.Fprintf(out, "%s has no secrets yet; nothing to rotate.\n", slug)
		return nil
	}
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "Gave %s a new key and re-encrypted %d secret(s).\n", slug, n)
	return nil
}

func initConfig(path string, force bool, out io.Writer) error {
	if _, err := os.Stat(path); err == nil && !force {
		return fmt.Errorf("%s already exists (use -force to overwrite)", path)
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return err
	}
	body := fmt.Sprintf(`# kmdn configuration. Every key can be overridden with KMDN_<PATH>,
# e.g. KMDN_SERVER_BASE_URL. See docs/specs/13-operations.md.
server:
  base_url: http://localhost:8080
  listen: :8080
data_dir: ./data
# Encrypts stored credentials. Keep it safe: without it, forge, SMTP and
# AI provider secrets must be re-entered.
secret_key: %s
db:
  url: sqlite://./data/kmdn.db
smtp:
  host: ""
  port: 587
  security: starttls
  username: ""
  password: ""
  from: ""
telemetry:
  log_format: json
  log_level: info
`, base64.StdEncoding.EncodeToString(key))
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		return err
	}
	fmt.Fprintf(out, "Wrote %s with a new secret key.\n", path)
	return nil
}

// orgArchive exports or imports an organization's archive.
func orgArchive(cfgPath, op, slug, out, in, newSlug string, intoDefault bool, stdout io.Writer) error {
	cfg, err := config.Load(cfgPath, os.LookupEnv)
	if err != nil {
		return err
	}
	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("invalid configuration:\n%w", err)
	}
	ctx := context.Background()
	a, err := app.New(ctx, cfg, telemetry.NewLogger(io.Discard, cfg.Telemetry.LogFormat, cfg.Telemetry.LogLevel))
	if err != nil {
		return err
	}
	defer func() { _ = a.Close() }()
	if op == "export" {
		if slug == "" || out == "" {
			return errors.New("usage: kmdn org export -org SLUG -out FILE")
		}
		o, err := orgs.BySlug(ctx, a.DB, slug)
		if err != nil {
			return fmt.Errorf("organization %q: %w", slug, err)
		}
		f, err := os.OpenFile(out, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err != nil {
			return err
		}
		m, err := a.Lifecycle.Export(ctx, o.ID, f)
		if cerr := f.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			_ = os.Remove(out)
			return err
		}
		rows := 0
		for _, n := range m.Rows {
			rows += n
		}
		fmt.Fprintf(stdout, "Exported %s: %d rows, %d uploads → %s\n", slug, rows, m.Uploads, out)
		return nil
	}
	if in == "" {
		return errors.New("usage: kmdn org import -in FILE [-slug SLUG] [-into-default]")
	}
	f, err := os.Open(in)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	res, err := a.Lifecycle.Import(ctx, f, lifecycle.ImportOptions{Slug: newSlug, IntoDefault: intoDefault})
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "Imported %s (%d accounts created, %d forge hosts added, %d uploads). Reconnect its repositories' credentials in the org console; they sync once connected.\n", res.Slug, res.Users, res.Hosts, res.Uploads)
	return nil
}
