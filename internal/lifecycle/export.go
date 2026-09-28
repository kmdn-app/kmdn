package lifecycle

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/kmdn-app/kmdn/internal/blobs"
	"github.com/kmdn-app/kmdn/internal/store"
	"github.com/kmdn-app/kmdn/internal/version"
)

// An org archive (docs/specs/16-organizations.md#lifecycle) is a .tar.gz:
//
//	manifest.json            format, versions, the org, row counts
//	users.ndjson             the accounts its rows mention (id, email, name)
//	forge_hosts.ndjson       the forge hosts its repositories use (no credentials)
//	tables/<table>.ndjson    the org's rows, one JSON object per line
//	uploads/<sha256>         uploaded images
//
// Credentials (tokens, keys, secrets), sign-ins and what kmdn derives again
// (search, links, summaries, consistency findings) aren't in it. Import
// (Import) loads it into another instance; mirrors are cloned again there.

// ArchiveFormat names the archive format.
const ArchiveFormat = "kmdn-org/1"

// table is an exported table and how to find the org's rows (? is the org id).
type table struct {
	name, where string
	// blank lists columns exported empty (secret references).
	blank []string
}

func under(parent, fk, parentWhere string) string {
	return fk + " IN (SELECT id FROM " + parent + " WHERE " + parentWhere + ")"
}

const (
	inOrg      = "org_id = ?"
	inRepos    = "repo_id IN (SELECT id FROM repos WHERE org_id = ?)"
	inRevs     = "revision_id IN (SELECT id FROM revisions WHERE repo_id IN (SELECT id FROM repos WHERE org_id = ?))"
	inThreads  = "thread_id IN (SELECT id FROM threads WHERE repo_id IN (SELECT id FROM repos WHERE org_id = ?))"
	inYdocs    = "ydoc_id IN (SELECT id FROM ydocs WHERE " + inRevs + ")"
	inChecks   = "checkpoint_id IN (SELECT id FROM revision_checkpoints WHERE " + inRevs + ")"
	inUpdates  = "update_id IN (SELECT id FROM revision_updates WHERE " + inRevs + ")"
	inComments = "comment_id IN (SELECT id FROM comments WHERE " + inThreads + ")"
)

// tables, in an order that respects foreign keys on import.
var tables = []table{
	{name: "orgs", where: "id = ?"},
	{name: "org_settings", where: inOrg},
	{name: "org_domains", where: inOrg},
	{name: "org_members", where: inOrg},
	{name: "groups", where: inOrg},
	{name: "group_members", where: under("groups", "group_id", inOrg)},
	{name: "uploads", where: inOrg},
	{name: "repos", where: inOrg, blank: []string{"token_ref", "webhook_secret_ref", "install_id"}},
	{name: "repo_members", where: inRepos},
	{name: "follows", where: inRepos},
	{name: "consistency_ignores", where: inRepos},
	{name: "revisions", where: inRepos},
	{name: "revision_files", where: inRevs},
	{name: "revision_members", where: inRevs},
	{name: "revision_reviewers", where: inRevs},
	{name: "approvals", where: inRevs},
	{name: "revision_events", where: inRevs},
	{name: "revision_assets", where: inRevs},
	{name: "revision_checkpoints", where: inRevs},
	{name: "revision_checkpoint_files", where: inChecks},
	{name: "revision_save_intents", where: inRevs},
	{name: "revision_updates", where: inRevs},
	{name: "revision_update_files", where: inUpdates},
	{name: "revision_publish_claims", where: inRevs},
	{name: "threads", where: inRepos},
	{name: "comments", where: inThreads},
	{name: "reactions", where: inComments},
	{name: "ydocs", where: inRevs},
	{name: "ydoc_snapshots", where: inYdocs},
	{name: "ydoc_updates", where: inYdocs},
	{name: "ydoc_clients", where: inYdocs},
	{name: "assistant_threads", where: inOrg},
	{name: "assistant_messages", where: under("assistant_threads", "thread_id", inOrg)},
	{name: "audit_log", where: inOrg},
}

// Manifest describes an archive.
type Manifest struct {
	Format        string         `json:"format"`
	KmdnVersion   string         `json:"kmdn_version"`
	SchemaVersion int            `json:"schema_version"`
	ExportedAt    time.Time      `json:"exported_at"`
	Org           ManifestOrg    `json:"org"`
	Rows          map[string]int `json:"rows"`
	Uploads       int            `json:"uploads"`
}

// ManifestOrg is the exported org.
type ManifestOrg struct {
	ID   string `json:"id"`
	Slug string `json:"slug"`
	Name string `json:"name"`
}

