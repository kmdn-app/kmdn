package mail

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/kmdn-app/kmdn/internal/config"
	"github.com/kmdn-app/kmdn/internal/secrets"
	"github.com/kmdn-app/kmdn/internal/settings"
	"github.com/kmdn-app/kmdn/internal/storetest"
)

func TestResolveFromSettingsWithEncryptedPassword(t *testing.T) {
	db := storetest.Open(t)
	ctx := context.Background()
	sec, _ := secrets.New(db, bytes.Repeat([]byte{7}, 32))
	svc := NewService(config.Defaults(), db, sec, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if _, _, err := svc.Resolve(ctx); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("expected not configured, got %v", err)
	}
	id, _ := sec.Put(ctx, db, "smtp_password", []byte("pw"))
	_ = settings.Set(ctx, db, SettingsKey, SMTPSettings{Host: "smtp.example.com", Port: 587, Security: "starttls", From: "Docs <docs@example.com>", PasswordSecretID: id})
	st, pw, err := svc.Resolve(ctx)
	if err != nil || st.Host != "smtp.example.com" || pw != "pw" {
		t.Fatalf("%+v %q %v", st, pw, err)
	}
}

func TestLogHostDoesNotDial(t *testing.T) {
	var buf bytes.Buffer
	err := SendWith(context.Background(), SMTPSettings{Host: "log"}, "", SignIn("Northwind Docs", "a@b.c", "https://x/auth/verify?token=t", "123456", 15), slog.New(slog.NewTextHandler(&buf, nil)))
	if err != nil || !strings.Contains(buf.String(), "123456") {
		t.Fatalf("%v %s", err, buf.String())
	}
}

func TestTemplatesEscape(t *testing.T) {
	m := Invite("kmdn", "a@b.c", "<script>", "repo", "https://x", 7)
	if strings.Contains(m.HTML, "<script>") {
		t.Fatal("inviter not escaped")
	}
}

// A filter can change a message or drop it; sent messages carry its name.
func TestFilter(t *testing.T) {
	var buf bytes.Buffer
	s := &Service{cfg: config.Config{SMTP: config.SMTP{Host: "log", From: "Docs <docs@example.com>"}}, log: slog.New(slog.NewTextHandler(&buf, nil))}
	s.Filter = func(_ context.Context, m *Message) error {
		if m.To == "bounced@example.com" {
			return ErrSkip
		}
		if m.Kind == KindInvite && m.OrgID == "org_busy" {
			return errors.New("too many invitations today")
		}
		m.FromName = "Acme via kmdn"
		m.Text += "\nfooter"
		return nil
	}
	if err := s.Send(context.Background(), Message{To: "bounced@example.com", Kind: KindSignIn}); err != nil || strings.Contains(buf.String(), "not sent") {
		t.Fatalf("skip: %v %s", err, buf.String())
	}
	if err := s.Send(context.Background(), Message{To: "a@example.com", Kind: KindInvite, OrgID: "org_busy"}); err == nil {
		t.Fatal("refused message sent")
	}
	if err := s.Send(context.Background(), Invite("Acme", "a@example.com", "Olga", "Acme", "https://x/invite/t", 7)); err != nil {
		t.Fatal(err)
	}
	if out := buf.String(); !strings.Contains(out, `from_name="Acme via kmdn"`) || !strings.Contains(out, `footer`) {
		t.Fatalf("filtered message: %s", out)
	}
}
