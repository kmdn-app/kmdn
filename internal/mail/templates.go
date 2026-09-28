package mail

import (
	"fmt"
	"html"
)

func htmlLayout(title, body string) string {
	return `<!doctype html><html><body style="margin:0;padding:32px 16px;background:#fafafa;font:15px/1.6 -apple-system,Segoe UI,sans-serif;color:#09090b">
<div style="max-width:480px;margin:0 auto;background:#fff;border:1px solid #e4e4e7;border-radius:12px;padding:28px">
<div style="width:32px;height:32px;border-radius:8px;background:#18181b;color:#fafafa;font:600 17px/32px ui-monospace,monospace;text-align:center">k</div>
<h1 style="font-size:19px;margin:18px 0 8px">` + html.EscapeString(title) + `</h1>` + body + `</div></body></html>`
}

// SignIn is the magic-link email.
func SignIn(instance, to, link, code string, minutes int) Message {
	text := fmt.Sprintf("Sign in to %s\n\nOpen this link to sign in:\n%s\n\nOr enter this code on the sign-in page: %s\n\nThe link and code work once and expire in %d minutes. If you didn't ask to sign in, you can ignore this email.\n",
		instance, link, code, minutes)
	body := fmt.Sprintf(`<p style="margin:0 0 20px">Open this link to sign in. It works once and expires in %d minutes.</p>
<p style="margin:0 0 20px"><a href="%s" style="display:inline-block;background:#18181b;color:#fafafa;text-decoration:none;padding:10px 16px;border-radius:8px;font-weight:500">Sign in</a></p>
<p style="margin:0 0 6px;color:#71717a;font-size:13px">Or enter this code on the sign-in page:</p>
<p style="margin:0 0 20px;font:600 22px ui-monospace,monospace;letter-spacing:4px">%s</p>
<p style="margin:0;color:#71717a;font-size:13px">If you didn't ask to sign in, you can ignore this email.</p>`, minutes, html.EscapeString(link), html.EscapeString(code))
	return Message{To: to, Subject: "Sign in to " + instance, Text: text, HTML: htmlLayout("Sign in to "+instance, body), Kind: KindSignIn}
}

// Invite is sent when someone is invited to the instance or a repo.
func Invite(instance, to, inviter, target, link string, days int) Message {
	text := fmt.Sprintf("%s invited you to %s on %s.\n\nAccept the invite:\n%s\n\nThe invite expires in %d days.\n", inviter, target, instance, link, days)
	body := fmt.Sprintf(`<p style="margin:0 0 20px">%s invited you to <b>%s</b>.</p>
<p style="margin:0 0 20px"><a href="%s" style="display:inline-block;background:#18181b;color:#fafafa;text-decoration:none;padding:10px 16px;border-radius:8px;font-weight:500">Accept invite</a></p>
<p style="margin:0;color:#71717a;font-size:13px">The invite expires in %d days.</p>`, html.EscapeString(inviter), html.EscapeString(target), html.EscapeString(link), days)
	return Message{To: to, Subject: inviter + " invited you to " + target, Text: text, HTML: htmlLayout("You're invited", body), Kind: KindInvite}
}

// Test is the "Send test email" message.
func Test(instance, to string) Message {
	return Message{Kind: KindTest, To: to, Subject: instance + " test email",
		Text: "This is a test email from " + instance + ". Sign-in links and invites will be delivered the same way.\n",
		HTML: htmlLayout("Email works", `<p style="margin:0">This is a test email from `+html.EscapeString(instance)+`. Sign-in links and invites will be delivered the same way.</p>`)}
}
