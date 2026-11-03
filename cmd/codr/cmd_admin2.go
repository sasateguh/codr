package main

import (
	"errors"
	"flag"
	"fmt"
	"strconv"
	"strings"
	"time"

	"codr.my/codr/internal/names"
	"codr.my/codr/internal/store"
)

// tokenAdd mints an access token for HTTPS git (D8). The token is printed once.
func (a *admin) tokenAdd(args []string) error {
	name, rest, err := a.positional(args, "NAME")
	if err != nil {
		return err
	}
	var title, scopes string
	var days int64
	if _, err := a.flags("token add", rest, func(fs *flag.FlagSet) {
		fs.StringVar(&title, "name", "", "")
		fs.StringVar(&scopes, "scopes", store.ScopeWrite, "")
		fs.Int64Var(&days, "days", 0, "")
	}); err != nil {
		return err
	}
	u, err := a.user(name)
	if err != nil {
		return err
	}
	sc, ok := store.ParseScopes(scopes)
	if !ok {
		return a.usage("token.bad_scopes", scopes, strings.Join([]string{store.ScopeRead, store.ScopeWrite, store.ScopeAdmin}, ","))
	}
	maxDays, err := a.st.SettingInt(a.ctx, "token.max_days")
	if err != nil {
		return err
	}
	if days == 0 {
		if days, err = a.st.SettingInt(a.ctx, "token.default_days"); err != nil {
			return err
		}
	}
	if days < 1 || days > maxDays {
		return a.usage("token.bad_days", maxDays)
	}
	if title == "" {
		title = "cli " + store.Now()[:10]
	}
	raw, t, err := a.st.CreateToken(a.ctx, u.ID, title, sc, time.Duration(days)*24*time.Hour)
	if err != nil {
		return err
	}
	_ = a.st.Audit(a.ctx, 0, "token.create", u.Name, title+" "+strings.Join(sc, ","))
	fmt.Fprintln(a.out, a.t("token.created", u.Name, t.ID, title, strings.Join(sc, ","), t.ExpiresAt[:10], raw, a.cfg.Server.HTTPHost, u.Name))
	return nil
}

func (a *admin) tokenLs(args []string) error {
	name, _, err := a.positional(args, "NAME")
	if err != nil {
		return err
	}
	u, err := a.user(name)
	if err != nil {
		return err
	}
	list, err := a.st.Tokens(a.ctx, u.ID)
	if err != nil {
		return err
	}
	for _, t := range list {
		last := t.LastUsedAt
		if last == "" {
			last = "-"
		}
		fmt.Fprintln(a.out, a.t("token.ls_row", t.ID, t.Name, strings.Join(t.Scopes, ","), t.ExpiresAt[:10], last))
	}
	return nil
}

func (a *admin) tokenRm(args []string) error {
	name, rest, err := a.positional(args, "NAME")
	if err != nil {
		return err
	}
	idStr, _, err := a.positional(rest, "ID")
	if err != nil {
		return err
	}
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		return a.usage("cli.need_arg", "ID")
	}
	u, err := a.user(name)
	if err != nil {
		return err
	}
	if err := a.st.DeleteToken(a.ctx, u.ID, id); errors.Is(err, store.ErrNotFound) {
		return a.usage("token.not_found", id, u.Name)
	} else if err != nil {
		return err
	}
	_ = a.st.Audit(a.ctx, 0, "token.revoke", u.Name, strconv.FormatInt(id, 10))
	fmt.Fprintln(a.out, a.t("token.removed", id, u.Name))
	return nil
}

func (a *admin) repo(ref string) (store.Repo, error) {
	owner, name, err := a.repoRef(ref)
	if err != nil {
		return store.Repo{}, err
	}
	r, err := a.st.RepoByPath(a.ctx, owner, name)
	if errors.Is(err, store.ErrNotFound) {
		return r, a.usage("repo.not_found", ref)
	}
	return r, err
}

// protectAdd adds a branch rule: protected = no delete, no force push, unless allowed.
func (a *admin) protectAdd(args []string) error {
	ref, rest, err := a.positional(args, "OWNER/NAME")
	if err != nil {
		return err
	}
	pattern, rest, err := a.positional(rest, "PATTERN")
	if err != nil {
		return err
	}
	var force, del, checks bool
	if _, err := a.flags("protect add", rest, func(fs *flag.FlagSet) {
		fs.BoolVar(&force, "allow-force", false, "")
		fs.BoolVar(&del, "allow-delete", false, "")
		fs.BoolVar(&checks, "require-checks", false, "")
	}); err != nil {
		return err
	}
	r, err := a.repo(ref)
	if err != nil {
		return err
	}
	if err := a.st.Protect(a.ctx, r.ID, pattern, force, del, checks); err != nil {
		return a.usage("protect.bad_pattern", pattern)
	}
	_ = a.st.Audit(a.ctx, 0, "protect.set", ref, fmt.Sprintf("%s force=%t delete=%t checks=%t", pattern, force, del, checks))
	fmt.Fprintln(a.out, a.t("protect.added", pattern, ref, force, del, checks))
	return nil
}

