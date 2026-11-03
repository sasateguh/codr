package runner

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"codr.my/codr/internal/i18n"
)

// Execer runs one command; the exit code is 0 on success, -1 when it could not start or was killed.
type Execer interface {
	Run(ctx context.Context, dir string, env []string, out io.Writer, name string, args ...string) (int, error)
}

type osExec struct{}

func (osExec) Run(ctx context.Context, dir string, env []string, out io.Writer, name string, args ...string) (int, error) {
	cmd := exec.CommandContext(ctx, name, args...) // #nosec G204 -- fixed verbs; user values are validated or passed as one argument
	cmd.Dir, cmd.Stdout, cmd.Stderr = dir, out, out
	cmd.Env = append([]string{"PATH=" + os.Getenv("PATH"), "HOME=" + os.Getenv("HOME"), "LANG=C.UTF-8"}, env...)
	err := cmd.Run()
	var ee *exec.ExitError
	if errors.As(err, &ee) && ctx.Err() == nil {
		return ee.ExitCode(), nil
	}
	if err != nil {
		return -1, err
	}
	return 0, nil
}

// capped writes at most n bytes, then notes once that the rest was cut.
type capped struct {
	w    io.Writer
	n    int
	cut  bool
	note string
}

func (c *capped) Write(p []byte) (int, error) {
	if c.n <= 0 {
		if !c.cut {
			c.cut = true
			_, _ = io.WriteString(c.w, "\n"+c.note+"\n")
		}
		return len(p), nil
	}
	q := p
	if len(q) > c.n {
		q = q[:c.n]
	}
	c.n -= len(q)
	if _, err := c.w.Write(q); err != nil {
		return 0, err
	}
	return len(p), nil
}

func (rn *Runner) t(key string, args ...any) string { return i18n.T(rn.Cfg.Lang, key, args...) }

// status posts one commit status; a failure is logged, never fatal (the job still runs).
func (rn *Runner) status(ctx context.Context, p push, context, state, desc, logID string) {
	if r := []rune(desc); len(r) > 140 {
		desc = string(r[:139]) + "…"
	}
	body := map[string]string{"state": state, "context": context, "description": desc}
	if logID != "" {
		body["target_url"] = strings.TrimRight(rn.Cfg.PublicURL, "/") + "/ci/log/" + logID
	}
	b, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(rn.Cfg.ForgeURL, "/")+"/api/v1/repos/"+p.Repo+"/statuses/"+p.SHA, bytes.NewReader(b))
	if err != nil {
		return
	}
	req.Header.Set("Authorization", "Bearer "+rn.Cfg.Token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := rn.http.Do(req)
	if err != nil {
		rn.Log.Error("status", "repo", p.Repo, "sha", p.SHA, "err", err)
		return
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		rn.Log.Error("status", "repo", p.Repo, "sha", p.SHA, "code", resp.StatusCode)
	}
}

func (rn *Runner) git(ctx context.Context, dir string, out io.Writer, args ...string) (int, error) {
	// the token travels as an HTTP header through git's environment config, never in a URL or an argument (D38)
	env := []string{"GIT_TERMINAL_PROMPT=0", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_COUNT=1",
		"GIT_CONFIG_KEY_0=http.extraHeader", "GIT_CONFIG_VALUE_0=Authorization: Bearer " + rn.Cfg.Token}
	return rn.Exec.Run(ctx, dir, env, out, rn.Cfg.Git, args...)
}

// fetch makes dir a checkout of p.SHA. It asks for the commit itself first; when the forge refuses an unadvertised
// commit, it fetches the branch and looks for the commit in its recent history.
func (rn *Runner) fetch(ctx context.Context, dir string, p push, out io.Writer) error {
	url := strings.TrimRight(rn.Cfg.ForgeURL, "/") + "/" + p.Repo + ".git"
	if code, err := rn.git(ctx, dir, out, "init", "-q"); err != nil || code != 0 {
		return fmt.Errorf("git init: %d %v", code, err)
	}
	if code, _ := rn.git(ctx, dir, out, "fetch", "-q", "--depth=1", "--end-of-options", url, p.SHA); code != 0 {
		if code, err := rn.git(ctx, dir, out, "fetch", "-q", "--depth=50", "--end-of-options", url, "refs/heads/"+p.Branch); err != nil || code != 0 {
			return fmt.Errorf("git fetch: %d %v", code, err)
		}
	}
	if code, err := rn.git(ctx, dir, out, "-c", "advice.detachedHead=false", "checkout", "-q", "--detach", p.SHA); err != nil || code != 0 {
		return fmt.Errorf("git checkout %s: %d %v", p.SHA, code, err)
	}
	return nil
}

