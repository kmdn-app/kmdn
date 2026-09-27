package policy

import (
	"context"
	"testing"
)

func TestGitURLStrict(t *testing.T) {
	ctx := context.Background()
	p := &Policy{Strict: true}
	for _, bad := range []string{
		"file:///data/uploads", "/data/mirrors/x.git", "./repo", "../repo", "repo",
		"ext::sh -c 'id'", "fd::17", "git://example.com/x.git", "http://example.com/x.git",
		"https://127.0.0.1/x.git", "https://169.254.169.254/latest", "https://10.1.2.3/x", "https://[::1]/x",
		"git@10.0.0.1:team/x.git", "git@localhost:team/x.git", "-oProxyCommand=id@host:x", "git@-oProxyCommand:x",
		"https://localhost/x.git",
	} {
		if err := p.GitURL(ctx, bad); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
	for _, ok := range []string{"https://1.1.1.1/team/docs.git", "ssh://git@1.1.1.1/team/docs.git", "git@1.1.1.1:team/docs.git"} {
		if err := p.GitURL(ctx, ok); err != nil {
			t.Errorf("refused %q: %v", ok, err)
		}
	}
	// The permissive default trusts the operator.
	if err := (&Policy{}).GitURL(ctx, "file:///srv/docs.git"); err != nil {
		t.Errorf("permissive refused a local URL: %v", err)
	}
	var none *Policy
	if err := none.GitURL(ctx, "/srv/docs.git"); err != nil || none.GitProtocols() != "" || !none.OrgForgesAllowed() {
		t.Errorf("nil policy isn't permissive")
	}
	if p.GitProtocols() != "https:ssh" || p.AllowPrivateOutbound(true) {
		t.Errorf("strict protocols or outbound")
	}
}
