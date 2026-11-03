package store

import (
	"context"
	"database/sql"
	"encoding/csv"
	"io"
	"strconv"
)

// Data report and personal data export (D73). Secrets never leave through here: no password hash, no token
// hash, no sealed value; a token or key is described by its name, scopes, fingerprint and dates.

// AccessRow says who can reach one repository.
type AccessRow struct {
	Repo          string
	Private       bool
	Mirror        bool
	Archived      bool
	Owner         string
	Collaborators []Collaborator
	OrgBase       string         // organisation repos: the members' base role (D74); "" for a person's repo
	OrgOwners     []string       // organisation owners (admin on every org repo)
	OrgMembers    int            // members who get OrgBase
	Teams         []Collaborator // team name and its role on this repo
}

// Collaborator is a user with an explicit role on a repository.
type Collaborator struct {
	User string
	Role string
}

// AccessMatrix lists every repository with its owner and collaborators (admins can reach everything; the
// caller lists them separately).
func (s *Store) AccessMatrix(ctx context.Context) ([]AccessRow, error) {
	repos, err := s.Repos(ctx, "")
	if err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT c.repo_id, u.name, c.role FROM repo_collaborators c JOIN users u ON u.id = c.user_id ORDER BY u.name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	collab := map[int64][]Collaborator{}
	for rows.Next() {
		var id int64
		var c Collaborator
		if err := rows.Scan(&id, &c.User, &c.Role); err != nil {
			return nil, err
		}
		collab[id] = append(collab[id], c)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]AccessRow, 0, len(repos))
	orgCache := map[int64]*AccessRow{}
	for _, r := range repos {
		row := AccessRow{Repo: r.Owner + "/" + r.Name, Private: r.Private, Mirror: r.Mirror, Archived: r.Archived, Owner: r.Owner, Collaborators: collab[r.ID]}
		if o, err := s.UserByID(ctx, r.OwnerID); err == nil && o.IsOrg {
			if c := orgCache[o.ID]; c != nil {
				row.OrgBase, row.OrgOwners, row.OrgMembers = c.OrgBase, c.OrgOwners, c.OrgMembers
			} else {
				_ = s.db.QueryRowContext(ctx, `SELECT org_base_role FROM users WHERE id = ?`, o.ID).Scan(&row.OrgBase)
				ms, err := s.OrgMembers(ctx, o.ID)
				if err != nil {
					return nil, err
				}
				for _, m := range ms {
					if m.Role == "owner" {
						row.OrgOwners = append(row.OrgOwners, m.Name)
					} else {
						row.OrgMembers++
					}
				}
				orgCache[o.ID] = &row
			}
			trs, err := s.db.QueryContext(ctx, `SELECT t.name, tr.role FROM team_repos tr JOIN teams t ON t.id = tr.team_id WHERE tr.repo_id = ? ORDER BY t.name`, r.ID)
			if err != nil {
				return nil, err
			}
			for trs.Next() {
				var c Collaborator
				_ = trs.Scan(&c.User, &c.Role)
				row.Teams = append(row.Teams, c)
			}
			trs.Close()
		}
		out = append(out, row)
	}
	return out, nil
}

// DBBytes is the size of the database file as SQLite counts it.
func (s *Store) DBBytes(ctx context.Context) (int64, error) {
	var pages, size int64
	if err := s.db.QueryRowContext(ctx, `PRAGMA page_count`).Scan(&pages); err != nil {
		return 0, err
	}
	err := s.db.QueryRowContext(ctx, `PRAGMA page_size`).Scan(&size)
	return pages * size, err
}

// WriteAuditCSV streams the whole audit trail as CSV, oldest first.
func (s *Store) WriteAuditCSV(ctx context.Context, w io.Writer) error {
	rows, err := s.db.QueryContext(ctx, `SELECT a.id, a.at, COALESCE(u.name, ''), a.action, a.target, a.detail FROM audit_log a LEFT JOIN users u ON u.id = a.actor_id ORDER BY a.id`)
	if err != nil {
		return err
	}
	defer rows.Close()
	cw := csv.NewWriter(w)
	if err := cw.Write([]string{"id", "at", "actor", "action", "target", "detail"}); err != nil {
		return err
	}
	for rows.Next() {
		var id int64
		var at, actor, action, target, detail string
		if err := rows.Scan(&id, &at, &actor, &action, &target, &detail); err != nil {
			return err
		}
		if err := cw.Write([]string{strconv.FormatInt(id, 10), at, actor, action, target, detail}); err != nil {
			return err
		}
	}
	cw.Flush()
	if err := cw.Error(); err != nil {
		return err
	}
	return rows.Err()
}

