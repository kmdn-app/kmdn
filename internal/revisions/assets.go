package revisions

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/kmdn-app/kmdn/internal/ids"
	"github.com/kmdn-app/kmdn/internal/repos"
	"github.com/kmdn-app/kmdn/internal/store"
)

// DefaultAssetPath is where images go unless .kmdn.yml or settings say otherwise.
const DefaultAssetPath = "{dir}/images/{name}.{ext}"

// Image types accepted for upload, by extension.
var assetTypes = map[string]string{
	"png": "image/png", "jpg": "image/jpeg", "jpeg": "image/jpeg", "gif": "image/gif",
	"webp": "image/webp", "avif": "image/avif", "svg": "image/svg+xml",
}

// Asset is an upload added to a revision.
type Asset struct {
	ID        string    `json:"id"`
	Path      string    `json:"path"`
	Size      int64     `json:"size"`
	Mime      string    `json:"mime"`
	SHA256    string    `json:"sha256"`
	CreatedAt time.Time `json:"created_at"`
}

// UploadResult is what the editor inserts.
type UploadResult struct {
	Asset
	// Markdown is an image reference relative to the page.
	Markdown string `json:"markdown"`
	// Src is the path relative to the page (what the image node stores).
	Src string `json:"src"`
}

// uploadPath is where content lives on disk.
func (s *Service) uploadPath(sha string) string {
	return filepath.Join(s.DataDir, "uploads", sha[:2], sha)
}

var (
	nameUnsafe = regexp.MustCompile(`[^a-z0-9._-]+`)
	dashes     = regexp.MustCompile(`-{2,}`)
)

func assetName(filename string) (name, ext string) {
	base := path.Base(strings.ReplaceAll(filename, "\\", "/"))
	ext = strings.ToLower(strings.TrimPrefix(path.Ext(base), "."))
	name = strings.ToLower(strings.TrimSuffix(base, path.Ext(base)))
	name = strings.Trim(dashes.ReplaceAllString(nameUnsafe.ReplaceAllString(name, "-"), "-"), "-.")
	if name == "" {
		name = "image"
	}
	if len(name) > 60 {
		name = name[:60]
	}
	return name, ext
}

// sniff checks the bytes match the claimed image type.
func sniff(b []byte, ext string) (string, error) {
	want, ok := assetTypes[ext]
	if !ok {
		return "", invalid("file", "Upload PNG, JPEG, GIF, WebP, AVIF or SVG images.")
	}
	if ext == "svg" {
		if err := checkSVG(b); err != nil {
			return "", err
		}
		return want, nil
	}
	got := http.DetectContentType(b)
	if ext == "avif" { // not known to DetectContentType
		if len(b) > 12 && string(b[4:8]) == "ftyp" {
			return want, nil
		}
		return "", invalid("file", "That file isn't an AVIF image.")
	}
	if got != want {
		return "", invalid("file", "The file's contents don't match its "+strings.ToUpper(ext)+" extension.")
	}
	return want, nil
}

// checkSVG rejects SVGs that could run code or load other resources. They are
// also served sandboxed, but pages copied elsewhere shouldn't carry scripts.
func checkSVG(b []byte) error {
	bad := func(why string) error { return invalid("file", "This SVG can't be uploaded: "+why+".") }
	d := xml.NewDecoder(bytes.NewReader(b))
	d.Strict = false
	sawSVG := false
	for {
		tok, err := d.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return bad("it isn't valid XML")
		}
		switch t := tok.(type) {
		case xml.StartElement:
			n := strings.ToLower(t.Name.Local)
			if n == "svg" {
				sawSVG = true
			}
			if n == "script" || n == "foreignobject" || n == "iframe" || n == "embed" || n == "object" {
				return bad("it contains <" + t.Name.Local + ">")
			}
			for _, a := range t.Attr {
				an := strings.ToLower(a.Name.Local)
				v := strings.TrimSpace(strings.ToLower(a.Value))
				if strings.HasPrefix(an, "on") {
					return bad("it has event handlers")
				}
				if (an == "href" || an == "src") && v != "" && !strings.HasPrefix(v, "#") && !strings.HasPrefix(v, "data:image/") {
					return bad("it references other files")
				}
				if strings.Contains(v, "javascript:") {
					return bad("it contains a script link")
				}
			}
		case xml.Directive:
			if bytes.Contains(bytes.ToUpper(t), []byte("ENTITY")) {
				return bad("it declares entities")
			}
		}
	}
	if !sawSVG {
		return bad("it isn't an SVG image")
	}
	return nil
}