// Export writes orgID's archive to w.
func (s *Service) Export(ctx context.Context, orgID string, w io.Writer) (Manifest, error) {
	m := Manifest{Format: ArchiveFormat, KmdnVersion: version.Get().Version, ExportedAt: time.Now().UTC(), Rows: map[string]int{}}
	if err := store.QueryRow(ctx, s.DB, `SELECT id, slug, name FROM orgs WHERE id = ?`, orgID).Scan(&m.Org.ID, &m.Org.Slug, &m.Org.Name); err != nil {
		return m, store.NotFound(err)
	}
	st, err := s.DB.Status(ctx)
	if err != nil {
		return m, err
	}
	for _, mi := range st {
		if mi.AppliedAt != nil && mi.Version > m.SchemaVersion {
			m.SchemaVersion = mi.Version
		}
	}
	gz := gzip.NewWriter(w)
	tw := tar.NewWriter(gz)
	userIDs := map[string]bool{}
	hostIDs := map[string]bool{}
	var shas []string
	for _, t := range tables {
		var buf strings.Builder
		n, err := s.dumpTable(ctx, t, orgID, &buf, func(col string, v any) {
			if sv, ok := v.(string); ok {
				if strings.HasPrefix(sv, "usr_") {
					userIDs[sv] = true
				}
				if t.name == "repos" && col == "forge_host_id" {
					hostIDs[sv] = true
				}
				if t.name == "uploads" && col == "sha256" {
					shas = append(shas, sv)
				}
			}
		})
		if err != nil {
			return m, fmt.Errorf("export %s: %w", t.name, err)
		}
		m.Rows[t.name] = n
		if err := writeFile(tw, "tables/"+t.name+".ndjson", []byte(buf.String())); err != nil {
			return m, err
		}
	}
	// The accounts and forge hosts the rows mention.
	var ub strings.Builder
	for _, id := range sortedKeys(userIDs) {
		var email, name, status string
		if err := store.QueryRow(ctx, s.DB, `SELECT email, name, status FROM users WHERE id = ?`, id).Scan(&email, &name, &status); err != nil {
			continue // a string that looked like an id
		}
		b, _ := json.Marshal(map[string]string{"id": id, "email": email, "name": name, "status": status})
		ub.Write(append(b, '\n'))
	}
	if err := writeFile(tw, "users.ndjson", []byte(ub.String())); err != nil {
		return m, err
	}
	var hb strings.Builder
	for _, id := range sortedKeys(hostIDs) {
		var kind, base, api, display string
		if err := store.QueryRow(ctx, s.DB, `SELECT kind, base_url, api_url, display_name FROM forge_hosts WHERE id = ?`, id).Scan(&kind, &base, &api, &display); err != nil {
			return m, err
		}
		b, _ := json.Marshal(map[string]string{"id": id, "kind": kind, "base_url": base, "api_url": api, "display_name": display})
		hb.Write(append(b, '\n'))
	}
	if err := writeFile(tw, "forge_hosts.ndjson", []byte(hb.String())); err != nil {
		return m, err
	}
	for _, sha := range shas {
		b, err := s.Blobs.Get(ctx, blobs.Key(orgID, sha))
		if err != nil {
			s.Log.Warn("export: upload missing", "org", orgID, "sha", sha, "err", err)
			continue
		}
		if err := writeFile(tw, "uploads/"+sha, b); err != nil {
			return m, err
		}
		m.Uploads++
	}
	mb, _ := json.MarshalIndent(m, "", "  ")
	if err := writeFile(tw, "manifest.json", mb); err != nil {
		return m, err
	}
	if err := tw.Close(); err != nil {
		return m, err
	}
	return m, gz.Close()
}

// dumpTable writes t's rows for orgID as NDJSON; see is called for every value.
func (s *Service) dumpTable(ctx context.Context, t table, orgID string, w io.Writer, see func(col string, v any)) (int, error) {
	args := make([]any, strings.Count(t.where, "?"))
	for i := range args {
		args[i] = orgID
	}
	rows, err := store.Query(ctx, s.DB, `SELECT * FROM `+t.name+` WHERE `+t.where, args...)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		return 0, err
	}
	n := 0
	vals := make([]any, len(cols))
	ptrs := make([]any, len(cols))
	for i := range vals {
		ptrs[i] = &vals[i]
	}
	for rows.Next() {
		if err := rows.Scan(ptrs...); err != nil {
			return n, err
		}
		row := make(map[string]any, len(cols))
		for i, c := range cols {
			v := vals[i]
			for _, b := range t.blank {
				if b == c {
					v = nil
				}
			}
			if b, ok := v.([]byte); ok {
				v = map[string]string{"$b": base64.StdEncoding.EncodeToString(b)}
			}
			see(c, v)
			row[c] = v
		}
		b, err := json.Marshal(row)
		if err != nil {
			return n, err
		}
		if _, err := w.Write(append(b, '\n')); err != nil {
			return n, err
		}
		n++
	}
	return n, rows.Err()
}

func writeFile(tw *tar.Writer, name string, b []byte) error {
	if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o600, Size: int64(len(b)), ModTime: time.Now()}); err != nil {
		return err
	}
	_, err := tw.Write(b)
	return err
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
