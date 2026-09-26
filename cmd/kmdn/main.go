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

	"github.com/kmdn-app/kmdn/internal/app"
	"github.com/kmdn-app/kmdn/internal/config"
	"github.com/kmdn-app/kmdn/internal/gitmirror"
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
                                     re-encrypt stored credentials with a new key
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
		if len(rest) == 0 || rest[0] != "rotate-secret-key" {
			return errors.New("usage: kmdn admin rotate-secret-key -new <base64 32-byte key>")
		}
		newKey := fs.String("new", "", "new secret key (32 bytes, base64)")
		if err := fs.Parse(rest[1:]); err != nil {
			return err
		}
		return rotateSecretKey(*cfgPath, *newKey, stdout)
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
