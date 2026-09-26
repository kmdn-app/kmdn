// Package mail sends kmdn's transactional email (sign-in links, invites).
// kmdn never sends notification email. SMTP settings come from the config file
// when set there, otherwise from the setup wizard (instance settings + an
// encrypted password). Host "log" prints messages to the server log, which is
// handy for local development.
package mail

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	gomail "github.com/wneessen/go-mail"

	"github.com/kmdn-app/kmdn/internal/config"
	"github.com/kmdn-app/kmdn/internal/secrets"
	"github.com/kmdn-app/kmdn/internal/settings"
	"github.com/kmdn-app/kmdn/internal/store"
)

// Message is one email.
type Message struct {
	To      string
	Subject string
	Text    string
	HTML    string
}

// Sender delivers a message.
type Sender interface {
	Send(ctx context.Context, m Message) error
}

// SMTPSettings are the values stored by the setup wizard / admin console.
type SMTPSettings struct {
	Host             string `json:"host"`
	Port             int    `json:"port"`
	Security         string `json:"security"` // starttls | tls | none
	Username         string `json:"username"`
	From             string `json:"from"`
	PasswordSecretID string `json:"password_secret_id,omitempty"`
}

// SettingsKey is the instance_settings key for SMTP.
const SettingsKey = "smtp"

// ErrNotConfigured means no SMTP settings exist yet.
var ErrNotConfigured = errors.New("mail: SMTP is not configured")

// Service resolves SMTP settings on each send (they can change at runtime).
type Service struct {
	cfg     config.Config
	db      *store.DB
	secrets *secrets.Store
	log     *slog.Logger
}

func NewService(cfg config.Config, db *store.DB, sec *secrets.Store, log *slog.Logger) *Service {
	return &Service{cfg: cfg, db: db, secrets: sec, log: log}
}

// FromConfig reports whether SMTP is fixed by the config file (the UI shows it locked).
func (s *Service) FromConfig() bool { return s.cfg.SMTPConfigured() }

// Resolve returns the effective settings and password.
func (s *Service) Resolve(ctx context.Context) (SMTPSettings, string, error) {
	if s.cfg.SMTPConfigured() {
		c := s.cfg.SMTP
		return SMTPSettings{Host: c.Host, Port: c.Port, Security: c.Security, Username: c.Username, From: c.From}, c.Password, nil
	}
	var st SMTPSettings
	if err := settings.Get(ctx, s.db, SettingsKey, &st); err != nil {
		if settings.IsNotFound(err) {
			return st, "", ErrNotConfigured
		}
		return st, "", err
	}
	pw := ""
	if st.PasswordSecretID != "" {
		b, err := s.secrets.Get(ctx, s.db, st.PasswordSecretID)
		if err != nil {
			return st, "", err
		}
		pw = string(b)
	}
	return st, pw, nil
}

// Configured reports whether a send can be attempted.
func (s *Service) Configured(ctx context.Context) bool {
	_, _, err := s.Resolve(ctx)
	return err == nil
}

// Send delivers m with the current settings.
func (s *Service) Send(ctx context.Context, m Message) error {
	st, pw, err := s.Resolve(ctx)
	if err != nil {
		return err
	}
	return SendWith(ctx, st, pw, m, s.log)
}

// SendWith delivers m with explicit settings (used by "Send test email").
func SendWith(ctx context.Context, st SMTPSettings, password string, m Message, log *slog.Logger) error {
	if st.Host == "log" {
		log.Info("email (smtp.host=log, not sent)", "to", m.To, "subject", m.Subject, "text", m.Text)
		return nil
	}
	msg := gomail.NewMsg()
	if err := msg.From(st.From); err != nil {
		return fmt.Errorf("from address %q: %w", st.From, err)
	}
	if err := msg.To(m.To); err != nil {
		return fmt.Errorf("to address %q: %w", m.To, err)
	}
	msg.Subject(m.Subject)
	msg.SetBodyString(gomail.TypeTextPlain, m.Text)
	if m.HTML != "" {
		msg.AddAlternativeString(gomail.TypeTextHTML, m.HTML)
	}
	opts := []gomail.Option{gomail.WithPort(st.Port), gomail.WithTimeout(15 * time.Second)}
	switch st.Security {
	case "tls":
		opts = append(opts, gomail.WithSSLPort(false))
	case "none":
		opts = append(opts, gomail.WithTLSPolicy(gomail.NoTLS))
	default:
		opts = append(opts, gomail.WithTLSPolicy(gomail.TLSMandatory))
	}
	if st.Username != "" {
		opts = append(opts, gomail.WithSMTPAuth(gomail.SMTPAuthAutoDiscover), gomail.WithUsername(st.Username), gomail.WithPassword(password))
	}
	c, err := gomail.NewClient(st.Host, opts...)
	if err != nil {
		return err
	}
	return c.DialAndSendWithContext(ctx, msg)
}

// Capture is a Sender for tests that records messages.
type Capture struct {
	mu   sync.Mutex
	Sent []Message
}

func (c *Capture) Send(_ context.Context, m Message) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.Sent = append(c.Sent, m)
	return nil
}

// Last returns the most recent message.
func (c *Capture) Last() (Message, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.Sent) == 0 {
		return Message{}, false
	}
	return c.Sent[len(c.Sent)-1], true
}
