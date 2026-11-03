package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"codr.my/codr/internal/backup"
	"codr.my/codr/internal/config"
	"codr.my/codr/internal/lfs"
	"codr.my/codr/internal/repos"
	"codr.my/codr/internal/store"
)

// cmdBackup writes a consistent snapshot: DB via VACUUM INTO (WAL-safe), config.toml, one git
// bundle per repo, and a manifest with every HEAD. Nothing is copied from a live file.
// Mirror repos are listed with their HEAD but not bundled (D54): their source URL is in the DB and
// the scheduler refills them after a restore; --with-mirrors bundles them too.
func (c *cli) cmdBackup(args []string) error {
	fs := flag.NewFlagSet("backup", flag.ContinueOnError)
	fs.SetOutput(c.errOut)
	withMirrors := fs.Bool("with-mirrors", false, "")
	if err := fs.Parse(args); err != nil || fs.NArg() != 1 {
		return c.usage("cli.need_arg", "DIR")
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
	// An older schema than this binary knows means the queries below would fail half-way with a bare SQL
	// error (v2.13.0 deploy: "no such column: r.fork_of"). Refuse up front and say which binary to use.
	if v, pending, err := st.Pending(context.Background()); err != nil {
		return err
	} else if pending > 0 {
		return fmt.Errorf("%s", c.t("backup.schema_behind", v, pending))
	}
	res, err := backup.Snapshot(context.Background(), backup.Options{Store: st, Disk: repos.Disk{Root: cfg.ReposDir(), GitBinary: cfg.Git.Binary},
		GitBinary: cfg.Git.Binary, ConfigPath: c.configPath, DestRoot: fs.Arg(0), WithMirrors: *withMirrors, LFSRoot: cfg.LFSDir()})
	var missing backup.RepoMissing
	if errors.As(err, &missing) {
		return fmt.Errorf("%s", c.t("backup.repo_missing", missing.Ref, missing.Path))
	}
	if err != nil {
		return err
	}
	fmt.Fprintln(c.out, c.t("backup.done", res.Dir, len(res.Manifest.Repos), res.Manifest.Schema))
	if res.MirrorsSkipped > 0 {
		fmt.Fprintln(c.out, c.t("backup.mirrors_skipped", res.MirrorsSkipped))
	}
	return nil
}

// cmdRestore rebuilds an instance from a backup into an EMPTY directory: config.toml (data_dir
// rewritten), DB, repos from bundles, HEADs verified against the manifest.
func (c *cli) cmdRestore(args []string) error {
	// `restore BACKUP --dir DIR`: the positional comes first, so flags are picked out by hand
	// (package flag stops at the first non-flag argument).
	var src, dirFlag string
	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "--dir" && i+1 < len(args):
			dirFlag = args[i+1]
			i++
		case strings.HasPrefix(args[i], "--dir="):
			dirFlag = strings.TrimPrefix(args[i], "--dir=")
		case strings.HasPrefix(args[i], "-") || src != "":
			return c.usage("cli.need_arg", "BACKUP --dir DIR")
		default:
			src = args[i]
		}
	}
	if src == "" || dirFlag == "" {
		return c.usage("cli.need_arg", "BACKUP --dir DIR")
	}
	dir := &dirFlag
	m, err := backup.ReadManifest(src)
	if err != nil {
		return c.usage("restore.bad_backup", src)
	}
	abs, err := filepath.Abs(*dir)
	if err != nil {
		return err
	}
	if entries, err := os.ReadDir(abs); err == nil && len(entries) > 0 {
		return c.usage("restore.not_empty", abs)
	}
	if err := os.MkdirAll(filepath.Join(abs, "data", "repos"), 0o700); err != nil {
		return err
	}
	cfg, err := config.Load(filepath.Join(src, "config.toml"))
	if err != nil {
		return err
	}
	cfg.Server.DataDir = "data"
	cfgPath := filepath.Join(abs, "config.toml")
	if err := config.Write(cfgPath, cfg); err != nil {
		return err
	}
	cfg.Dir = abs
	db, err := os.ReadFile(filepath.Join(src, config.DBName)) // #nosec G304 -- operator path
	if os.IsNotExist(err) {                                   // a backup written before the rename (D58)
		db, err = os.ReadFile(filepath.Join(src, config.LegacyDBName)) // #nosec G304 -- operator path
	}
	if err != nil {
		return c.usage("restore.bad_backup", src)
	}
	if err := os.WriteFile(cfg.DBPath(), db, 0o600); err != nil {
		return err
	}
	disk := repos.Disk{Root: cfg.ReposDir(), GitBinary: cfg.Git.Binary}
	refill := 0
	for _, r := range m.Repos {
		if r.WikiBundle != "" { // the wiki (D83): a bare repository fetched from its bundle, then its head checked
			wp := disk.WikiPath(r.ID)
			if _, err := backup.GitOut(cfg.Git.Binary, "init", "--quiet", "--bare", "--initial-branch=main", wp); err != nil {
				return err
			}
			if _, err := backup.GitOut(cfg.Git.Binary, "-C", wp, "-c", "fetch.fsckObjects=true", "fetch", "--quiet", "--", filepath.Join(src, "repos", r.WikiBundle), "+refs/*:refs/*"); err != nil {
				return fmt.Errorf("restore wiki %s: %w", r.Ref, err)
			}
			if head, err := backup.GitOut(cfg.Git.Binary, "-C", wp, "rev-parse", "--verify", "-q", "refs/heads/main^{commit}"); err != nil || head != r.WikiHead {
				return fmt.Errorf("%s", c.t("restore.head_mismatch", r.Ref+" (wiki)", head, r.WikiHead))
			}
		}
		branch := r.Branch
		if branch == "" {
			branch = "main"
		}
		if err := disk.Create(context.Background(), r.ID, branch); err != nil {
			return err
		}
		if r.Bundle == "" {
			if r.Mirror && r.Head != "" {
				refill++ // empty folder now; the mirror scheduler fetches it from the source URL (D54)
			}
			continue
		}
		p := disk.Path(r.ID)
		if _, err := backup.GitOut(cfg.Git.Binary, "-C", p, "-c", "fetch.fsckObjects=true", "fetch", "--quiet", "--", filepath.Join(src, "repos", r.Bundle), "+refs/*:refs/*"); err != nil {
			return fmt.Errorf("restore %s: %w", r.Ref, err)
		}
		if err := disk.SetDefaultBranch(context.Background(), r.ID, branch); err != nil {
			return err
		}
		head, err := backup.GitOut(cfg.Git.Binary, "-C", p, "rev-parse", "--verify", "-q", "HEAD^{commit}")
		if err != nil || head != r.Head {
			return fmt.Errorf("%s", c.t("restore.head_mismatch", r.Ref, head, r.Head))
		}
	}
	// LFS objects (D82): linked back from the snapshot, then every object is hashed against its name and counted
	lfsFrom, dst := lfs.Store{Root: filepath.Join(src, "lfs")}, lfs.Store{Root: cfg.LFSDir()}
	for _, l := range m.LFS {
		if _, err := lfsFrom.LinkAll(l.ID, dst, l.ID); err != nil {
			return fmt.Errorf("restore lfs %d: %w", l.ID, err)
		}
		n, b, err := dst.Usage(l.ID)
		bad, verr := dst.Verify(l.ID)
		if err != nil || verr != nil || n != l.Objects || b != l.Bytes || len(bad) > 0 {
			return fmt.Errorf("%s", c.t("restore.lfs_mismatch", l.ID, n, l.Objects, len(bad)))
		}
	}
	st, err := store.Open(cfg.DBPath())
	if err != nil {
		return err
	}
	defer st.Close()
	v, pending, err := st.Pending(context.Background())
	if err != nil {
		return err
	}
	if v != m.Schema {
		return fmt.Errorf("%s", c.t("restore.schema_mismatch", m.Schema, v))
	}
	_ = st.Audit(context.Background(), 0, "backup.restore", src, abs)
	c.configPath = cfgPath
	fmt.Fprintln(c.out, c.t("restore.done", abs, len(m.Repos), v, pending))
	if refill > 0 {
		fmt.Fprintln(c.out, c.t("restore.mirrors_refill", refill))
	}
	return nil
}
