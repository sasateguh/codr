package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"codr.my/codr/internal/config"
	"codr.my/codr/internal/lfs"
	"codr.my/codr/internal/store"
)

type doctor struct {
	*cli
	ok, warn, fail int
}

func (d *doctor) line(level string, key string, args ...any) {
	switch level {
	case "ok":
		d.ok++
	case "warn":
		d.warn++
	default:
		d.fail++
	}
	fmt.Fprintln(d.out, d.t("doctor."+level)+" "+d.t(key, args...))
}

// cmdDoctor checks the install the way an operator would at 3 a.m.: each line names what
// was checked and what to do. Exit 1 when anything failed.
func (c *cli) cmdDoctor(args []string) error {
	if len(args) != 0 {
		return c.usage("cli.bad_flag", strings.Join(args, " "))
	}
	d := &doctor{cli: c}
	fmt.Fprintln(c.out, c.t("doctor.title", c.configPath))
	cfg, err := config.Load(c.configPath)
	if err != nil {
		d.line("fail", "doctor.config_fail", err)
		return d.summary()
	}
	d.line("ok", "doctor.config", c.configPath)
	ctx := context.Background()

	if f, err := os.CreateTemp(cfg.DataDir(), ".doctor-*"); err != nil {
		d.line("fail", "doctor.data_fail", err)
	} else {
		f.Close()
		_ = os.Remove(f.Name())
		d.line("ok", "doctor.data", cfg.DataDir())
	}

	st, err := store.Open(cfg.DBPath())
	if err != nil {
		d.line("fail", "doctor.db_fail", err)
		return d.summary()
	}
	defer st.Close()
	v, pending, err := st.Pending(ctx)
	switch {
	case err != nil:
		d.line("fail", "doctor.db_fail", err)
	case pending > 0:
		d.line("fail", "doctor.db_pending", pending)
	default:
		d.line("ok", "doctor.db", v)
	}

	if out, err := exec.Command(cfg.Git.Binary, "--version").Output(); err != nil { // #nosec G204 -- operator config
		d.line("fail", "doctor.git_fail", err)
	} else {
		if v := strings.TrimSpace(string(out)); gitAtLeast(v, 2, 40) {
			d.line("ok", "doctor.git", v)
		} else {
			// rebase/squash merges use `merge-tree --merge-base`, added in git 2.40 (D88)
			d.line("fail", "doctor.git_old", v, "2.40")
		}
		// git-http-backend ships in a separate package on some distros (Alpine: git-daemon); without
		// it every HTTPS clone is a 500 while SSH keeps working.
		if ep, err := exec.Command(cfg.Git.Binary, "--exec-path").Output(); err == nil { // #nosec G204 -- operator config
			if !fileExists(filepath.Join(strings.TrimSpace(string(ep)), "git-http-backend")) {
				d.line("fail", "doctor.http_backend_missing", strings.TrimSpace(string(ep)))
			} else {
				d.line("ok", "doctor.http_backend")
			}
		}
	}

	raw, err := os.ReadFile(filepath.Join(cfg.HooksDir(), "pre-receive")) // #nosec G304 -- data dir
	if err != nil {
		d.line("warn", "doctor.hooks_missing")
	} else if bin := hookBinary(string(raw)); bin == "" || !fileExists(bin) {
		d.line("fail", "doctor.hooks_stale", bin)
	} else {
		d.line("ok", "doctor.hooks")
	}

	sock := cfg.HookSocket()
	if filepath.Dir(sock) != cfg.DataDir() {
		d.line("warn", "doctor.socket_long", sock)
	}
	if fi, err := os.Stat(sock); err == nil && fi.Mode()&os.ModeSocket != 0 {
		d.line("ok", "doctor.socket", sock, c.t("doctor.serve_up"))
	} else {
		d.line("warn", "doctor.serve_down")
	}

	if cfg.Auth.SecretKey == "" {
		d.line("warn", "doctor.secret_missing")
	} else {
		d.line("ok", "doctor.secret")
	}

	switch {
	case len(cfg.Server.TrustedProxies) > 0:
		d.line("ok", "doctor.proxy", strings.Join(cfg.Server.TrustedProxies, ", "), cfg.Server.ClientIPHeader)
	case strings.HasPrefix(cfg.Server.HTTPHost, "https://"):
		d.line("warn", "doctor.proxy_none", cfg.Server.HTTPHost)
	default:
		d.line("ok", "doctor.proxy_direct")
	}

	minMB, _ := st.SettingInt(ctx, "doctor.min_free_mb")
	if free, err := freeMB(cfg.DataDir()); err == nil {
		if free < minMB {
			d.line("fail", "doctor.disk_low", mb(free), mb(minMB))
		} else {
			d.line("ok", "doctor.disk", mb(free), mb(minMB))
		}
	}

	all, err := st.Repos(ctx, "")
	if err == nil {
		var missing []string
		known := map[string]bool{}
		for _, r := range all {
			p := filepath.Join(cfg.ReposDir(), strconv.FormatInt(r.ID, 10)+".git")
			known[filepath.Base(p)] = true
			known[strconv.FormatInt(r.ID, 10)+".wiki.git"] = true // its wiki (D83) belongs to it too
			if !fileExists(filepath.Join(p, "HEAD")) {
				missing = append(missing, r.Owner+"/"+r.Name)
			}
		}
		var orphans []string
		if entries, err := os.ReadDir(cfg.ReposDir()); err == nil {
			for _, e := range entries {
				if e.IsDir() && strings.HasSuffix(e.Name(), ".git") && !known[e.Name()] {
					orphans = append(orphans, e.Name())
				}
			}
		}
		sort.Strings(missing)
		sort.Strings(orphans)
		switch {
		case len(missing) > 0:
			d.line("fail", "doctor.repos_missing", strings.Join(missing, ", "))
		case len(orphans) > 0:
			d.line("warn", "doctor.repos_orphan", strings.Join(orphans, ", "))
		default:
			d.line("ok", "doctor.repos", len(all))
		}
	}

	if mirrors, err := st.Mirrors(ctx); err == nil {
		var bad, stale []string
		for _, m := range mirrors {
			name := strconv.FormatInt(m.RepoID, 10)
			if r, err := st.RepoByID(ctx, m.RepoID); err == nil {
				name = r.Owner + "/" + r.Name
			}
			if m.LastError != "" {
				bad = append(bad, name+" ("+m.LastError+")")
			} else if m.LastOKAt == "" {
				stale = append(stale, name)
			}
		}
		switch {
		case len(bad) > 0:
			d.line("fail", "doctor.mirrors_err", strings.Join(bad, "; "))
		case len(stale) > 0:
			d.line("warn", "doctor.mirrors_stale", strings.Join(stale, ", "))
		default:
			d.line("ok", "doctor.mirrors", len(mirrors))
		}
	}
	d.reaper("/proc")
	d.cloud(ctx, st)
	d.webhooks(ctx, st)
	d.mail(ctx, st)
	d.lfs(ctx, st, cfg.LFSDir())
	return d.summary()
}

