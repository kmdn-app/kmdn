package notify

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"sync"
	"time"

	webpush "github.com/SherClockHolmes/webpush-go"

	"github.com/kmdn-app/kmdn/internal/ids"
	"github.com/kmdn-app/kmdn/internal/secrets"
	"github.com/kmdn-app/kmdn/internal/settings"
	"github.com/kmdn-app/kmdn/internal/store"
)

// Subscription is a browser's push endpoint.
type Subscription struct {
	ID       string `json:"id"`
	Endpoint string `json:"endpoint"`
	P256dh   string `json:"p256dh"`
	Auth     string `json:"auth"`
}

// Message is what a push shows.
type Message struct {
	Title string `json:"title"`
	Body  string `json:"body"`
	URL   string `json:"url"`
	Tag   string `json:"tag,omitempty"` // same tag replaces the previous notification
}

// Sender delivers one push; it returns the push service's status code.
type Sender func(ctx context.Context, sub Subscription, payload []byte, keys VAPID) (int, error)

// VAPID is the instance's application server key pair.
type VAPID struct {
	Public  string
	Private string
}

const vapidKey = "push.vapid"

// Pusher sends Web Push notifications with the instance's VAPID keys,
// created on first use (the private key lives in the secrets store).
type Pusher struct {
	DB      *store.DB
	Secrets *secrets.Store
	// Subject identifies the sender to push services (mailto: or https: URL).
	Subject string
	// Transport defaults to Web Push over HTTP; tests replace it.
	Transport Sender
	Log       *slog.Logger

	mu   sync.Mutex
	keys *VAPID
}

type storedVAPID struct {
	Public     string `json:"public"`
	PrivateRef string `json:"private_ref"`
}

// Keys returns the VAPID key pair, generating it the first time.
func (p *Pusher) Keys(ctx context.Context) (VAPID, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.keys != nil {
		return *p.keys, nil
	}
	var st storedVAPID
	err := settings.Get(ctx, p.DB, vapidKey, &st)
	if settings.IsNotFound(err) {
		priv, pub, err := webpush.GenerateVAPIDKeys()
		if err != nil {
			return VAPID{}, err
		}
		ref, err := p.Secrets.Put(ctx, p.DB, "push_vapid", []byte(priv))
		if err != nil {
			return VAPID{}, err
		}
		st = storedVAPID{Public: pub, PrivateRef: ref}
		if err := settings.Set(ctx, p.DB, vapidKey, st); err != nil {
			return VAPID{}, err
		}
	} else if err != nil {
		return VAPID{}, err
	}
	priv, err := p.Secrets.Get(ctx, p.DB, st.PrivateRef)
	if err != nil {
		return VAPID{}, err
	}
	p.keys = &VAPID{Public: st.Public, Private: string(priv)}
	return *p.keys, nil
}

func (p *Pusher) sender() Sender {
	if p.Transport != nil {
		return p.Transport
	}
	client := &http.Client{Timeout: 15 * time.Second}
	return func(ctx context.Context, sub Subscription, payload []byte, keys VAPID) (int, error) {
		resp, err := webpush.SendNotificationWithContext(ctx, payload, &webpush.Subscription{Endpoint: sub.Endpoint, Keys: webpush.Keys{P256dh: sub.P256dh, Auth: sub.Auth}}, &webpush.Options{
			HTTPClient: client, Subscriber: p.Subject, VAPIDPublicKey: keys.Public, VAPIDPrivateKey: keys.Private, TTL: 3600, Urgency: webpush.UrgencyNormal,
		})
		if err != nil {
			return 0, err
		}
		defer resp.Body.Close()
		_, _ = io.Copy(io.Discard, resp.Body)
		return resp.StatusCode, nil
	}
}