// run handles one push: fetch, read .codr/ci.toml, run each job, report each.
func (rn *Runner) run(ctx context.Context, p push) {
	id := newLogID()
	dir := filepath.Join(rn.Cfg.WorkDir, id)
	defer os.RemoveAll(dir) //nolint:errcheck
	if err := os.MkdirAll(dir, 0o700); err != nil {
		rn.Log.Error("workdir", "err", err)
		return
	}
	if err := os.MkdirAll(rn.Cfg.LogDir, 0o700); err != nil {
		rn.Log.Error("logdir", "err", err)
		return
	}
	var fetchLog bytes.Buffer
	if err := rn.fetch(ctx, dir, p, &fetchLog); err != nil {
		rn.Log.Error("fetch", "repo", p.Repo, "sha", p.SHA, "err", err, "out", strings.TrimSpace(fetchLog.String()))
		rn.writeLog(id, rn.t("runner.log.head", p.Repo, p.Branch, p.SHA)+"\n"+fetchLog.String()+"\n"+err.Error()+"\n")
		rn.status(ctx, p, "ci", "error", rn.t("runner.status.fetch_failed"), id)
		return
	}
	raw, err := os.ReadFile(filepath.Join(dir, CIPath)) // #nosec G304 -- inside our own checkout
	if errors.Is(err, os.ErrNotExist) {
		return // no CI in this repository at this commit: nothing to say
	}
	if err != nil {
		rn.Log.Error("read ci", "err", err)
		return
	}
	file, jobs, err := Parse(raw, rn.Cfg.MaxJobs, rn.Cfg.MaxSteps)
	if errors.Is(err, ErrNoJobs) {
		return
	}
	if err != nil {
		rn.writeLog(id, rn.t("runner.log.head", p.Repo, p.Branch, p.SHA)+"\n"+err.Error()+"\n")
		rn.status(ctx, p, "ci", "error", err.Error(), id)
		return
	}
	if !file.Wants(p.Branch) {
		return
	}
	logs := map[string]string{}
	for _, j := range jobs { // every job is pending at once, so the pull request shows what is coming
		logs[j.Name] = newLogID()
		rn.status(ctx, p, "ci/"+j.Name, "pending", rn.t("runner.status.queued"), logs[j.Name])
	}
	for _, j := range jobs {
		rn.job(ctx, p, dir, j, logs[j.Name])
	}
}

func (rn *Runner) writeLog(id, text string) {
	_ = os.WriteFile(filepath.Join(rn.Cfg.LogDir, id+".log"), []byte(text), 0o600)
}

// job runs one job in its own container and reports it.
func (rn *Runner) job(ctx context.Context, p push, dir string, j Job, logID string) {
	f, err := os.OpenFile(filepath.Join(rn.Cfg.LogDir, logID+".log"), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600) // #nosec G304 -- random hex name
	if err != nil {
		rn.Log.Error("log file", "err", err)
		return
	}
	defer f.Close()
	out := &capped{w: f, n: rn.Cfg.LogMaxKB << 10, note: rn.t("runner.log.cut", rn.Cfg.LogMaxKB)}
	limit := rn.Cfg.TimeoutMinutes
	if j.TimeoutMinutes > 0 && j.TimeoutMinutes < limit {
		limit = j.TimeoutMinutes
	}
	fmt.Fprintln(out, rn.t("runner.log.head", p.Repo, p.Branch, p.SHA))
	fmt.Fprintln(out, rn.t("runner.log.job", j.Name, j.Image, limit))
	fmt.Fprintln(out)
	rn.status(ctx, p, "ci/"+j.Name, "pending", rn.t("runner.status.running"), logID)
	name := "codr-ci-" + logID
	user := rn.Cfg.User
	if user == "" {
		user = strconv.Itoa(os.Getuid()) + ":" + strconv.Itoa(os.Getgid())
	}
	args := []string{"run", "--rm", "--name", name, "--memory", rn.Cfg.Memory, "--memory-swap", rn.Cfg.Memory, "--cpus", rn.Cfg.CPUs,
		"--pids-limit", strconv.Itoa(rn.Cfg.Pids), "--cap-drop", "ALL", "--security-opt", "no-new-privileges", "--read-only",
		"--tmpfs", "/tmp:rw,exec,size=" + rn.Cfg.TmpSize, "--user", user,
		"-e", "HOME=/tmp", "-e", "CI=true", "-e", "CODR_REPO=" + p.Repo, "-e", "CODR_BRANCH=" + p.Branch, "-e", "CODR_SHA=" + p.SHA,
		"-v", dir + ":/src", "-w", "/src", "--entrypoint", "sh"}
	if !rn.Cfg.AllowNetwork {
		args = append(args, "--network", "none")
	}
	args = append(args, "--", j.Image, "-c", j.Script())
	unit := rn.unit
	if unit == 0 {
		unit = time.Minute
	}
	jctx, cancel := context.WithTimeout(ctx, time.Duration(limit)*unit)
	defer cancel()
	start := time.Now()
	code, err := rn.Exec.Run(jctx, dir, nil, out, rn.Cfg.Docker, args...)
	took := time.Since(start).Round(time.Second)
	switch {
	case jctx.Err() != nil:
		// the docker client is gone but the container is not: remove it by name
		_, _ = rn.Exec.Run(context.Background(), dir, nil, io.Discard, rn.Cfg.Docker, "rm", "-f", name)
		fmt.Fprintln(out, "\n"+rn.t("runner.log.timeout", limit))
		rn.status(ctx, p, "ci/"+j.Name, "failure", rn.t("runner.status.timeout", limit), logID)
	case err != nil:
		fmt.Fprintln(out, "\n"+err.Error())
		rn.status(ctx, p, "ci/"+j.Name, "error", rn.t("runner.status.error"), logID)
	case code == 0:
		fmt.Fprintln(out, "\n"+rn.t("runner.log.passed", took))
		rn.status(ctx, p, "ci/"+j.Name, "success", rn.t("runner.status.passed", took), logID)
	default:
		fmt.Fprintln(out, "\n"+rn.t("runner.log.failed", code, took))
		rn.status(ctx, p, "ci/"+j.Name, "failure", rn.t("runner.status.failed", code, took), logID)
	}
	rn.Log.Info("job", "repo", p.Repo, "sha", p.SHA[:10], "job", j.Name, "code", code, "took", took.String())
}