// reaper (D98): when codr itself is PID 1 of a container with no init, nothing reaps orphaned children. git detaches
// `maintenance run --auto` after every fetch and push, so each mirror refresh left one defunct process behind (measured
// 19 Oct 2026: 1,022 in 14 hours, 73 mirrors x hourly). No /proc (not Linux) = no line.
func (d *doctor) reaper(proc string) {
	pid1, zombies, err := reaperState(proc)
	if err != nil {
		return
	}
	if pid1 == "codr" {
		d.line("fail", "doctor.reaper_codr", zombies)
		return
	}
	d.line("ok", "doctor.reaper", pid1, zombies)
}

// reaperState reads the name of PID 1 and counts defunct processes whose parent is PID 1.
func reaperState(proc string) (string, int, error) {
	root, err := os.OpenRoot(proc) // every read below stays inside proc
	if err != nil {
		return "", 0, err
	}
	defer root.Close()
	b, err := root.ReadFile("1/comm")
	if err != nil {
		return "", 0, err
	}
	dir, err := root.Open(".")
	if err != nil {
		return "", 0, err
	}
	entries, err := dir.ReadDir(-1)
	dir.Close()
	if err != nil {
		return "", 0, err
	}
	n := 0
	for _, e := range entries {
		if _, err := strconv.Atoi(e.Name()); err != nil {
			continue
		}
		st, err := root.ReadFile(e.Name() + "/stat")
		if err != nil {
			continue
		}
		// "pid (comm) state ppid ...": comm may hold spaces or parentheses, so read after the last ')'
		i := strings.LastIndexByte(string(st), ')')
		if i < 0 {
			continue
		}
		f := strings.Fields(string(st[i+1:]))
		if len(f) >= 2 && f[0] == "Z" && f[1] == "1" {
			n++
		}
	}
	return strings.TrimSpace(string(b)), n, nil
}

