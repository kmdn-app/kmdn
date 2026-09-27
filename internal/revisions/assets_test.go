package revisions

import (
	"testing"

	"github.com/kmdn-app/kmdn/internal/repos"
)

func TestAssetTargetAndRelative(t *testing.T) {
	r := repos.Repo{}
	if got := assetTarget(r, "docs/guides/first-week.md", "desk", "png"); got != "docs/guides/images/desk.png" {
		t.Fatalf("default pattern: %s", got)
	}
	r.KmdnYML.Assets = &repos.AssetsConfig{Path: "static/img/{name}.{ext}"}
	if got := assetTarget(r, "docs/guides/first-week.md", "desk", "png"); got != "static/img/desk.png" {
		t.Fatalf("custom pattern: %s", got)
	}
	cases := [][3]string{
		{"docs/guides/a.md", "docs/guides/images/x.png", "images/x.png"},
		{"docs/guides/a.md", "static/img/x.png", "../../static/img/x.png"},
		{"index.md", "images/x.png", "images/x.png"},
		{"docs/a.md", "docs/b/c/x.png", "b/c/x.png"},
	}
	for _, c := range cases {
		if got := relativeTo(c[0], c[1]); got != c[2] {
			t.Errorf("relativeTo(%s, %s) = %s, want %s", c[0], c[1], got, c[2])
		}
	}
	if n, e := assetName("My Screenshot (2).PNG"); n != "my-screenshot-2" || e != "png" {
		t.Fatalf("assetName: %s %s", n, e)
	}
}

func TestSVGChecks(t *testing.T) {
	ok := `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 10 10"><defs><linearGradient id="g"/></defs><rect fill="url(#g)" width="10" height="10"/><use href="#g"/></svg>`
	if err := checkSVG([]byte(ok)); err != nil {
		t.Fatalf("clean svg rejected: %v", err)
	}
	for name, bad := range map[string]string{
		"script":   `<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`,
		"handler":  `<svg xmlns="http://www.w3.org/2000/svg" onload="alert(1)"/>`,
		"external": `<svg xmlns="http://www.w3.org/2000/svg" xmlns:xlink="http://www.w3.org/1999/xlink"><image xlink:href="https://evil.example/x.png"/></svg>`,
		"js link":  `<svg xmlns="http://www.w3.org/2000/svg"><a href="javascript:alert(1)"><rect/></a></svg>`,
		"foreign":  `<svg xmlns="http://www.w3.org/2000/svg"><foreignObject><div/></foreignObject></svg>`,
		"entity":   `<?xml version="1.0"?><!DOCTYPE svg [<!ENTITY x "y">]><svg xmlns="http://www.w3.org/2000/svg"/>`,
		"not svg":  `<html><body/></html>`,
	} {
		if err := checkSVG([]byte(bad)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if _, err := sniff([]byte("GIF89a....."), "png"); err == nil {
		t.Fatal("mismatched type accepted")
	}
}
