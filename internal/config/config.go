// Package config loads kmdn configuration from a YAML file and KMDN_* environment
// variables. Every leaf field can be overridden by an env var whose name is the
// field's YAML path in upper case with dots replaced by underscores, prefixed with
// KMDN_ (server.base_url → KMDN_SERVER_BASE_URL).
package config

import (
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Config is the full server configuration. See docs/specs/13-operations.md.
type Config struct {
	Server    Server    `yaml:"server"`
	DataDir   string    `yaml:"data_dir"`
	SecretKey string    `yaml:"secret_key"`
	DB        DB        `yaml:"db"`
	SMTP      SMTP      `yaml:"smtp"`
	Auth      Auth      `yaml:"auth"`
	Assistant Assistant `yaml:"assistant"`
	Limits    Limits    `yaml:"limits"`
	Telemetry Telemetry `yaml:"telemetry"`
	Hooks     Hooks     `yaml:"hooks"`
}

// Hooks configures outgoing webhooks.
type Hooks struct {
	// AllowPrivate lets webhooks reach loopback and private-network
	// addresses (off: hooks can't probe the instance's own network).
	AllowPrivate bool `yaml:"allow_private"`
}

type Server struct {
	BaseURL        string   `yaml:"base_url"`
	Listen         string   `yaml:"listen"`
	TrustedProxies []string `yaml:"trusted_proxies"`
}

type DB struct {
	URL string `yaml:"url"`
}

type SMTP struct {
	Host     string `yaml:"host"`
	Port     int    `yaml:"port"`
	Username string `yaml:"username"`
	Password string `yaml:"password"`
	From     string `yaml:"from"`
	// Security is one of "starttls" (default), "tls" or "none".
	Security string `yaml:"security"`
}

type Auth struct {
	AutoJoinDomains []string      `yaml:"auto_join_domains"`
	SessionTTL      time.Duration `yaml:"session_ttl"`
}

type Assistant struct {
	Enabled bool `yaml:"enabled"`
}

type Limits struct {
	UploadMaxMB      int `yaml:"upload_max_mb"`
	WysiwygMaxFileMB int `yaml:"wysiwyg_max_file_mb"`
}

type Telemetry struct {
	Metrics      bool   `yaml:"metrics"`
	OTLPEndpoint string `yaml:"otlp_endpoint"`
	LogFormat    string `yaml:"log_format"`
	LogLevel     string `yaml:"log_level"`
}

// Defaults returns the configuration used when nothing else is set.
func Defaults() Config {
	return Config{
		Server:    Server{BaseURL: "http://localhost:8080", Listen: ":8080"},
		DataDir:   "./data",
		SMTP:      SMTP{Port: 587, Security: "starttls"},
		Auth:      Auth{SessionTTL: 30 * 24 * time.Hour},
		Assistant: Assistant{Enabled: true},
		Limits:    Limits{UploadMaxMB: 10, WysiwygMaxFileMB: 1},
		Telemetry: Telemetry{Metrics: true, LogFormat: "json", LogLevel: "info"},
	}
}

// Load reads defaults, then the YAML file at path (if it exists), then env vars.
// An empty path means "kmdn.yaml in the working directory, if present".
func Load(path string, env func(string) (string, bool)) (Config, error) {
	cfg := Defaults()
	explicit := path != ""
	if path == "" {
		path = "kmdn.yaml"
	}
	b, err := os.ReadFile(path)
	switch {
	case err == nil:
		dec := yaml.NewDecoder(strings.NewReader(os.Expand(string(b), func(k string) string {
			v, _ := env(k)
			return v
		})))
		dec.KnownFields(true)
		if err := dec.Decode(&cfg); err != nil && !errors.Is(err, io.EOF) {
			return cfg, fmt.Errorf("config: %s: %w", path, err)
		}
	case errors.Is(err, os.ErrNotExist) && !explicit:
	default:
		return cfg, fmt.Errorf("config: %w", err)
	}
	if err := applyEnv(reflect.ValueOf(&cfg).Elem(), "KMDN", env); err != nil {
		return cfg, err
	}
	if cfg.DB.URL == "" {
		cfg.DB.URL = "sqlite://" + filepath.ToSlash(filepath.Join(cfg.DataDir, "kmdn.db"))
	}
	return cfg, nil
}

// EnvName returns the environment variable for a YAML path such as "server.base_url".
func EnvName(path string) string {
	return "KMDN_" + strings.ToUpper(strings.ReplaceAll(path, ".", "_"))
}

func applyEnv(v reflect.Value, prefix string, env func(string) (string, bool)) error {
	t := v.Type()
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		tag := strings.Split(f.Tag.Get("yaml"), ",")[0]
		if tag == "" || tag == "-" {
			continue
		}
		name := prefix + "_" + strings.ToUpper(tag)
		fv := v.Field(i)
		if fv.Kind() == reflect.Struct {
			if err := applyEnv(fv, name, env); err != nil {
				return err
			}
			continue
		}
		raw, ok := env(name)
		if !ok {
			continue
		}
		if err := setValue(fv, raw); err != nil {
			return fmt.Errorf("config: %s: %w", name, err)
		}
	}
	return nil
}