// webhooks: a line only when webhooks exist (D78); deliveries that failed for good in the last day are a warning.
func (d *doctor) webhooks(ctx context.Context, st *store.Store) {
	active, failed, err := st.WebhookHealth(ctx, time.Now().UTC().Add(-24*time.Hour).Format("2006-01-02T15:04:05.000Z"))
	switch {
	case err != nil:
		d.line("fail", "doctor.db_fail", err)
	case failed > 0:
		d.line("warn", "doctor.webhooks_failed", active, failed)
	case active > 0:
		d.line("ok", "doctor.webhooks", active)
	}
}

// lfs: a line only when LFS objects exist (D82); an object folder with no repository row is a warning.
func (d *doctor) lfs(ctx context.Context, st *store.Store, root string) {
	ls := lfs.Store{Root: root}
	ids, err := ls.Repos()
	if err != nil || len(ids) == 0 {
		return
	}
	var objects int
	var bytes int64
	var orphans []string
	for _, id := range ids {
		if _, err := st.RepoByID(ctx, id); err != nil {
			orphans = append(orphans, strconv.FormatInt(id, 10))
			continue
		}
		n, b, _ := ls.Usage(id)
		objects, bytes = objects+n, bytes+b
	}
	if len(orphans) > 0 {
		d.line("warn", "doctor.lfs_orphan", strings.Join(orphans, ", "))
	}
	d.line("ok", "doctor.lfs", objects, byteSize(bytes), len(ids)-len(orphans))
}

// mail: a line only when notification email is switched on (D79).
func (d *doctor) mail(ctx context.Context, st *store.Store) {
	c, err := st.MailConfig(ctx)
	switch {
	case err != nil:
		d.line("fail", "doctor.db_fail", err)
	case !c.Enabled:
	case c.LastError != "":
		d.line("warn", "doctor.mail_failed", c.Host, c.LastError)
	case c.LastOKAt == "":
		d.line("warn", "doctor.mail_untested", c.Host)
	default:
		d.line("ok", "doctor.mail", c.Host, c.LastOKAt)
	}
}

func (d *doctor) summary() error {
	n := d.ok + d.warn + d.fail
	if d.fail == 0 {
		fmt.Fprintln(d.out, d.t("doctor.summary_ok", n))
		return nil
	}
	return errors.New(d.t("doctor.summary_fail", d.fail, d.warn, n))
}

// hookBinary extracts the quoted binary path from a script WriteScripts produced.
func hookBinary(script string) string {
	for _, line := range strings.Split(script, "\n") {
		if rest, ok := strings.CutPrefix(line, "exec '"); ok {
			if i := strings.Index(rest, "' hook "); i > 0 {
				return strings.ReplaceAll(rest[:i], `'\''`, "'")
			}
		}
	}
	return ""
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

func freeMB(path string) (int64, error) {
	var fs syscall.Statfs_t
	if err := syscall.Statfs(path, &fs); err != nil {
		return 0, err
	}
	return int64(fs.Bavail) * int64(fs.Bsize) / (1 << 20), nil // #nosec G115 -- block counts fit
}

// byteSize formats a byte count (mb takes megabytes).
func byteSize(n int64) string {
	switch {
	case n >= 1<<30:
		return strconv.FormatFloat(float64(n)/(1<<30), 'f', 1, 64) + " GB"
	case n >= 1<<20:
		return strconv.FormatFloat(float64(n)/(1<<20), 'f', 1, 64) + " MB"
	}
	return strconv.FormatFloat(float64(n)/1024, 'f', 1, 64) + " KB"
}

func mb(n int64) string {
	if n >= 1024 {
		return strconv.FormatFloat(float64(n)/1024, 'f', 1, 64) + " GB"
	}
	return strconv.FormatInt(n, 10) + " MB"
}

// gitAtLeast reports whether `git --version` output ("git version 2.43.0", "git version 2.39.5 (Apple Git-154)")
// is at least major.minor. Unreadable output counts as too old.
func gitAtLeast(out string, major, minor int) bool {
	f := strings.Fields(out)
	if len(f) < 3 {
		return false
	}
	parts := strings.SplitN(f[2], ".", 3)
	if len(parts) < 2 {
		return false
	}
	ma, err1 := strconv.Atoi(parts[0])
	mi, err2 := strconv.Atoi(parts[1])
	if err1 != nil || err2 != nil {
		return false
	}
	return ma > major || ma == major && mi >= minor
}
