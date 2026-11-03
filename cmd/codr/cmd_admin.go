package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"golang.org/x/crypto/ssh"
	"golang.org/x/term"

	"codr.my/codr/internal/auth"
	"codr.my/codr/internal/config"
	"codr.my/codr/internal/i18n"
	"codr.my/codr/internal/lfs"
	"codr.my/codr/internal/names"
	"codr.my/codr/internal/repos"
	"codr.my/codr/internal/store"
)

// cmdAdmin dispatches `admin user|key|repo|collab ...`.
func (c *cli) cmdAdmin(args []string) error {
	if len(args) < 2 {
		return c.usage("cli.usage")
	}
	cfg, err := c.loadConfig()
	if err != nil {
		return err
	}
	st, err := store.Open(cfg.DBPath())
	if err != nil {
		return err
	}
	defer st.Close()
	a := &admin{cli: c, cfg: cfg, st: st, ctx: context.Background(),
		disk: repos.Disk{Root: cfg.ReposDir(), GitBinary: cfg.Git.Binary}}
	switch args[0] + " " + args[1] {
	case "user add":
		return a.userAdd(args[2:])
	case "user ls":
		return a.userLs()
	case "user 2fa-off":
		return a.user2FAOff(args[2:])
	case "user passwd":
		return a.userPasswd(args[2:])
	case "key add":
		return a.keyAdd(args[2:])
	case "key ls":
		return a.keyLs(args[2:])
	case "key rm":
		return a.keyRm(args[2:])
	case "repo add":
		return a.repoAdd(args[2:])
	case "repo ls":
		return a.repoLs(args[2:])
	case "repo rm":
		return a.repoRm(args[2:])
	case "repo desc":
		return a.repoDesc(args[2:])
	case "github import":
		return a.githubImport(args[2:])
	case "org add":
		return a.orgAdd(args[2:])
	case "org ls":
		return a.orgLs()
	case "collab add":
		return a.collabAdd(args[2:])
	case "token add":
		return a.tokenAdd(args[2:])
	case "token ls":
		return a.tokenLs(args[2:])
	case "token rm":
		return a.tokenRm(args[2:])
	case "protect add":
		return a.protectAdd(args[2:])
	case "protect ls":
		return a.protectLs(args[2:])
	case "protect rm":
		return a.protectRm(args[2:])
	case "setting ls":
		return a.settingLs()
	case "setting set":
		return a.settingSet(args[2:])
	case "repo import":
		return a.repoImport(args[2:])
	case "mirror ls":
		return a.mirrorLs()
	case "mirror sync":
		return a.mirrorSync(args[2:])
	case "mirror rm":
		return a.mirrorRm(args[2:])
	}
	return c.usage("cli.unknown_command", "admin "+args[0]+" "+args[1])
}

type admin struct {
	*cli
	cfg  config.Config
	st   *store.Store
	ctx  context.Context
	disk repos.Disk
}

// positional returns args[0] or a usage error naming what is missing.
func (a *admin) positional(args []string, what string) (string, []string, error) {
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		return "", nil, a.usage("cli.need_arg", what)
	}
	return args[0], args[1:], nil
}

func (a *admin) flags(name string, args []string, set func(*flag.FlagSet)) (*flag.FlagSet, error) {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	set(fs)
	if err := fs.Parse(args); err != nil {
		return nil, a.usage("cli.bad_flag", strings.Join(args, " "))
	}
	return fs, nil
}

func (a *admin) nameErr(err error) error {
	switch {
	case errors.Is(err, names.ErrUser):
		return a.usage("names.user_invalid", names.MaxUser)
	case errors.Is(err, names.ErrRepo):
		return a.usage("names.repo_invalid", names.MaxRepo)
	case errors.Is(err, names.ErrReserved):
		return a.usage("names.reserved", err)
	}
	return err
}

// readPassword takes the password from stdin: hidden when a terminal, first line otherwise.
func (a *admin) readPassword() (string, error) {
	if f, ok := a.in.(*os.File); ok && term.IsTerminal(int(f.Fd())) {
		fmt.Fprint(a.errOut, a.t("user.password_prompt"))
		raw, err := term.ReadPassword(int(f.Fd()))
		fmt.Fprintln(a.errOut)
		return string(raw), err
	}
	line, err := bufio.NewReader(a.in).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	return strings.TrimRight(line, "\r\n"), nil
}