// assetTarget expands the path pattern for an image inserted into page.
func assetTarget(repo repos.Repo, page, name, ext string) string {
	pattern := DefaultAssetPath
	if a := repo.KmdnYML.Assets; a != nil && a.Path != "" {
		pattern = a.Path
	} else if a := repo.Settings.Assets; a != nil && a.Path != "" {
		pattern = a.Path
	}
	dir := path.Dir(page)
	if dir == "." {
		dir = ""
	}
	p := strings.NewReplacer("{dir}", dir, "{name}", name, "{ext}", ext).Replace(pattern)
	return strings.TrimPrefix(path.Clean("/"+p), "/")
}

func maxAssetBytes(repo repos.Repo, configMB int) int64 {
	mb := configMB
	for _, a := range []*repos.AssetsConfig{repo.Settings.Assets, repo.KmdnYML.Assets} {
		if a != nil && a.MaxSizeMB > 0 && a.MaxSizeMB < mb {
			mb = a.MaxSizeMB
		}
	}
	if mb <= 0 {
		mb = 10
	}
	return int64(mb) << 20
}

// relativeTo returns target relative to page's directory ("images/x.png", "../img/x.png").
func relativeTo(page, target string) string {
	from := strings.Split(path.Dir(page), "/")
	if path.Dir(page) == "." {
		from = nil
	}
	to := strings.Split(target, "/")
	i := 0
	for i < len(from) && i < len(to)-1 && from[i] == to[i] {
		i++
	}
	parts := make([]string, 0, len(from)-i+len(to)-i)
	for range from[i:] {
		parts = append(parts, "..")
	}
	return strings.Join(append(parts, to[i:]...), "/")
}

// Upload adds an image to the revision, next to the page it's inserted in.
func (s *Service) Upload(ctx context.Context, repo repos.Repo, rev Revision, c Caller, page, filename string, r io.Reader) (UploadResult, error) {
	ctx, current, unlock, gateErr := s.Mutate(ctx, rev.ID)
	if gateErr != nil {
		return UploadResult{}, gateErr
	}
	defer unlock()
	rev = current
	a, err := s.AccessFor(ctx, rev, c)
	if err != nil {
		return UploadResult{}, err
	}
	if !a.CanEdit {
		return UploadResult{}, ErrForbidden
	}
	page = cleanPath(page)
	if !repos.IsMarkdown(page) || !repo.Scope().Contains(page) {
		return UploadResult{}, invalid("page", "Upload images from a page in the repository's content.")
	}
	limit := maxAssetBytes(repo, s.UploadMaxMB)
	b, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return UploadResult{}, err
	}
	if int64(len(b)) > limit {
		return UploadResult{}, invalid("file", fmt.Sprintf("Images can be up to %d MB.", limit>>20))
	}
	if len(b) == 0 {
		return UploadResult{}, invalid("file", "That file is empty.")
	}
	name, ext := assetName(filename)
	if ext == "jpeg" {
		ext = "jpg"
	}
	mime, err := sniff(b, ext)
	if err != nil {
		return UploadResult{}, err
	}
	sum := sha256.Sum256(b)
	sha := hex.EncodeToString(sum[:])
	if err := s.store(sha, b); err != nil {
		return UploadResult{}, err
	}

	var out UploadResult
	err = s.DB.InTx(ctx, func(tx *store.Tx) error {
		now := store.Millis(time.Now())
		var uploadID string
		err := store.QueryRow(ctx, tx, `SELECT id FROM uploads WHERE sha256 = ?`, sha).Scan(&uploadID)
		if err != nil {
			uploadID = ids.New(ids.Upload)
			if _, err := store.Exec(ctx, tx, `INSERT INTO uploads (id, sha256, size, mime, created_by, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
				uploadID, sha, len(b), mime, c.User.ID, now); err != nil {
				return err
			}
		}
		// The same image already in this revision: reuse it.
		var existing string
		if err := store.QueryRow(ctx, tx, `SELECT path FROM revision_assets WHERE revision_id = ? AND sha256 = ? ORDER BY created_at LIMIT 1`, rev.ID, sha).Scan(&existing); err == nil {
			out.Path = existing
		} else {
			target, err := s.freePath(ctx, tx, repo, rev, assetTarget(repo, page, name, ext))
			if err != nil {
				return err
			}
			if !repo.Scope().Contains(target) {
				return invalid("file", "The assets path in the repository settings points outside the content folder.")
			}
			out.Path = target
			if _, err := store.Exec(ctx, tx, `INSERT INTO revision_assets (id, revision_id, path, upload_id, size, mime, sha256, created_by, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
				ids.New("rva"), rev.ID, target, uploadID, len(b), mime, sha, c.User.ID, now); err != nil {
				return err
			}
			if err := Record(ctx, tx, rev.ID, ActorUser, c.User.ID, "asset_added", map[string]any{"path": target}); err != nil {
				return err
			}
		}
		out.Size, out.Mime, out.SHA256, out.CreatedAt = int64(len(b)), mime, sha, store.FromMillis(now)
		return nil
	})
	if err != nil {
		return UploadResult{}, err
	}
	out.Src = relativeTo(page, out.Path)
	alt := strings.ReplaceAll(name, "-", " ")
	out.Markdown = "![" + alt + "](" + out.Src + ")"
	if err := s.ContentChanged(ctx, rev.ID, []string{c.User.ID}); err != nil {
		return UploadResult{}, err
	}
	s.changed(ctx, rev.ID, "assets")
	return out, nil
}