func (a *admin) protectLs(args []string) error {
	ref, _, err := a.positional(args, "OWNER/NAME")
	if err != nil {
		return err
	}
	r, err := a.repo(ref)
	if err != nil {
		return err
	}
	on, err := a.st.SettingBool(a.ctx, "repo.protect_default_branch")
	if err != nil {
		return err
	}
	fmt.Fprintln(a.out, a.t("protect.default_branch", r.DefaultBranch, on))
	rules, err := a.st.Protections(a.ctx, r.ID)
	if err != nil {
		return err
	}
	for _, p := range rules {
		fmt.Fprintln(a.out, a.t("protect.ls_row", p.Pattern, p.AllowForce, p.AllowDelete, p.RequireChecks))
	}
	return nil
}

func (a *admin) protectRm(args []string) error {
	ref, rest, err := a.positional(args, "OWNER/NAME")
	if err != nil {
		return err
	}
	pattern, _, err := a.positional(rest, "PATTERN")
	if err != nil {
		return err
	}
	r, err := a.repo(ref)
	if err != nil {
		return err
	}
	if err := a.st.Unprotect(a.ctx, r.ID, pattern); errors.Is(err, store.ErrNotFound) {
		return a.usage("protect.not_found", pattern, ref)
	} else if err != nil {
		return err
	}
	_ = a.st.Audit(a.ctx, 0, "protect.unset", ref, pattern)
	fmt.Fprintln(a.out, a.t("protect.removed", pattern, ref))
	return nil
}

// settingLs prints every product setting (D14): the admin screen edits the same rows.
func (a *admin) settingLs() error {
	rows, err := a.st.Settings(a.ctx)
	if err != nil {
		return err
	}
	for _, r := range rows {
		fmt.Fprintln(a.out, a.t("setting.ls_row", r.Key, r.Value, r.Type, r.UpdatedAt[:10]))
	}
	return nil
}

func (a *admin) settingSet(args []string) error {
	key, rest, err := a.positional(args, "KEY")
	if err != nil {
		return err
	}
	value, _, err := a.positional(rest, "VALUE")
	if err != nil {
		return err
	}
	typ, err := a.st.SetSettingChecked(a.ctx, key, value, 0)
	switch {
	case errors.Is(err, store.ErrNotFound):
		return a.usage("setting.not_found", key)
	case errors.Is(err, store.ErrBadValue):
		return a.usage("setting.bad_value", key, typ)
	case err != nil:
		return err
	}
	_ = a.st.Audit(a.ctx, 0, "setting.set", key, value)
	fmt.Fprintln(a.out, a.t("setting.updated", key, value))
	return nil
}

// repoDesc replaces a repository's description: codr admin repo desc OWNER/NAME TEXT… (empty TEXT clears it).
func (a *admin) repoDesc(args []string) error {
	ref, rest, err := a.positional(args, "OWNER/NAME")
	if err != nil {
		return err
	}
	owner, name, err := a.repoRef(ref)
	if err != nil {
		return err
	}
	r, err := a.st.RepoByPath(a.ctx, owner, name)
	if errors.Is(err, store.ErrNotFound) {
		return a.usage("repo.not_found", ref)
	}
	if err != nil {
		return err
	}
	desc := strings.TrimSpace(strings.Join(rest, " "))
	if len(desc) > 255 { // the same limit as the web form (ui_new.go)
		desc = desc[:255]
	}
	if err := a.st.SetDescription(a.ctx, r.ID, desc); err != nil {
		return err
	}
	_ = a.st.Audit(a.ctx, 0, "repo.describe", owner+"/"+name, "cli")
	fmt.Fprintln(a.out, a.t("repo.described", owner, name))
	return nil
}

// orgAdd creates an organisation with one owner (D74): codr admin org add NAME --owner USER.
func (a *admin) orgAdd(args []string) error {
	name, rest, err := a.positional(args, "NAME")
	if err != nil {
		return err
	}
	var owner string
	if _, err := a.flags("org add", rest, func(fs *flag.FlagSet) { fs.StringVar(&owner, "owner", "", "") }); err != nil {
		return err
	}
	if owner == "" {
		return a.usage("cli.need_arg", "--owner USER")
	}
	if err := names.User(name); err != nil {
		return a.usage("org.bad_name", name)
	}
	u, err := a.user(owner)
	if err != nil {
		return err
	}
	o, err := a.st.CreateOrg(a.ctx, name, u.ID)
	if errors.Is(err, store.ErrExists) {
		return a.usage("org.exists", name)
	}
	if err != nil {
		return err
	}
	_ = a.st.Audit(a.ctx, 0, "org.create", o.Name, "cli owner="+u.Name)
	fmt.Fprintln(a.out, a.t("org.created", o.Name, u.Name))
	return nil
}

// orgLs lists organisations with their owners.
func (a *admin) orgLs() error {
	users, err := a.st.Users(a.ctx)
	if err != nil {
		return err
	}
	for _, u := range users {
		if !u.IsOrg {
			continue
		}
		ms, err := a.st.OrgMembers(a.ctx, u.ID)
		if err != nil {
			return err
		}
		var owners []string
		for _, m := range ms {
			if m.Role == "owner" {
				owners = append(owners, m.Name)
			}
		}
		fmt.Fprintln(a.out, a.t("org.ls_row", u.Name, len(ms), strings.Join(owners, ",")))
	}
	return nil
}