func (a *admin) userAdd(args []string) error {
	name, rest, err := a.positional(args, "NAME")
	if err != nil {
		return err
	}
	var email, lang string
	var isAdmin bool
	if _, err := a.flags("user add", rest, func(fs *flag.FlagSet) {
		fs.StringVar(&email, "email", "", "")
		fs.StringVar(&lang, "lang", a.lang, "")
		fs.BoolVar(&isAdmin, "admin", false, "")
	}); err != nil {
		return err
	}
	if err := names.User(name); err != nil {
		return a.nameErr(err)
	}
	lang = i18n.Normalize(lang, a.lang)
	pw, err := a.readPassword()
	if err != nil {
		return err
	}
	if len(pw) < a.cfg.Auth.PasswordMinLen {
		return a.usage("user.password_short", a.cfg.Auth.PasswordMinLen)
	}
	hash, err := auth.Hash(pw, auth.Params{Time: a.cfg.Auth.Argon2Time, Memory: a.cfg.Auth.Argon2MemoryKiB, Threads: a.cfg.Auth.Argon2Threads})
	if err != nil {
		return err
	}
	u, err := a.st.CreateUser(a.ctx, name, email, hash, isAdmin, lang)
	if err != nil {
		return err
	}
	_ = a.st.Audit(a.ctx, 0, "user.create", u.Name, "cli")
	fmt.Fprintln(a.out, a.t("user.created", u.Name, u.ID, u.IsAdmin))
	return nil
}

func (a *admin) userLs() error {
	users, err := a.st.Users(a.ctx)
	if err != nil {
		return err
	}
	fmt.Fprintln(a.out, a.t("user.ls_header"))
	for _, u := range users {
		if u.IsOrg {
			continue // `codr admin org ls`
		}
		fmt.Fprintln(a.out, a.t("user.ls_row", u.ID, u.Name, u.IsAdmin, u.Lang, u.CreatedAt))
	}
	return nil
}

func (a *admin) user(name string) (store.User, error) {
	u, err := a.st.UserByName(a.ctx, name)
	if errors.Is(err, store.ErrNotFound) {
		return u, a.usage("user.not_found", name)
	}
	return u, err
}

func (a *admin) keyAdd(args []string) error {
	name, rest, err := a.positional(args, "NAME")
	if err != nil {
		return err
	}
	var title string
	if _, err := a.flags("key add", rest, func(fs *flag.FlagSet) { fs.StringVar(&title, "title", "", "") }); err != nil {
		return err
	}
	u, err := a.user(name)
	if err != nil {
		return err
	}
	raw, err := io.ReadAll(io.LimitReader(a.in, 16*1024))
	if err != nil {
		return err
	}
	pub, comment, _, _, err := ssh.ParseAuthorizedKey(raw)
	if err != nil {
		return a.usage("key.invalid", err)
	}
	if title == "" {
		title = comment
	}
	if title == "" {
		title = pub.Type()
	}
	fp := ssh.FingerprintSHA256(pub)
	k, err := a.st.AddSSHKey(a.ctx, u.ID, title, fp, strings.TrimSpace(string(ssh.MarshalAuthorizedKey(pub))))
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return a.usage("key.duplicate")
		}
		return err
	}
	_ = a.st.Audit(a.ctx, 0, "key.add", u.Name, fp)
	fmt.Fprintln(a.out, a.t("key.added", u.Name, k.Fingerprint, k.Title))
	return nil
}

func (a *admin) keyLs(args []string) error {
	name, _, err := a.positional(args, "NAME")
	if err != nil {
		return err
	}
	u, err := a.user(name)
	if err != nil {
		return err
	}
	keys, err := a.st.SSHKeys(a.ctx, u.ID)
	if err != nil {
		return err
	}
	for _, k := range keys {
		fmt.Fprintln(a.out, a.t("key.ls_row", k.ID, k.Fingerprint, k.Title, k.CreatedAt))
	}
	return nil
}

func (a *admin) repoRef(ref string) (owner, name string, err error) {
	owner, name, err = names.SplitRef(ref)
	if err != nil {
		if strings.Count(ref, "/") != 1 {
			return "", "", a.usage("repo.bad_ref", ref)
		}
		return "", "", a.nameErr(err)
	}
	return owner, name, nil
}

