package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
)

// Organisations and teams (D74). Access is decided in ONE place, AccessFor (and its list form in ReposVisible):
// an org owner is admin on every org repo; a member gets the org base role; a team gives its role on its repos;
// the highest role wins. Nothing here bypasses those two functions.

// OrgMember is a person in an organisation.
type OrgMember struct {
	UserID int64
	Name   string
	Role   string // owner | member
}

// Team is a group inside an organisation.
type Team struct {
	ID      int64
	OrgID   int64
	Name    string
	Members []string
	Repos   []TeamRepo
}

// TeamRepo is a role a team holds on one repository.
type TeamRepo struct {
	RepoID int64
	Repo   string // name inside the org
	Role   string
}

// Org is an organisation with its settings.
type Org struct {
	User
	BaseRole string // none | read | write
}

// CreateOrg makes an organisation with one owner.
func (s *Store) CreateOrg(ctx context.Context, name string, ownerID int64) (Org, error) {
	owner, err := s.UserByID(ctx, ownerID)
	if err != nil {
		return Org{}, err
	}
	if owner.IsOrg {
		return Org{}, ErrIsOrg
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Org{}, err
	}
	defer tx.Rollback() //nolint:errcheck
	now := Now()
	res, err := tx.ExecContext(ctx, `INSERT INTO users (name, password_hash, is_admin, lang, created_at, kind) VALUES (?, '!', 0, ?, ?, 'org')`, name, owner.Lang, now)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return Org{}, ErrExists
		}
		return Org{}, err
	}
	id, _ := res.LastInsertId()
	if _, err := tx.ExecContext(ctx, `INSERT INTO org_members (org_id, user_id, role, created_at) VALUES (?, ?, 'owner', ?)`, id, ownerID, now); err != nil {
		return Org{}, err
	}
	if err := tx.Commit(); err != nil {
		return Org{}, err
	}
	return s.OrgByName(ctx, name)
}

// OrgByName loads an organisation; a person's name is ErrNotFound.
func (s *Store) OrgByName(ctx context.Context, name string) (Org, error) {
	u, err := s.UserByName(ctx, name)
	if err != nil {
		return Org{}, err
	}
	if !u.IsOrg {
		return Org{}, ErrNotFound
	}
	o := Org{User: u}
	err = s.db.QueryRowContext(ctx, `SELECT org_base_role FROM users WHERE id = ?`, u.ID).Scan(&o.BaseRole)
	return o, err
}

// SetOrgBaseRole sets what every member gets on every org repository.
func (s *Store) SetOrgBaseRole(ctx context.Context, orgID int64, role string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE users SET org_base_role = ? WHERE id = ? AND kind = 'org'`, role, orgID)
	return err
}

// OrgRole is a person's role in an organisation, "" when not a member.
func (s *Store) OrgRole(ctx context.Context, orgID, userID int64) (string, error) {
	var role string
	err := s.db.QueryRowContext(ctx, `SELECT role FROM org_members WHERE org_id = ? AND user_id = ?`, orgID, userID).Scan(&role)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return role, err
}

// OrgMembers lists members, owners first.
func (s *Store) OrgMembers(ctx context.Context, orgID int64) ([]OrgMember, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT m.user_id, u.name, m.role FROM org_members m JOIN users u ON u.id = m.user_id WHERE m.org_id = ? ORDER BY m.role DESC, u.name`, orgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []OrgMember
	for rows.Next() {
		var m OrgMember
		if err := rows.Scan(&m.UserID, &m.Name, &m.Role); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func (s *Store) ownerCount(ctx context.Context, tx *sql.Tx, orgID int64) (int, error) {
	var n int
	err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM org_members WHERE org_id = ? AND role = 'owner'`, orgID).Scan(&n)
	return n, err
}

// SetOrgMember adds a person or changes their role; demoting the last owner is refused.
func (s *Store) SetOrgMember(ctx context.Context, orgID, userID int64, role string) error {
	if u, err := s.UserByID(ctx, userID); err != nil {
		return err
	} else if u.IsOrg {
		return ErrIsOrg
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck
	if _, err := tx.ExecContext(ctx, `INSERT INTO org_members (org_id, user_id, role, created_at) VALUES (?, ?, ?, ?) ON CONFLICT (org_id, user_id) DO UPDATE SET role = excluded.role`,
		orgID, userID, role, Now()); err != nil {
		return err
	}
	if n, err := s.ownerCount(ctx, tx, orgID); err != nil {
		return err
	} else if n == 0 {
		return ErrLastOwner
	}
	return tx.Commit()
}

// RemoveOrgMember takes a person out of the organisation and its teams; the last owner stays.
func (s *Store) RemoveOrgMember(ctx context.Context, orgID, userID int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck
	if _, err := tx.ExecContext(ctx, `DELETE FROM org_members WHERE org_id = ? AND user_id = ?`, orgID, userID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM team_members WHERE user_id = ? AND team_id IN (SELECT id FROM teams WHERE org_id = ?)`, userID, orgID); err != nil {
		return err
	}
	if n, err := s.ownerCount(ctx, tx, orgID); err != nil {
		return err
	} else if n == 0 {
		return ErrLastOwner
	}
	return tx.Commit()
}

// OrgsOf lists the organisations a person belongs to, with their role.
func (s *Store) OrgsOf(ctx context.Context, userID int64) ([]OrgMember, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT o.id, o.name, m.role FROM org_members m JOIN users o ON o.id = m.org_id WHERE m.user_id = ? ORDER BY o.name`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []OrgMember
	for rows.Next() {
		var m OrgMember
		if err := rows.Scan(&m.UserID, &m.Name, &m.Role); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// CreateTeam adds a team.
func (s *Store) CreateTeam(ctx context.Context, orgID int64, name string) (int64, error) {
	res, err := s.db.ExecContext(ctx, `INSERT INTO teams (org_id, name, created_at) VALUES (?, ?, ?)`, orgID, name, Now())
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return 0, ErrExists
		}
		return 0, err
	}
	return res.LastInsertId()
}

// DeleteTeam removes a team of orgID (and what it granted).
func (s *Store) DeleteTeam(ctx context.Context, orgID, teamID int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM teams WHERE id = ? AND org_id = ?`, teamID, orgID)
	return err
}

