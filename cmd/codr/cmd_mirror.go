package main

import (
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"codr.my/codr/internal/mirror"
	"codr.my/codr/internal/secret"
	"codr.my/codr/internal/store"
)

func (a *admin) syncer() (*mirror.Syncer, error) {
	key, err := secret.Parse(a.cfg.Auth.SecretKey)
	if err != nil {
		return nil, err
	}
	return &mirror.Syncer{Store: a.st, Disk: a.disk, Git: a.cfg.Git.Binary, Timeout: time.Duration(a.cfg.Git.TimeoutSeconds) * time.Second,
		Key: key, Log: slog.New(slog.NewTextHandler(a.errOut, &slog.HandlerOptions{Level: slog.LevelWarn}))}, nil
}

// repoImport creates a repo that mirrors URL and runs the first sync. The token (if --token)
// is read from stdin, sealed under auth.secret_key, and never printed.
func (a *admin) repoImport(args []string) error {
	ref, rest, err := a.positional(args, "OWNER/NAME")
	if err != nil {
		return err
	}
	src, rest, err := a.positional(rest, "URL")
	if err != nil {
		return err
	}
	var desc string
	var public, withToken bool
	var interval int64
	if _, err := a.flags("repo import", rest, func(fs *flag.FlagSet) {
		fs.StringVar(&desc, "desc", "", "")
		fs.BoolVar(&public, "public", false, "")
		fs.BoolVar(&withToken, "token", false, "")
		fs.Int64Var(&interval, "interval", 0, "")
	}); err != nil {
		return err
	}
	if err := mirror.ValidURL(src, true); err != nil {
		return a.usage("mirror.bad_url", src)
	}
	owner, name, err := a.repoRef(ref)
	if err != nil {
		return err
	}
	u, err := a.user(owner)
	if err != nil {
		return err
	}
	sy, err := a.syncer()
	if err != nil {
		return err
	}
	tokenEnc := ""
	if withToken {
		if sy.Key == nil {
			return a.usage("mirror.no_key", a.configPath)
		}
		tok, err := a.readPassword()
		if err != nil {
			return err
		}
		if tokenEnc, err = sy.Key.Seal(strings.TrimSpace(tok)); err != nil {
			return err
		}
	}
	if interval == 0 {
		if interval, err = a.st.SettingInt(a.ctx, "mirror.default_interval_minutes"); err != nil {
			return err
		}
	}
	if interval < 1 {
		return a.usage("mirror.bad_interval")
	}
	branch, err := a.st.Setting(a.ctx, "repo.default_branch")
	if err != nil {
		return err
	}
	r, err := a.st.CreateRepo(a.ctx, u.ID, name, desc, !public, branch)
	if err != nil {
		return err
	}
	if err := a.disk.Create(a.ctx, r.ID, branch); err != nil {
		_ = a.st.DeleteRepo(a.ctx, r.ID)
		return err
	}
	if err := a.st.SetMirror(a.ctx, r.ID, src, tokenEnc, interval); err != nil {
		return err
	}
	_ = a.st.Audit(a.ctx, 0, "repo.import", ref, src)
	fmt.Fprintln(a.out, a.t("mirror.created", ref, src, interval))
	if err := sy.Sync(a.ctx, r.ID); err != nil {
		return fmt.Errorf("%s", a.t("mirror.sync_failed", ref, err))
	}
	rp, _ := a.st.RepoByID(a.ctx, r.ID)
	fmt.Fprintln(a.out, a.t("mirror.synced", ref, rp.DefaultBranch))
	return nil
}

func (a *admin) mirrorLs() error {
	list, err := a.st.Mirrors(a.ctx)
	if err != nil {
		return err
	}
	for _, m := range list {
		r, err := a.st.RepoByID(a.ctx, m.RepoID)
		if err != nil {
			continue
		}
		tok := "-"
		if m.TokenEnc != "" {
			tok = "token"
		}
		last := m.LastOKAt
		if last == "" {
			last = "-"
		}
		status := a.t("mirror.ok")
		if m.LastError != "" {
			status = a.t("mirror.error", m.LastError)
		}
		fmt.Fprintln(a.out, a.t("mirror.ls_row", r.Owner+"/"+r.Name, m.URL, tok, m.IntervalMinutes, last, status))
	}
	return nil
}

func (a *admin) mirrorSync(args []string) error {
	var all bool
	fs, err := a.flags("mirror sync", args, func(fs *flag.FlagSet) { fs.BoolVar(&all, "all", false, "") })
	if err != nil {
		return err
	}
	sy, err := a.syncer()
	if err != nil {
		return err
	}
	var ids []int64
	var names []string
	if all {
		list, err := a.st.Mirrors(a.ctx)
		if err != nil {
			return err
		}
		for _, m := range list {
			if r, err := a.st.RepoByID(a.ctx, m.RepoID); err == nil {
				ids, names = append(ids, m.RepoID), append(names, r.Owner+"/"+r.Name)
			}
		}
	} else {
		ref, _, err := a.positional(fs.Args(), "OWNER/NAME")
		if err != nil {
			return err
		}
		r, err := a.repo(ref)
		if err != nil {
			return err
		}
		if !r.Mirror {
			return a.usage("mirror.not_mirror", ref)
		}
		ids, names = []int64{r.ID}, []string{ref}
	}
	failed := 0
	for i, id := range ids {
		if err := sy.Sync(a.ctx, id); err != nil {
			failed++
			fmt.Fprintln(a.errOut, a.t("mirror.sync_failed", names[i], err))
			continue
		}
		rp, _ := a.st.RepoByID(a.ctx, id)
		fmt.Fprintln(a.out, a.t("mirror.synced", names[i], rp.DefaultBranch))
	}
	if failed > 0 {
		return fmt.Errorf("%s", a.t("mirror.some_failed", failed, len(ids)))
	}
	return nil
}

func (a *admin) mirrorRm(args []string) error {
	ref, _, err := a.positional(args, "OWNER/NAME")
	if err != nil {
		return err
	}
	r, err := a.repo(ref)
	if err != nil {
		return err
	}
	if err := a.st.RemoveMirror(a.ctx, r.ID); errors.Is(err, store.ErrNotFound) {
		return a.usage("mirror.not_mirror", ref)
	} else if err != nil {
		return err
	}
	_ = a.st.Audit(a.ctx, 0, "mirror.remove", ref, "")
	fmt.Fprintln(a.out, a.t("mirror.removed", ref))
	return nil
}
