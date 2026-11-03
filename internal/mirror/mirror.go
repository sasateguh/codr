// Package mirror pulls a repository from a URL into a bare repo on disk and keeps it fresh
// (FR-08). Credentials travel through git's credential helper from an environment variable,
// never inside the URL; the stored token is sealed with the config key (internal/secret).
package mirror

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"time"

	"codr.my/codr/internal/repos"
	"codr.my/codr/internal/secret"
	"codr.my/codr/internal/store"
)

// ErrBadURL rejects URLs the mirror will not fetch from.
var ErrBadURL = errors.New("mirror: bad url")

// ValidURL accepts https/http/ssh URLs and scp-like git@host:path. allowLocal additionally
// accepts file:// and absolute paths (CLI on the host only; the web form never passes it).
func ValidURL(u string, allowLocal bool) error {
	if u == "" || len(u) > 1024 || strings.ContainsAny(u, " \t\n\r'\"`$\\") || strings.HasPrefix(u, "-") {
		return ErrBadURL
	}
	if strings.HasPrefix(u, "git@") && strings.Contains(u, ":") && !strings.Contains(u, "://") {
		return nil
	}
	p, err := url.Parse(u)
	if err != nil {
		return ErrBadURL
	}
	switch p.Scheme {
	case "https", "http", "ssh":
		if p.Host == "" {
			return ErrBadURL
		}
		if p.User != nil { // a user name is normal for ssh (git@host); a password never is
			if _, has := p.User.Password(); has || p.Scheme != "ssh" {
				return ErrBadURL
			}
		}
		return nil
	case "file":
		if allowLocal {
			return nil
		}
	}
	if allowLocal && strings.HasPrefix(u, "/") {
		return nil
	}
	return ErrBadURL
}

// Syncer fetches mirrors.
type Syncer struct {
	Store   *store.Store
	Disk    repos.Disk
	Git     string
	Timeout time.Duration
	Key     secret.Key
	Log     *slog.Logger
}

// Sync fetches every branch and tag from the mirror's source (forced, pruned) and points
// HEAD at the source's default branch. The outcome is recorded on the mirror row and audited.
func (s *Syncer) Sync(ctx context.Context, repoID int64) error {
	m, err := s.Store.MirrorOf(ctx, repoID)
	if err != nil {
		return err
	}
	repo, err := s.Store.RepoByID(ctx, repoID)
	if err != nil {
		return err
	}
	token, err := s.Key.Open(m.TokenEnc)
	if err != nil {
		return s.record(ctx, repo, err)
	}
	ctx, cancel := context.WithTimeout(ctx, s.Timeout)
	defer cancel()
	path := s.Disk.Path(repoID)
	if _, err := s.git(ctx, token, "-C", path, "fetch", "--quiet", "--prune", "--force", "--", m.URL,
		"+refs/heads/*:refs/heads/*", "+refs/tags/*:refs/tags/*"); err != nil {
		return s.record(ctx, repo, err)
	}
	// Default branch follows the source.
	if out, err := s.git(ctx, token, "ls-remote", "--symref", "--", m.URL, "HEAD"); err == nil {
		for _, line := range strings.Split(out, "\n") {
			if branch, ok := strings.CutPrefix(line, "ref: refs/heads/"); ok {
				branch = strings.TrimSpace(strings.TrimSuffix(branch, "HEAD"))
				if branch != "" && branch != repo.DefaultBranch {
					if err := s.Disk.SetDefaultBranch(ctx, repoID, branch); err == nil {
						_ = s.Store.SetDefaultBranch(ctx, repoID, branch)
					}
				}
				break
			}
		}
	}
	_ = s.Store.Touch(ctx, repoID)
	return s.record(ctx, repo, nil)
}

func (s *Syncer) record(ctx context.Context, repo store.Repo, err error) error {
	ref := repo.Owner + "/" + repo.Name
	msg := ""
	if err != nil {
		msg = err.Error()
		if len(msg) > 500 {
			msg = msg[:500]
		}
		s.Log.Warn("mirror sync failed", "repo", ref, "err", msg)
	} else {
		s.Log.Info("mirror synced", "repo", ref)
	}
	dbctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	_ = s.Store.MirrorSynced(dbctx, repo.ID, msg)
	_ = s.Store.Audit(dbctx, 0, "mirror.sync", ref, firstLine(msg))
	return err
}

// git runs one git command with the token offered through a credential helper.
func (s *Syncer) git(ctx context.Context, token string, args ...string) (string, error) {
	full := []string{"-c", "credential.helper=", "-c", `credential.helper=!f(){ echo username=x-access-token; echo "password=$CODR_MIRROR_TOKEN"; }; f`}
	cmd := exec.CommandContext(ctx, s.Git, append(full, args...)...) // #nosec G204 -- fixed verbs; URL validated; token via env, never argv
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + os.Getenv("HOME"), "LANG=C.UTF-8", "GIT_CONFIG_NOSYSTEM=1",
		"GIT_TERMINAL_PROMPT=0", "CODR_MIRROR_TOKEN=" + token,
		"GIT_SSH_COMMAND=ssh -o BatchMode=yes"}
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	cmd.WaitDelay = 2 * time.Second
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(errb.String())
		msg = strings.ReplaceAll(msg, token, "***") // belt and braces: git never prints it, but a remote might echo
		if token == "" {
			msg = strings.TrimSpace(errb.String())
		}
		verb := args[0]
		if verb == "-C" && len(args) > 2 {
			verb = args[2]
		}
		return "", fmt.Errorf("git %s: %s", verb, firstLine(msg))
	}
	return out.String(), nil
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// Run is the scheduler: every tick, when the setting mirror.enabled is true, every due
// mirror is synced one after another. It returns when ctx ends.
func (s *Syncer) Run(ctx context.Context, tick time.Duration) {
	t := time.NewTicker(tick)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.tick(ctx)
		}
	}
}

func (s *Syncer) tick(ctx context.Context) {
	on, err := s.Store.SettingBool(ctx, "mirror.enabled")
	if err != nil || !on {
		return
	}
	due, err := s.Store.MirrorsDue(ctx)
	if err != nil {
		s.Log.Error("mirror: due query", "err", err)
		return
	}
	for _, m := range due {
		if ctx.Err() != nil {
			return
		}
		_ = s.Sync(ctx, m.RepoID)
	}
}
