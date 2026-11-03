package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
)

// Repo is a row of repos joined with its owner's name.
type Repo struct {
	ID            int64
	OwnerID       int64
	Owner         string
	Name          string
	Description   string
	Private       bool
	DefaultBranch string
	Archived      bool
	Mirror        bool // has a repo_mirrors row: pushes are refused, content comes from the source
	CreatedAt     string
	UpdatedAt     string
	ForkOf        int64  // the source repository of a fork (D75); 0 = not a fork, or its source was deleted
	MergeStyles   string // D88: comma list of merge | squash | rebase, first = preselected
}

// MergeStyleNames are the accepted merge styles, in their canonical order (D88).
var MergeStyleNames = []string{"merge", "squash", "rebase"}

// Styles lists the repository's allowed merge styles; an empty or broken value falls back to "merge".
func (r Repo) Styles() []string {
	var out []string
	for _, x := range strings.Split(r.MergeStyles, ",") {
		for _, ok := range MergeStyleNames {
			if strings.TrimSpace(x) == ok {
				out = append(out, ok)
			}
		}
	}
	if len(out) == 0 {
		return []string{"merge"}
	}
	return out
}

// Allows reports whether style is one of the repository's merge styles.
func (r Repo) Allows(style string) bool {
	for _, x := range r.Styles() {
		if x == style {
			return true
		}
	}
	return false
}

// Access levels, ordered: a higher level includes the lower ones.
type Access int

// Access values.
const (
	AccessNone Access = iota
	AccessRead
	AccessWrite
	AccessAdmin
)

func (a Access) String() string {
	switch a {
	case AccessRead:
		return "read"
	case AccessWrite:
		return "write"
	case AccessAdmin:
		return "admin"
	}
	return "none"
}

// ParseAccess maps a role string to an Access (for collaborators).
func ParseAccess(role string) (Access, bool) {
	switch role {
	case "read":
		return AccessRead, true
	case "write":
		return AccessWrite, true
	case "admin":
		return AccessAdmin, true
	}
	return AccessNone, false
}

const repoCols = `r.id, r.owner_id, u.name, r.name, r.description, r.is_private, r.default_branch, r.archived_at IS NOT NULL, EXISTS(SELECT 1 FROM repo_mirrors m WHERE m.repo_id = r.id), r.created_at, r.updated_at, COALESCE(r.fork_of, 0), r.merge_styles`

func scanRepo(row interface{ Scan(...any) error }) (Repo, error) {
	var r Repo
	err := row.Scan(&r.ID, &r.OwnerID, &r.Owner, &r.Name, &r.Description, &r.Private, &r.DefaultBranch, &r.Archived, &r.Mirror, &r.CreatedAt, &r.UpdatedAt, &r.ForkOf, &r.MergeStyles)
	if errors.Is(err, sql.ErrNoRows) {
		return Repo{}, ErrNotFound
	}
	return r, err
}

// CreateRepo inserts the metadata row. The caller creates the bare repo on disk afterwards
// using the returned id (D27) and deletes the row if that fails.
func (s *Store) CreateRepo(ctx context.Context, ownerID int64, name, description string, private bool, defaultBranch string) (Repo, error) {
	now := Now()
	res, err := s.db.ExecContext(ctx, `INSERT INTO repos (owner_id, name, description, is_private, default_branch, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		ownerID, name, description, private, defaultBranch, now, now)
	if err != nil {
		return Repo{}, err
	}
	id, _ := res.LastInsertId()
	return s.RepoByID(ctx, id)
}

// RepoByID fetches one repo.
func (s *Store) RepoByID(ctx context.Context, id int64) (Repo, error) {
	return scanRepo(s.db.QueryRowContext(ctx, `SELECT `+repoCols+` FROM repos r JOIN users u ON u.id = r.owner_id WHERE r.id = ?`, id))
}

// RepoByPath fetches owner/name.
func (s *Store) RepoByPath(ctx context.Context, owner, name string) (Repo, error) {
	return scanRepo(s.db.QueryRowContext(ctx, `SELECT `+repoCols+` FROM repos r JOIN users u ON u.id = r.owner_id WHERE u.name = ? AND r.name = ?`, owner, name))
}

// Repos lists repos, optionally only those of one owner (owner == "" lists all).
func (s *Store) Repos(ctx context.Context, owner string) ([]Repo, error) {
	q := `SELECT ` + repoCols + ` FROM repos r JOIN users u ON u.id = r.owner_id`
	var args []any
	if owner != "" {
		q += ` WHERE u.name = ?`
		args = append(args, owner)
	}
	rows, err := s.db.QueryContext(ctx, q+` ORDER BY u.name, r.name`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Repo
	for rows.Next() {
		r, err := scanRepo(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// RepoCount is the number of repositories on this install, mirrors included.
func (s *Store) RepoCount(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM repos`).Scan(&n)
	return n, err
}