func (s *Store) teamInOrg(ctx context.Context, orgID, teamID int64) error {
	var one int
	err := s.db.QueryRowContext(ctx, `SELECT 1 FROM teams WHERE id = ? AND org_id = ?`, teamID, orgID).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	return err
}

// SetTeamMember adds an org member to a team (someone outside the org is refused).
func (s *Store) SetTeamMember(ctx context.Context, orgID, teamID, userID int64) error {
	if err := s.teamInOrg(ctx, orgID, teamID); err != nil {
		return err
	}
	if role, err := s.OrgRole(ctx, orgID, userID); err != nil {
		return err
	} else if role == "" {
		return ErrNotFound
	}
	_, err := s.db.ExecContext(ctx, `INSERT OR IGNORE INTO team_members (team_id, user_id) VALUES (?, ?)`, teamID, userID)
	return err
}

// RemoveTeamMember takes a person out of a team.
func (s *Store) RemoveTeamMember(ctx context.Context, orgID, teamID, userID int64) error {
	if err := s.teamInOrg(ctx, orgID, teamID); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `DELETE FROM team_members WHERE team_id = ? AND user_id = ?`, teamID, userID)
	return err
}

// SetTeamRepo grants a team a role on one of the org's own repositories.
func (s *Store) SetTeamRepo(ctx context.Context, orgID, teamID, repoID int64, role string) error {
	if err := s.teamInOrg(ctx, orgID, teamID); err != nil {
		return err
	}
	r, err := s.RepoByID(ctx, repoID)
	if err != nil {
		return err
	}
	if r.OwnerID != orgID {
		return ErrNotFound // a team only reaches its own organisation's repositories
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO team_repos (team_id, repo_id, role) VALUES (?, ?, ?) ON CONFLICT (team_id, repo_id) DO UPDATE SET role = excluded.role`, teamID, repoID, role)
	return err
}

// RemoveTeamRepo withdraws a team's role on a repository.
func (s *Store) RemoveTeamRepo(ctx context.Context, orgID, teamID, repoID int64) error {
	if err := s.teamInOrg(ctx, orgID, teamID); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `DELETE FROM team_repos WHERE team_id = ? AND repo_id = ?`, teamID, repoID)
	return err
}

// Teams lists an organisation's teams with members and repositories.
func (s *Store) Teams(ctx context.Context, orgID int64) ([]Team, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, org_id, name FROM teams WHERE org_id = ? ORDER BY name`, orgID)
	if err != nil {
		return nil, err
	}
	var out []Team
	for rows.Next() {
		var t Team
		if err := rows.Scan(&t.ID, &t.OrgID, &t.Name); err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, t)
	}
	rows.Close()
	for i := range out {
		mr, err := s.db.QueryContext(ctx, `SELECT u.name FROM team_members m JOIN users u ON u.id = m.user_id WHERE m.team_id = ? ORDER BY u.name`, out[i].ID)
		if err != nil {
			return nil, err
		}
		for mr.Next() {
			var n string
			_ = mr.Scan(&n)
			out[i].Members = append(out[i].Members, n)
		}
		mr.Close()
		rr, err := s.db.QueryContext(ctx, `SELECT r.id, r.name, t.role FROM team_repos t JOIN repos r ON r.id = t.repo_id WHERE t.team_id = ? ORDER BY r.name`, out[i].ID)
		if err != nil {
			return nil, err
		}
		for rr.Next() {
			var tr TeamRepo
			_ = rr.Scan(&tr.RepoID, &tr.Repo, &tr.Role)
			out[i].Repos = append(out[i].Repos, tr)
		}
		rr.Close()
	}
	return out, nil
}