// freePath appends -2, -3… until neither the base nor the revision has the path.
func (s *Service) freePath(ctx context.Context, tx *store.Tx, repo repos.Repo, rev Revision, want string) (string, error) {
	ext := path.Ext(want)
	stem := strings.TrimSuffix(want, ext)
	for i := 1; i < 1000; i++ {
		p := want
		if i > 1 {
			p = fmt.Sprintf("%s-%d%s", stem, i, ext)
		}
		var n int
		if err := store.QueryRow(ctx, tx, `SELECT COUNT(*) FROM revision_assets WHERE revision_id = ? AND path = ?`, rev.ID, p).Scan(&n); err != nil {
			return "", err
		}
		if n > 0 {
			continue
		}
		if _, ok, err := s.base(ctx, repo, rev, p); err != nil {
			return "", err
		} else if ok {
			continue
		}
		return p, nil
	}
	return "", conflict("no_free_name", "Couldn't find a free name for the image.")
}

func (s *Service) store(sha string, b []byte) error {
	dst := s.uploadPath(sha)
	if _, err := os.Stat(dst); err == nil {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o750); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(dst), ".upload-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmp.Name()) }() // no-op after the rename
	if _, err := tmp.Write(b); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), dst)
}

// Assets lists the images a revision adds.
func Assets(ctx context.Context, q store.Querier, revisionID string) ([]Asset, error) {
	rows, err := store.Query(ctx, q, `SELECT id, path, size, mime, sha256, created_at FROM revision_assets WHERE revision_id = ? ORDER BY path`, revisionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Asset{}
	for rows.Next() {
		var a Asset
		var at int64
		if err := rows.Scan(&a.ID, &a.Path, &a.Size, &a.Mime, &a.SHA256, &at); err != nil {
			return nil, err
		}
		a.CreatedAt = store.FromMillis(at)
		out = append(out, a)
	}
	return out, rows.Err()
}

// ReadAsset returns the bytes of a revision's image, or nil when the revision
// didn't add one at p.
func (s *Service) ReadAsset(ctx context.Context, rev Revision, p string) ([]byte, string, error) {
	var sha, mime string
	err := store.QueryRow(ctx, s.DB, `SELECT sha256, mime FROM revision_assets WHERE revision_id = ? AND path = ?`, rev.ID, cleanPath(p)).Scan(&sha, &mime)
	if err != nil {
		return nil, "", store.NotFound(err)
	}
	b, err := os.ReadFile(s.uploadPath(sha))
	return b, mime, err
}