func setValue(fv reflect.Value, raw string) error {
	if fv.Type() == reflect.TypeOf(time.Duration(0)) {
		d, err := time.ParseDuration(raw)
		if err != nil {
			return fmt.Errorf("expected a duration like 720h: %w", err)
		}
		fv.SetInt(int64(d))
		return nil
	}
	switch fv.Kind() {
	case reflect.String:
		fv.SetString(raw)
	case reflect.Int:
		n, err := strconv.Atoi(raw)
		if err != nil {
			return fmt.Errorf("expected an integer: %w", err)
		}
		fv.SetInt(int64(n))
	case reflect.Bool:
		b, err := strconv.ParseBool(raw)
		if err != nil {
			return fmt.Errorf("expected true or false: %w", err)
		}
		fv.SetBool(b)
	case reflect.Slice:
		var parts []string
		for _, p := range strings.Split(raw, ",") {
			if p = strings.TrimSpace(p); p != "" {
				parts = append(parts, p)
			}
		}
		fv.Set(reflect.ValueOf(parts))
	default:
		return fmt.Errorf("unsupported type %s", fv.Type())
	}
	return nil
}

// Validate reports every problem with the configuration, each with a hint on
// how to fix it.
func (c Config) Validate() error {
	var errs []error
	u, err := url.Parse(c.Server.BaseURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		errs = append(errs, fmt.Errorf("server.base_url %q must be an absolute http(s) URL, e.g. https://kmdn.example.com (env %s)", c.Server.BaseURL, EnvName("server.base_url")))
	}
	if c.Server.Listen == "" {
		errs = append(errs, fmt.Errorf("server.listen is empty; set it to an address like :8080"))
	}
	if c.DataDir == "" {
		errs = append(errs, fmt.Errorf("data_dir is empty; set it to a writable directory"))
	}
	if _, err := c.SecretKeyBytes(); err != nil {
		errs = append(errs, err)
	}
	if !strings.HasPrefix(c.DB.URL, "sqlite://") && !strings.HasPrefix(c.DB.URL, "postgres://") && !strings.HasPrefix(c.DB.URL, "postgresql://") {
		errs = append(errs, fmt.Errorf("db.url %q must start with sqlite:// or postgres://", c.DB.URL))
	}
	switch c.SMTP.Security {
	case "starttls", "tls", "none":
	default:
		errs = append(errs, fmt.Errorf("smtp.security %q must be starttls, tls or none", c.SMTP.Security))
	}
	switch c.Telemetry.LogFormat {
	case "json", "text":
	default:
		errs = append(errs, fmt.Errorf("telemetry.log_format %q must be json or text", c.Telemetry.LogFormat))
	}
	if c.Limits.UploadMaxMB <= 0 {
		errs = append(errs, fmt.Errorf("limits.upload_max_mb must be positive"))
	}
	return errors.Join(errs...)
}

// SecretKeyBytes decodes the 32-byte key-encryption key.
func (c Config) SecretKeyBytes() ([]byte, error) {
	if c.SecretKey == "" {
		return nil, fmt.Errorf("secret_key is not set; run `kmdn init` to generate one, or set %s to 32 random bytes in base64", EnvName("secret_key"))
	}
	k, err := base64.StdEncoding.DecodeString(c.SecretKey)
	if err != nil || len(k) != 32 {
		return nil, fmt.Errorf("secret_key must be 32 bytes encoded in base64 (got %d bytes); generate one with `kmdn init`", len(k))
	}
	return k, nil
}

// SMTPConfigured reports whether enough SMTP settings exist to send mail.
func (c Config) SMTPConfigured() bool {
	return c.SMTP.Host != "" && c.SMTP.From != ""
}