// SetForkOf records that repo id was forked from src (D75).
func (s *Store) SetForkOf(ctx context.Context, id, src int64) error {
	_, err := s.db.ExecContext(ctx, `UPDATE repos SET fork_of = ? WHERE id = ?`, src, id)
	return err
}

// ForkCount is how many repositories were forked from id.
func (s *Store) ForkCount(ctx context.Context, id int64) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM repos WHERE fork_of = ?`, id).Scan(&n)
	return n, err
}

// DeleteRepo removes the metadata row (collaborators cascade).
func (s *Store) DeleteRepo(ctx context.Context, id int64) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM repos WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// SetDefaultBranch records the branch HEAD points at (mirrors follow their source).
func (s *Store) SetDefaultBranch(ctx context.Context, id int64, branch string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE repos SET default_branch = ?, updated_at = ? WHERE id = ?`, branch, Now(), id)
	return err
}

// SetDescription replaces a repository's one-line description.
func (s *Store) SetDescription(ctx context.Context, id int64, description string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE repos SET description = ?, updated_at = ? WHERE id = ?`, description, Now(), id)
	return err
}

// SetMergeStyles stores the allowed merge styles (already validated, canonical names, at least one).
func (s *Store) SetMergeStyles(ctx context.Context, id int64, styles []string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE repos SET merge_styles = ? WHERE id = ?`, strings.Join(styles, ","), id)
	return err
}

// SetArchived flips the archive flag.
func (s *Store) SetArchived(ctx context.Context, id int64, archived bool) error {
	var at any
	if archived {
		at = Now()
	}
	_, err := s.db.ExecContext(ctx, `UPDATE repos SET archived_at = ?, updated_at = ? WHERE id = ?`, at, Now(), id)
	return err
}

// Touch bumps updated_at (after a push).
func (s *Store) Touch(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `UPDATE repos SET updated_at = ? WHERE id = ?`, Now(), id)
	return err
}

// SetCollaborator grants or updates a role.
func (s *Store) SetCollaborator(ctx context.Context, repoID, userID int64, role Access) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO repo_collaborators (repo_id, user_id, role, created_at) VALUES (?, ?, ?, ?)
		ON CONFLICT(repo_id, user_id) DO UPDATE SET role = excluded.role`, repoID, userID, role.String(), Now())
	return err
}

// RemoveCollaborator revokes access.
func (s *Store) RemoveCollaborator(ctx context.Context, repoID, userID int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM repo_collaborators WHERE repo_id = ? AND user_id = ?`, repoID, userID)
	return err
}

