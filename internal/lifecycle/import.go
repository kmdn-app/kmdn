package lifecycle

import (
	"archive/tar"
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
	"time"

	"github.com/kmdn-app/kmdn/internal/blobs"
	"github.com/kmdn-app/kmdn/internal/ids"
	"github.com/kmdn-app/kmdn/internal/orgs"
	"github.com/kmdn-app/kmdn/internal/repos"
	"github.com/kmdn-app/kmdn/internal/store"
)

// ImportOptions shape an import.
type ImportOptions struct {
	// Slug renames the org (its slug is taken here, say).
	Slug string
	// IntoDefault loads it into this instance's default org (single mode):
	// the default org must have no repositories yet.
	IntoDefault bool
}

// ImportResult says what an import did.
type ImportResult struct {
	OrgID   string         `json:"org_id"`
	Slug    string         `json:"slug"`
	Rows    map[string]int `json:"rows"`
	Users   int            `json:"users_created"`
	Hosts   int            `json:"forge_hosts_created"`
	Uploads int            `json:"uploads"`
}

type archive struct {
	manifest Manifest
	files    map[string][]byte
}

func readArchive(r io.Reader) (archive, error) {
	a := archive{files: map[string][]byte{}}
	gz, err := gzip.NewReader(r)
	if err != nil {
		return a, fmt.Errorf("not a kmdn org archive (gzip): %w", err)
	}
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return a, err
		}
		if strings.Contains(h.Name, "..") {
			return a, fmt.Errorf("bad entry %q", h.Name)
		}
		b, err := io.ReadAll(io.LimitReader(tr, 4<<30))
		if err != nil {
			return a, err
		}
		a.files[h.Name] = b
	}
	mb, ok := a.files["manifest.json"]
	if !ok {
		return a, errors.New("not a kmdn org archive: no manifest.json")
	}
	if err := json.Unmarshal(mb, &a.manifest); err != nil {
		return a, err
	}
	if a.manifest.Format != ArchiveFormat {
		return a, fmt.Errorf("archive format %q, this kmdn reads %q", a.manifest.Format, ArchiveFormat)
	}
	return a, nil
}

func ndjson(b []byte, each func(map[string]any) error) error {
	sc := bufio.NewScanner(bytes.NewReader(b))
	sc.Buffer(make([]byte, 1<<20), 1<<30)
	for sc.Scan() {
		if len(bytes.TrimSpace(sc.Bytes())) == 0 {
			continue
		}
		dec := json.NewDecoder(bytes.NewReader(sc.Bytes()))
		dec.UseNumber()
		var row map[string]any
		if err := dec.Decode(&row); err != nil {
			return err
		}
		if err := each(row); err != nil {
			return err
		}
	}
	return sc.Err()
}

