package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"codr.my/codr/internal/ghimport"
	"codr.my/codr/internal/secret"
	"codr.my/codr/internal/store"
)

// `codr admin github import OWNER/NAME [--meta DIR]` and `codr admin github import --all --meta ROOT` (D72).
// With --meta the GitHub mirror's JSON Lines files are read (no network); without it the repository must be a
// mirror of github.com and its sealed token is used against the REST API. --all walks every mirror whose
// source is file:///cermin/<name>.git and reads ROOT/<name>/.
func (a *admin) githubImport(args []string) error {
	var all bool
	var meta string
	ref := ""
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		ref, args = args[0], args[1:]
	}
	if _, err := a.flags("github import", args, func(fs *flag.FlagSet) {
		fs.BoolVar(&all, "all", false, "")
		fs.StringVar(&meta, "meta", "", "")
	}); err != nil {
		return err
	}
	by, err := a.st.FirstAdmin(a.ctx)
	if err != nil {
		return a.usage("github.no_admin")
	}
	if all {
		if meta == "" || ref != "" {
			return a.usage("cli.need_arg", "--all --meta ROOT")
		}
		mirrors, err := a.st.Mirrors(a.ctx)
		if err != nil {
			return err
		}
		for _, m := range mirrors {
			name := strings.TrimSuffix(strings.TrimPrefix(m.URL, "file:///cermin/"), ".git")
			if name == m.URL || strings.ContainsAny(name, "/\\") {
				continue // not a host mirror
			}
			dir := filepath.Join(meta, name)
			if st, err := os.Stat(dir); err != nil || !st.IsDir() {
				continue
			}
			if err := a.importOne(m.RepoID, ghimport.Dir(dir), by.ID); err != nil {
				return err
			}
		}
		return nil
	}
	if ref == "" {
		return a.usage("cli.need_arg", "OWNER/NAME")
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
	var src ghimport.Source
	if meta != "" {
		src = ghimport.Dir(meta)
	} else {
		m, err := a.st.MirrorOf(a.ctx, r.ID)
		if err != nil {
			return a.usage("github.not_github", ref)
		}
		gho, ghr := ghimport.ParseGitHubURL(m.URL)
		if gho == "" {
			return a.usage("github.not_github", ref)
		}
		token := ""
		if m.TokenEnc != "" {
			key, err := secret.Parse(a.cfg.Auth.SecretKey)
			if err != nil || key == nil {
				return a.usage("cloud.no_secret_key")
			}
			if token, err = key.Open(m.TokenEnc); err != nil {
				return err
			}
		}
		src = ghimport.API{Owner: gho, Repo: ghr, Token: token}
	}
	return a.importOne(r.ID, src, by.ID)
}

func (a *admin) importOne(repoID int64, src ghimport.Source, by int64) error {
	r, err := a.st.RepoByID(a.ctx, repoID)
	if err != nil {
		return err
	}
	res, err := ghimport.Import(a.ctx, a.st, repoID, by, src)
	if err != nil {
		return fmt.Errorf("%s/%s: %w", r.Owner, r.Name, err)
	}
	if res.Issues+res.Pulls+res.Comments+res.Updated+len(res.Skipped) == 0 {
		return nil
	}
	_ = a.st.Audit(a.ctx, 0, "github.import", r.Owner+"/"+r.Name, fmt.Sprintf("%d issues, %d pulls, %d comments new; %d updated; %d skipped", res.Issues, res.Pulls, res.Comments, res.Updated, len(res.Skipped)))
	fmt.Fprintln(a.out, a.t("github.imported", r.Owner, r.Name, res.Issues, res.Pulls, res.Comments, res.Updated))
	if len(res.Skipped) > 0 {
		fmt.Fprintln(a.out, a.t("github.skipped", strings.Join(res.Skipped, " ")))
	}
	return nil
}
