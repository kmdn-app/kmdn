package app

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"io"
	"mime/multipart"
	"net/http"
	"testing"

	"github.com/kmdn-app/kmdn/internal/access"
	"github.com/kmdn-app/kmdn/internal/auth"
	"github.com/kmdn-app/kmdn/internal/users"
)

func (c *tc) upload(p, page, filename string, content []byte) (int, map[string]any) {
	c.t.Helper()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	_ = mw.WriteField("page", page)
	fw, _ := mw.CreateFormFile("file", filename)
	_, _ = fw.Write(content)
	_ = mw.Close()
	req, _ := http.NewRequest("POST", c.base+"/api/v1"+p, &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set(auth.CSRFHeader, c.csrf())
	res, err := c.c.Do(req)
	if err != nil {
		c.t.Fatal(err)
	}
	defer res.Body.Close()
	out := map[string]any{}
	_ = json.NewDecoder(res.Body).Decode(&out)
	return res.StatusCode, out
}

func pngBytes(t *testing.T) []byte {
	img := image.NewRGBA(image.Rect(0, 0, 2, 2))
	img.Set(0, 0, color.RGBA{255, 0, 0, 255})
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func TestUploadImagesIntoRevision(t *testing.T) {
	a, admin := newApp(t, nil)
	ctx := context.Background()
	maya, _ := users.Create(ctx, a.DB, "maya@northwind.dev", "Maya", true)
	signIn(t, a, admin, maya)
	repoID := connectLocal(t, a, admin, map[string]string{
		"docs/guides/first-week.md":   "# First week\n",
		"docs/guides/images/desk.png": "existing",
	})
	vic, _ := users.Create(ctx, a.DB, "vic@northwind.dev", "Vic", false)
	_ = access.Grant(ctx, a.DB, repoID, "user", vic.ID, access.Viewer)
	vicC := &tc{t: t, base: admin.base, c: newClient()}
	signIn(t, a, vicC, vic)
	_, rev := admin.do("POST", "/repos/"+repoID+"/revisions", map[string]any{"title": "Photos"})
	revID := rev["id"].(string)
	img := pngBytes(t)

	// desk.png exists in the repo, so the upload gets a free name next to it.
	code, up := admin.upload("/revisions/"+revID+"/assets", "docs/guides/first-week.md", "Desk.png", img)
	if code != 201 || up["path"] != "docs/guides/images/desk-2.png" || up["src"] != "images/desk-2.png" || up["markdown"] != "![desk](images/desk-2.png)" {
		t.Fatalf("upload: %d %v", code, up)
	}
	// The same bytes again reuse the asset.
	if code, again := admin.upload("/revisions/"+revID+"/assets", "docs/guides/first-week.md", "copy.png", img); code != 201 || again["path"] != "docs/guides/images/desk-2.png" {
		t.Fatalf("dedupe: %d %v", code, again)
	}
	// Served as the revision sees it: the upload, and base files too.
	res, err := admin.c.Get(admin.base + "/api/v1/revisions/" + revID + "/raw/docs/guides/images/desk-2.png")
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != 200 || !bytes.Equal(got, img) || res.Header.Get("Content-Type") != "image/png" || res.Header.Get("Content-Security-Policy") == "" {
		t.Fatalf("raw: %d %s %q", res.StatusCode, res.Header.Get("Content-Type"), got[:min(8, len(got))])
	}
	if res, _ := admin.c.Get(admin.base + "/api/v1/revisions/" + revID + "/raw/docs/guides/images/desk.png"); res.StatusCode != 200 {
		t.Fatalf("base image through revision raw: %d", res.StatusCode)
	}
	if _, list := admin.do("GET", "/revisions/"+revID+"/assets", nil); len(list["items"].([]any)) != 1 {
		t.Fatalf("assets: %v", list)
	}
	// A page linking the uploaded image isn't flagged by the link checker.
	admin.do("POST", "/revisions/"+revID+"/files", map[string]any{"op": "add", "path": "docs/guides/photos.md", "content": "# Photos\n\n![desk](images/desk-2.png)\n"})
	if _, ch := admin.do("GET", "/revisions/"+revID+"/checks", nil); len(ch["broken_links"].([]any)) != 0 {
		t.Fatalf("uploaded image flagged as broken: %v", ch)
	}
	// Refusals: wrong type, lying extension, script SVG, viewer.
	if code, _ := admin.upload("/revisions/"+revID+"/assets", "docs/guides/first-week.md", "notes.txt", []byte("hi")); code != 422 {
		t.Fatalf("txt: %d", code)
	}
	if code, _ := admin.upload("/revisions/"+revID+"/assets", "docs/guides/first-week.md", "fake.png", []byte("GIF89a-not-a-png")); code != 422 {
		t.Fatalf("fake png: %d", code)
	}
	if code, _ := admin.upload("/revisions/"+revID+"/assets", "docs/guides/first-week.md", "x.svg", []byte(`<svg xmlns="http://www.w3.org/2000/svg"><script>1</script></svg>`)); code != 422 {
		t.Fatalf("script svg: %d", code)
	}
	if code, _ := vicC.upload("/revisions/"+revID+"/assets", "docs/guides/first-week.md", "v.png", img); code != 403 {
		t.Fatalf("viewer upload: %d", code)
	}
	// Size limit from .kmdn.yml-style settings (config default is 10 MB).
	big := append(append([]byte{}, img...), make([]byte, 11<<20)...)
	if code, _ := admin.upload("/revisions/"+revID+"/assets", "docs/guides/first-week.md", "big.png", big); code != 422 && code != 413 {
		t.Fatalf("too big: %d", code)
	}
}
