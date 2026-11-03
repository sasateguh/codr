package store

import (
	"context"
	"path"
)

// Protection is a row of protected_branches.
type Protection struct {
	RepoID      int64
	Pattern     string
	AllowForce  bool
	AllowDelete bool
	// RequireChecks: the branch only moves to a commit whose commit statuses are all success (at least one, D89).
	RequireChecks bool
	CreatedAt     string
}

// Protect adds or updates a protection rule for a branch glob.
func (s *Store) Protect(ctx context.Context, repoID int64, pattern string, allowForce, allowDelete, requireChecks bool) error {
	if _, err := path.Match(pattern, ""); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO protected_branches (repo_id, pattern, allow_force, allow_delete, require_checks, created_at) VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(repo_id, pattern) DO UPDATE SET allow_force = excluded.allow_force, allow_delete = excluded.allow_delete, require_checks = excluded.require_checks`,
		repoID, pattern, allowForce, allowDelete, requireChecks, Now())
	return err
}

// Unprotect removes a rule.
func (s *Store) Unprotect(ctx context.Context, repoID int64, pattern string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM protected_branches WHERE repo_id = ? AND pattern = ?`, repoID, pattern)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// Protections lists a repo's rules.
func (s *Store) Protections(ctx context.Context, repoID int64) ([]Protection, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT repo_id, pattern, allow_force, allow_delete, require_checks, created_at FROM protected_branches WHERE repo_id = ? ORDER BY pattern`, repoID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Protection
	for rows.Next() {
		var p Protection
		if err := rows.Scan(&p.RepoID, &p.Pattern, &p.AllowForce, &p.AllowDelete, &p.RequireChecks, &p.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// BranchRule is the effective rule for one branch: whether it is protected and what is allowed.
type BranchRule struct {
	Protected   bool
	AllowForce  bool
	AllowDelete bool
	// RequireChecks: any matching rule asking for green checks makes the branch ask for them (strictest wins).
	RequireChecks bool
}

// RuleFor computes the rule for a branch: explicit patterns first (the most permissive match
// wins so an admin can carve exceptions), then the implicit default-branch rule from the
// setting repo.protect_default_branch.
func (s *Store) RuleFor(ctx context.Context, r Repo, branch string) (BranchRule, error) {
	rules, err := s.Protections(ctx, r.ID)
	if err != nil {
		return BranchRule{}, err
	}
	var out BranchRule
	for _, p := range rules {
		if ok, _ := path.Match(p.Pattern, branch); ok {
			out.Protected = true
			out.AllowForce = out.AllowForce || p.AllowForce
			out.AllowDelete = out.AllowDelete || p.AllowDelete
			out.RequireChecks = out.RequireChecks || p.RequireChecks
		}
	}
	if out.Protected {
		return out, nil
	}
	if branch == r.DefaultBranch {
		on, err := s.SettingBool(ctx, "repo.protect_default_branch")
		if err != nil {
			return BranchRule{}, err
		}
		if on {
			return BranchRule{Protected: true}, nil
		}
	}
	return out, nil
}
