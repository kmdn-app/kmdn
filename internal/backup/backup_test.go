package backup

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/kmdn-app/kmdn/internal/config"
)

func TestBackupRedactsCredentials(t *testing.T) {
	const input = `secret_key: encryption-secret
smtp: &mail
  host: smtp.example.com
  password: &credential mail-secret
assistant:
  provider: openai
  api_key: assistant-secret
  embeddings:
    api_key: *credential
telemetry:
  metrics_token: metrics-secret
  otlp_endpoint: https://collector:trace-secret@example.com
db:
  url: postgres://reader:database-secret@example.com/kmdn?sslmode=require&password=query-secret
`
	for _, include := range []bool{false, true} {
		t.Run(map[bool]string{false: "redacted", true: "include-secrets"}[include], func(t *testing.T) {
			dir := t.TempDir()
			cfgPath := filepath.Join(dir, "config.yaml")
			if err := os.WriteFile(cfgPath, []byte(input), 0600); err != nil {
				t.Fatal(err)
			}
			cfg := config.Defaults()
			cfg.DataDir = dir
			out := filepath.Join(dir, "backup.tar.gz")
			manifest, err := Backup(context.Background(), Options{Config: cfg, ConfigPath: cfgPath, Out: out, SkipDB: true, IncludeSecrets: include})
			if err != nil {
				t.Fatal(err)
			}
			if manifest.Secrets != include {
				t.Fatal("incorrect secrets flag")
			}
			tr, closeArchive, err := open(out)
			if err != nil {
				t.Fatal(err)
			}
			defer closeArchive()
			for {
				header, err := tr.Next()
				if err != nil {
					t.Fatal(err)
				}
				if header.Name != "kmdn.yaml" {
					continue
				}
				data, err := io.ReadAll(tr)
				if err != nil {
					t.Fatal(err)
				}
				if include {
					if string(data) != input {
						t.Fatal("include-secrets changed the config")
					}
					break
				}
				for _, secret := range []string{"encryption-secret", "mail-secret", "assistant-secret", "metrics-secret", "trace-secret", "database-secret", "query-secret"} {
					if strings.Contains(string(data), secret) {
						t.Errorf("backup retains %s", secret)
					}
				}
				var restored config.Config
				if err := yaml.Unmarshal(data, &restored); err != nil {
					t.Fatal(err)
				}
				if restored.SMTP.Host != "smtp.example.com" || restored.Assistant.Provider != "openai" || restored.DB.URL != "postgres://reader@example.com/kmdn?sslmode=require" {
					t.Fatal("redaction changed non-secret config")
				}
				break
			}
		})
	}
}

func TestRedactMergedAliases(t *testing.T) {
	data, err := redact([]byte("smtp: &mail\n  password: secret-value\nassistant:\n  <<: *mail\n  api_key: another-secret\n"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "secret-value") || strings.Contains(string(data), "another-secret") {
		t.Fatal("merged alias retains a credential")
	}
}