// records runs a query and returns its rows as column→value maps, for the JSON export.
func (s *Store) records(ctx context.Context, q string, args ...any) ([]map[string]any, error) {
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	out := []map[string]any{}
	for rows.Next() {
		vals := make([]sql.NullString, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return nil, err
		}
		m := make(map[string]any, len(cols))
		for i, c := range cols {
			if vals[i].Valid {
				m[c] = vals[i].String
			} else {
				m[c] = nil
			}
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// PersonalData is everything Codr holds about one person, without secrets (PDPA right of access, D73).
func (s *Store) PersonalData(ctx context.Context, userID int64) (map[string]any, error) {
	parts := []struct{ name, q string }{
		{"account", `SELECT name, email, lang, theme, is_admin, created_at, disabled_at FROM users WHERE id = ? AND kind = 'user'`},
		{"ssh_keys", `SELECT title, fingerprint, created_at, last_used_at FROM ssh_keys WHERE user_id = ? ORDER BY id`},
		{"tokens", `SELECT name, scopes, created_at, expires_at, last_used_at FROM access_tokens WHERE user_id = ? ORDER BY id`},
		{"sessions", `SELECT created_at, expires_at, user_agent, ip FROM sessions WHERE user_id = ? ORDER BY created_at`},
		{"repositories_owned", `SELECT name, description, is_private, created_at FROM repos WHERE owner_id = ? ORDER BY name`},
		{"organisations", `SELECT o.name AS organisation, m.role, m.created_at FROM org_members m JOIN users o ON o.id = m.org_id WHERE m.user_id = ? ORDER BY o.name`},
		{"teams", `SELECT o.name AS organisation, t.name AS team FROM team_members tm JOIN teams t ON t.id = tm.team_id JOIN users o ON o.id = t.org_id WHERE tm.user_id = ? ORDER BY o.name, t.name`},
		{"collaborations", `SELECT u.name || '/' || r.name AS repo, c.role, c.created_at FROM repo_collaborators c JOIN repos r ON r.id = c.repo_id JOIN users u ON u.id = r.owner_id WHERE c.user_id = ? ORDER BY repo`},
		{"issues_opened", `SELECT u.name || '/' || r.name AS repo, i.number, i.title, i.body, i.state, i.created_at FROM issues i JOIN repos r ON r.id = i.repo_id JOIN users u ON u.id = r.owner_id WHERE i.author_id = ? AND i.ext_author = '' ORDER BY i.id`},
		{"issue_comments", `SELECT u.name || '/' || r.name AS repo, i.number, c.body, c.created_at FROM issue_comments c JOIN issues i ON i.id = c.issue_id JOIN repos r ON r.id = i.repo_id JOIN users u ON u.id = r.owner_id WHERE c.author_id = ? AND c.ext_author = '' ORDER BY c.id`},
		{"pull_requests_opened", `SELECT u.name || '/' || r.name AS repo, p.number, p.title, p.body, p.state, p.created_at FROM pulls p JOIN repos r ON r.id = p.repo_id JOIN users u ON u.id = r.owner_id WHERE p.author_id = ? AND p.ext_author = '' ORDER BY p.id`},
		{"pull_comments", `SELECT u.name || '/' || r.name AS repo, p.number, c.body, c.created_at FROM pull_comments c JOIN pulls p ON p.id = c.pull_id JOIN repos r ON r.id = p.repo_id JOIN users u ON u.id = r.owner_id WHERE c.author_id = ? AND c.ext_author = '' ORDER BY c.id`},
		{"line_comments", `SELECT u.name || '/' || r.name AS repo, p.number, c.path, c.line, c.body, c.created_at FROM pull_line_comments c JOIN pulls p ON p.id = c.pull_id JOIN repos r ON r.id = p.repo_id JOIN users u ON u.id = r.owner_id WHERE c.author_id = ? ORDER BY c.id`},
		{"reviews", `SELECT u.name || '/' || r.name AS repo, p.number, v.verdict, v.body, v.created_at FROM pull_reviews v JOIN pulls p ON p.id = v.pull_id JOIN repos r ON r.id = p.repo_id JOIN users u ON u.id = r.owner_id WHERE v.reviewer_id = ? ORDER BY v.id`},
		{"ai_requests", `SELECT u.name || '/' || r.name AS repo, p.number, n.kind, n.model, n.bytes_sent, n.created_at FROM pull_ai_notes n JOIN pulls p ON p.id = n.pull_id JOIN repos r ON r.id = p.repo_id JOIN users u ON u.id = r.owner_id WHERE n.requested_by = ? ORDER BY n.id`},
		{"assignments", `SELECT u.name || '/' || r.name AS repo, a.number, a.created_at FROM assignees a JOIN repos r ON r.id = a.repo_id JOIN users u ON u.id = r.owner_id WHERE a.user_id = ? ORDER BY a.created_at`},
		{"audit_as_actor", `SELECT at, action, target, detail FROM audit_log WHERE actor_id = ? ORDER BY id`},
	}
	out := map[string]any{}
	for _, p := range parts {
		recs, err := s.records(ctx, p.q, userID)
		if err != nil {
			return nil, err
		}
		out[p.name] = recs
	}
	return out, nil
}