func (a *admin) repoAdd(args []string) error {
	ref, rest, err := a.positional(args, "OWNER/NAME")
	if err != nil {
		return err
	}
	var desc, branch string
	var public bool
	if _, err := a.flags("repo add", rest, func(fs *flag.FlagSet) {
		fs.StringVar(&desc, "desc", "", "")
		fs.StringVar(&branch, "branch", "", "")
		fs.BoolVar(&public, "public", false, "")
	}); err != nil {
		return err
	}
	owner, name, err := a.repoRef(ref)
	if err != nil {
		return err
	}
	u, err := a.user(owner)
	if err != nil {
		return err
	}
	if branch == "" {
		if branch, err = a.st.Setting(a.ctx, "repo.default_branch"); err != nil {
			return err
		}
	}
	r, err := a.st.CreateRepo(a.ctx, u.ID, name, desc, !public, branch)
	if err != nil {
		return err
	}
	if err := a.disk.Create(a.ctx, r.ID, branch); err != nil {
		_ = a.st.DeleteRepo(a.ctx, r.ID)
		return err
	}
	_ = a.st.Audit(a.ctx, 0, "repo.create", owner+"/"+name, "cli")
	vis := a.t("repo.private")
	if public {
		vis = a.t("repo.public")
	}
	fmt.Fprintln(a.out, a.t("repo.created", owner, name, r.ID, vis, a.disk.Path(r.ID), a.cfg.Server.SSHHost, owner, name))
	return nil
}

func (a *admin) repoLs(args []string) error {
	owner := ""
	if len(args) > 0 {
		owner = args[0]
	}
	list, err := a.st.Repos(a.ctx, owner)
	if err != nil {
		return err
	}
	for _, r := range list {
		vis := a.t("repo.private")
		if !r.Private {
			vis = a.t("repo.public")
		}
		fmt.Fprintln(a.out, a.t("repo.ls_row", r.ID, r.Owner, r.Name, vis, r.DefaultBranch, a.disk.Path(r.ID)))
	}
	return nil
}

func (a *admin) repoRm(args []string) error {
	ref, rest, err := a.positional(args, "OWNER/NAME")
	if err != nil {
		return err
	}
	var yes bool
	if _, err := a.flags("repo rm", rest, func(fs *flag.FlagSet) { fs.BoolVar(&yes, "yes", false, "") }); err != nil {
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
	if !yes {
		return a.usage("repo.rm_need_yes", owner, name)
	}
	if err := a.disk.Delete(r.ID); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := (lfs.Store{Root: a.cfg.LFSDir()}).Delete(r.ID); err != nil { // its LFS objects go with it (D82)
		return err
	}
	if err := a.st.DeleteRepo(a.ctx, r.ID); err != nil {
		return err
	}
	_ = a.st.Audit(a.ctx, 0, "repo.delete", owner+"/"+name, "cli")
	fmt.Fprintln(a.out, a.t("repo.removed", owner, name, r.ID))
	return nil
}

func (a *admin) collabAdd(args []string) error {
	ref, rest, err := a.positional(args, "OWNER/NAME")
	if err != nil {
		return err
	}
	user, rest, err := a.positional(rest, "USER")
	if err != nil {
		return err
	}
	var role string
	if _, err := a.flags("collab add", rest, func(fs *flag.FlagSet) { fs.StringVar(&role, "role", "read", "") }); err != nil {
		return err
	}
	access, ok := store.ParseAccess(role)
	if !ok {
		return a.usage("cli.bad_flag", "--role "+role)
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
	u, err := a.user(user)
	if err != nil {
		return err
	}
	if err := a.st.SetCollaborator(a.ctx, r.ID, u.ID, access); err != nil {
		return err
	}
	_ = a.st.Audit(a.ctx, 0, "collab.set", owner+"/"+name, u.Name+"="+access.String())
	fmt.Fprintln(a.out, a.t("collab.added", u.Name, access.String(), owner, name))
	return nil
}

// keyRm revokes one SSH key; the SSH door looks keys up per connection, so the next
// handshake with that key is refused.
func (a *admin) keyRm(args []string) error {
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
	if err := a.st.DeleteSSHKey(a.ctx, u.ID, id); errors.Is(err, store.ErrNotFound) {
		return a.usage("key.not_found", id, u.Name)
	} else if err != nil {
		return err
	}
	_ = a.st.Audit(a.ctx, 0, "key.remove", u.Name, strconv.FormatInt(id, 10))
	fmt.Fprintln(a.out, a.t("key.removed", id, u.Name))
	return nil
}