// Import loads an org archive (Export) into this instance. IDs are kept;
// the org, accounts and forge hosts that already exist here are matched
// (org by id, accounts by email, hosts by kind and URL) and every exact
// occurrence of the old id is rewritten, which is unambiguous because ids
// are unique prefixed strings. Repositories come back without credentials:
// reconnect them, then they sync.
func (s *Service) Import(ctx context.Context, r io.Reader, opt ImportOptions) (ImportResult, error) {
	res := ImportResult{Rows: map[string]int{}}
	a, err := readArchive(r)
	if err != nil {
		return res, err
	}
	st, err := s.DB.Status(ctx)
	if err != nil {
		return res, err
	}
	var schema int
	for _, m := range st {
		if m.AppliedAt != nil && m.Version > schema {
			schema = m.Version
		}
	}
	if a.manifest.SchemaVersion > schema {
		return res, fmt.Errorf("the archive comes from a newer kmdn (schema %d, here %d): upgrade first", a.manifest.SchemaVersion, schema)
	}
	rm := &remapper{to: map[string]string{}}
	remap := rm.to
	orgID, slug := a.manifest.Org.ID, a.manifest.Org.Slug
	if opt.Slug != "" {
		slug = opt.Slug
	}
	if opt.IntoDefault {
		var n int
		if err := store.QueryRow(ctx, s.DB, `SELECT COUNT(*) FROM repos WHERE org_id = ?`, orgs.DefaultID).Scan(&n); err != nil {
			return res, err
		}
		if n > 0 {
			return res, errors.New("the default org already has repositories; import into a new org instead")
		}
		remap[orgID] = orgs.DefaultID
		orgID = orgs.DefaultID
	} else {
		if _, err := orgs.ByID(ctx, s.DB, orgID); err == nil {
			// A copy of an org that's here: every row needs a new id.
			rm.fresh = true
			remap[orgID] = ids.New(ids.Org)
			orgID = remap[orgID]
		}
		if err := orgs.ValidSlug(slug); err != nil {
			return res, err
		}
		if _, err := orgs.BySlug(ctx, s.DB, slug); err == nil {
			return res, fmt.Errorf("the slug %q is taken here: pass another", slug)
		}
	}
	res.OrgID, res.Slug = orgID, slug
	err = s.DB.InTx(ctx, func(tx *store.Tx) error {
		// Accounts: matched by email, else created with their id.
		if err := ndjson(a.files["users.ndjson"], func(u map[string]any) error {
			id, email, name := str(u["id"]), strings.ToLower(str(u["email"])), str(u["name"])
			var have string
			err := store.QueryRow(ctx, tx, `SELECT id FROM users WHERE email = ?`, email).Scan(&have)
			switch {
			case err == nil:
				remap[id] = have // itself too, so a copy doesn't renew it
				return nil
			case !errors.Is(store.NotFound(err), store.ErrNotFound):
				return err
			}
			var taken int
			if err := store.QueryRow(ctx, tx, `SELECT COUNT(*) FROM users WHERE id = ?`, id).Scan(&taken); err != nil {
				return err
			}
			if taken > 0 {
				remap[id] = ids.New(ids.User)
			} else {
				remap[id] = id
			}
			id = remap[id]
			status := str(u["status"])
			if status == "" {
				status = "active"
			}
			res.Users++
			_, err = store.Exec(ctx, tx, `INSERT INTO users (id, email, name, is_instance_admin, status, created_at) VALUES (?, ?, ?, ?, ?, ?)`, id, email, name, false, status, store.Millis(time.Now()))
			return err
		}); err != nil {
			return fmt.Errorf("users: %w", err)
		}
		// Forge hosts: matched by kind and URL, else added to the org, without credentials.
		if err := ndjson(a.files["forge_hosts.ndjson"], func(h map[string]any) error {
			id, kind, base := str(h["id"]), str(h["kind"]), str(h["base_url"])
			var have string
			err := store.QueryRow(ctx, tx, `SELECT id FROM forge_hosts WHERE kind = ? AND base_url = ? AND (org_id IS NULL OR org_id = ?) ORDER BY org_id NULLS FIRST LIMIT 1`, kind, base, orgID).Scan(&have)
			if err == nil {
				remap[id] = have
				return nil
			}
			if !errors.Is(store.NotFound(err), store.ErrNotFound) {
				return err
			}
			newID := ids.New("fh")
			remap[id] = newID
			res.Hosts++
			_, err = store.Exec(ctx, tx, `INSERT INTO forge_hosts (id, kind, base_url, api_url, display_name, org_id, created_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
				newID, kind, base, str(h["api_url"]), str(h["display_name"]), orgID, store.Millis(time.Now()))
			return err
		}); err != nil {
			return fmt.Errorf("forge hosts: %w", err)
		}
		for _, t := range tables {
			if t.name == "orgs" && opt.IntoDefault {
				continue
			}
			cols, types, err := columns(ctx, tx, t.name)
			if err != nil {
				return err
			}
			n := 0
			if err := ndjson(a.files["tables/"+t.name+".ndjson"], func(row map[string]any) error {
				if t.name == "orgs" {
					row["slug"], row["status"], row["status_reason"], row["deleted_at"] = slug, orgs.Active, "", nil
				}
				if t.name == "org_members" && opt.IntoDefault {
					return insertIgnore(ctx, tx, t.name, cols, types, row, rm)
				}
				n++
				return insert(ctx, tx, t.name, cols, types, row, rm)
			}); err != nil {
				return fmt.Errorf("%s: %w", t.name, err)
			}
			res.Rows[t.name] = n
		}
		return nil
	})
	if err != nil {
		return res, err
	}
	for name, b := range a.files {
		if sha, ok := strings.CutPrefix(name, "uploads/"); ok {
			if err := s.Blobs.Put(ctx, blobs.Key(orgID, sha), b); err != nil {
				return res, err
			}
			res.Uploads++
		}
	}
	// Mirrors: cloned again once the repositories have credentials.
	if list, err := repos.ListIn(ctx, s.DB, orgID); err == nil && s.Repos != nil {
		for _, rp := range list {
			_, _ = s.Repos.EnqueueSync(ctx, rp.ID)
		}
	}
	s.Log.Info("org imported", "org", orgID, "slug", slug, "users", res.Users)
	return res, nil
}

// remapper rewrites ids. In fresh mode (the org is already here: a copy),
// every id it meets gets a new one of the same kind, consistently, except
// the accounts and forge hosts matched here.
type remapper struct {
	to    map[string]string
	fresh bool
}

var idLike = regexp.MustCompile(`^([a-z]{2,5})_[0-9a-hjkmnp-tv-z]{26}$`)

func (m *remapper) id(s string) string {
	if to, ok := m.to[s]; ok {
		return to
	}
	if m.fresh {
		if p := idLike.FindStringSubmatch(s); p != nil && p[1] != ids.User {
			m.to[s] = ids.New(p[1])
			return m.to[s]
		}
	}
	return s
}

func str(v any) string {
	s, _ := v.(string)
	return s
}

// columns are t's columns here, with their declared types.
func columns(ctx context.Context, q store.Querier, t string) ([]string, map[string]string, error) {
	rows, err := store.Query(ctx, q, `SELECT * FROM `+t+` WHERE 1 = 0`)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	cts, err := rows.ColumnTypes()
	if err != nil {
		return nil, nil, err
	}
	cols := make([]string, 0, len(cts))
	types := map[string]string{}
	for _, c := range cts {
		cols = append(cols, c.Name())
		types[c.Name()] = strings.ToUpper(c.DatabaseTypeName())
	}
	return cols, types, nil
}

// value converts an archived value for a column of type typ, rewriting
// remapped ids.
func value(v any, typ string, remap *remapper) any {
	switch x := v.(type) {
	case string:
		return remap.id(x)
	case json.Number:
		if strings.HasPrefix(typ, "BOOL") {
			return x.String() != "0"
		}
		if i, err := x.Int64(); err == nil {
			return i
		}
		f, _ := x.Float64()
		return f
	case map[string]any:
		if b, ok := x["$b"].(string); ok {
			d, _ := base64.StdEncoding.DecodeString(b)
			return d
		}
	}
	return v
}

func insert(ctx context.Context, q store.Querier, t string, cols []string, types map[string]string, row map[string]any, remap *remapper) error {
	var names []string
	var args []any
	for _, c := range cols {
		v, ok := row[c]
		if !ok {
			continue // a column this archive predates: its default
		}
		names = append(names, c)
		args = append(args, value(v, types[c], remap))
	}
	_, err := store.Exec(ctx, q, `INSERT INTO `+t+` (`+strings.Join(names, ", ")+`) VALUES (`+strings.TrimSuffix(strings.Repeat("?, ", len(names)), ", ")+`)`, args...)
	return err
}

func insertIgnore(ctx context.Context, q store.Querier, t string, cols []string, types map[string]string, row map[string]any, remap *remapper) error {
	var names []string
	var args []any
	for _, c := range cols {
		if v, ok := row[c]; ok {
			names = append(names, c)
			args = append(args, value(v, types[c], remap))
		}
	}
	_, err := store.Exec(ctx, q, `INSERT INTO `+t+` (`+strings.Join(names, ", ")+`) VALUES (`+strings.TrimSuffix(strings.Repeat("?, ", len(names)), ", ")+`) ON CONFLICT DO NOTHING`, args...)
	return err
}