// AccessFor is the one permission decision for a repo (PRD §9: checked before git runs,
// at every door). userID 0 = anonymous. Private repos are invisible to outsiders:
// the caller must answer "not found", never "forbidden", when this returns AccessNone.
func (s *Store) AccessFor(ctx context.Context, userID int64, r Repo) (Access, error) {
	best := AccessNone
	if !r.Private {
		best = AccessRead
	}
	if userID <= 0 {
		return best, nil
	}
	if r.OwnerID == userID {
		return AccessAdmin, nil
	}
	u, err := s.UserByID(ctx, userID)
	if err != nil && !errors.Is(err, ErrNotFound) {
		return AccessNone, err
	}
	if err == nil && (u.Disabled || u.IsOrg) {
		return best, nil // a disabled account or an organisation gets no more than the public does
	}
	if err == nil && u.IsAdmin {
		return AccessAdmin, nil
	}
	raise := func(role string) {
		if a, ok := ParseAccess(role); ok && a > best {
			best = a
		}
	}
	var role string
	switch err := s.db.QueryRowContext(ctx, `SELECT role FROM repo_collaborators WHERE repo_id = ? AND user_id = ?`, r.ID, userID).Scan(&role); {
	case err == nil:
		raise(role)
	case !errors.Is(err, sql.ErrNoRows):
		return AccessNone, err
	}
	// organisation repositories (D74): owner = admin, member = base role, teams = their role on this repo
	var orgRole, base string
	switch err := s.db.QueryRowContext(ctx, `SELECT m.role, o.org_base_role FROM org_members m JOIN users o ON o.id = m.org_id WHERE m.org_id = ? AND m.user_id = ? AND o.kind = 'org'`, r.OwnerID, userID).Scan(&orgRole, &base); {
	case err == nil:
		if orgRole == "owner" {
			return AccessAdmin, nil
		}
		if base != "none" {
			raise(base)
		}
	case !errors.Is(err, sql.ErrNoRows):
		return AccessNone, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT tr.role FROM team_repos tr JOIN team_members tm ON tm.team_id = tr.team_id JOIN teams t ON t.id = tr.team_id
		WHERE tr.repo_id = ? AND tm.user_id = ? AND t.org_id = ?`, r.ID, userID, r.OwnerID)
	if err != nil {
		return AccessNone, err
	}
	defer rows.Close()
	for rows.Next() {
		if err := rows.Scan(&role); err != nil {
			return AccessNone, err
		}
		raise(role)
	}
	return best, rows.Err()
}

// ReposVisible lists the repos a viewer may see (public, own, collaborator; admins see all),
// optionally only one owner's, filtered by a name substring, sorted by "recent" (updated_at
// desc) or "name". It returns one page plus the total count.
func (s *Store) ReposVisible(ctx context.Context, viewerID int64, owner, q, sort string, limit, offset int) ([]Repo, int, error) {
	// the list form of AccessFor (D74): keep the two in step; TestOrgAccess checks they agree
	where := `WHERE (r.is_private = 0 OR r.owner_id = ? OR EXISTS (SELECT 1 FROM repo_collaborators c WHERE c.repo_id = r.id AND c.user_id = ?)
		OR EXISTS (SELECT 1 FROM users a WHERE a.id = ? AND a.is_admin = 1 AND a.disabled_at IS NULL AND a.kind = 'user')
		OR EXISTS (SELECT 1 FROM org_members m WHERE m.org_id = r.owner_id AND m.user_id = ? AND (m.role = 'owner' OR u.org_base_role <> 'none'))
		OR EXISTS (SELECT 1 FROM team_repos tr JOIN team_members tm ON tm.team_id = tr.team_id JOIN teams t ON t.id = tr.team_id WHERE tr.repo_id = r.id AND tm.user_id = ? AND t.org_id = r.owner_id))`
	args := []any{viewerID, viewerID, viewerID, viewerID, viewerID}
	if owner != "" {
		where += ` AND u.name = ?`
		args = append(args, owner)
	}
	if q != "" {
		where += ` AND (r.name LIKE ? ESCAPE '\' OR r.description LIKE ? ESCAPE '\')`
		like := "%" + escapeLike(q) + "%"
		args = append(args, like, like)
	}
	var total int
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM repos r JOIN users u ON u.id = r.owner_id `+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	order := ` ORDER BY r.updated_at DESC, u.name, r.name`
	if sort == "name" {
		order = ` ORDER BY u.name, r.name`
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+repoCols+` FROM repos r JOIN users u ON u.id = r.owner_id `+where+order+` LIMIT ? OFFSET ?`, append(args, limit, offset)...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var out []Repo
	for rows.Next() {
		r, err := scanRepo(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, r)
	}
	return out, total, rows.Err()
}

func escapeLike(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return r.Replace(s)
}