// Send pushes msg to every browser the user subscribed; subscriptions the
// push service no longer knows are dropped.
func (p *Pusher) Send(ctx context.Context, userID string, msg Message) {
	subs, err := Subscriptions(ctx, p.DB, userID)
	if err != nil || len(subs) == 0 {
		return
	}
	keys, err := p.Keys(ctx)
	if err != nil {
		p.Log.Error("push keys", "err", err)
		return
	}
	payload, _ := json.Marshal(msg)
	send := p.sender()
	for _, sub := range subs {
		status, err := send(ctx, sub, payload, keys)
		switch {
		case err != nil:
			p.Log.Warn("push failed", "err", err, "user", userID)
		case status == http.StatusGone || status == http.StatusNotFound:
			_, _ = store.Exec(ctx, p.DB, `DELETE FROM push_subscriptions WHERE id = ?`, sub.ID)
		case status >= 200 && status < 300:
			_, _ = store.Exec(ctx, p.DB, `UPDATE push_subscriptions SET last_used_at = ? WHERE id = ?`, store.Millis(time.Now()), sub.ID)
		default:
			p.Log.Warn("push rejected", "status", status, "user", userID)
		}
	}
}

// Subscriptions lists a user's push subscriptions.
func Subscriptions(ctx context.Context, q store.Querier, userID string) ([]Subscription, error) {
	rows, err := store.Query(ctx, q, `SELECT id, endpoint, p256dh, auth FROM push_subscriptions WHERE user_id = ?`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Subscription
	for rows.Next() {
		var s Subscription
		if err := rows.Scan(&s.ID, &s.Endpoint, &s.P256dh, &s.Auth); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

var errEndpoint = errors.New("notify: push endpoint must be an https URL")

// Subscribe records (or moves to this user) a browser's push subscription.
func Subscribe(ctx context.Context, q store.Querier, userID string, s Subscription, userAgent string) error {
	if len(s.Endpoint) < 12 || s.Endpoint[:8] != "https://" || len(s.Endpoint) > 2048 || s.P256dh == "" || s.Auth == "" {
		return errEndpoint
	}
	_, err := store.Exec(ctx, q, `INSERT INTO push_subscriptions (id, user_id, endpoint, p256dh, auth, user_agent, created_at) VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (endpoint) DO UPDATE SET user_id = excluded.user_id, p256dh = excluded.p256dh, auth = excluded.auth, user_agent = excluded.user_agent`,
		ids.New("psh"), userID, s.Endpoint, s.P256dh, s.Auth, truncate(userAgent, 300), store.Millis(time.Now()))
	return err
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

// pushMessage words a notification for a push (English; the inbox is localized).
func (s *Service) pushMessage(n Notification) Message {
	var d Data
	_ = json.Unmarshal(n.Data, &d)
	who := n.ActorName
	if who == "" {
		who = "Someone"
	}
	m := Message{URL: s.BaseURL, Tag: n.RevisionID}
	if d.Owner != "" && d.Number > 0 {
		m.URL = s.BaseURL + "/" + d.Owner + "/" + d.Repo + "/revisions/" + itoa(d.Number)
	} else if d.Owner != "" && d.Path != "" {
		m.URL = s.BaseURL + "/" + d.Owner + "/" + d.Repo + "/" + d.Path
	}
	switch n.Kind {
	case KindReviewRequested:
		m.Title, m.Body = "Review requested", d.Title
	case KindApproval:
		m.Title, m.Body = "Approved "+itoa(d.Approved)+" of "+itoa(d.Of), d.Title
	case KindApproved:
		m.Title, m.Body = "Ready to publish", d.Title
	case KindChanges:
		m.Title, m.Body = "Changes requested", d.Title
	case KindUpdate:
		m.Title, m.Body = "Updates from Published", d.Title
	case KindConflicts:
		m.Title, m.Body = "Conflicts to resolve", d.Title
	case KindPublishing:
		m.Title, m.Body = "Pull request opened", d.Title
	case KindPublished:
		m.Title, m.Body = "Published", d.Title
	case KindPublishClosed:
		m.Title, m.Body = "Pull request closed", d.Title
	case KindInvited:
		m.Title, m.Body = "You were added to a revision", d.Title
	case KindMention:
		m.Title, m.Body = who+" mentioned you", d.Excerpt
	case KindReply:
		m.Title, m.Body = who+" replied", d.Excerpt
	default:
		m.Title, m.Body = "kmdn", d.Title
	}
	return m
}

func itoa(n int) string {
	b, _ := json.Marshal(n)
	return string(b)
}
