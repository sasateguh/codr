package hooks

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"codr.my/codr/internal/i18n"
	"codr.my/codr/internal/repos"
	"codr.my/codr/internal/store"
)

// Server answers hook requests. It is the one place that knows the push rules.
type Server struct {
	Store     *store.Store
	Disk      repos.Disk
	GitBinary string
	Log       *slog.Logger
	// OnEvent is called after a push wrote its outbox events (serve wakes the webhook dispatcher); nil = no call.
	OnEvent func()

	wg sync.WaitGroup
}

// ProbeTimeout bounds one dial of the hook socket by Listen, Probe, /healthz and doctor. A live
// server accepts in microseconds; the bound only matters when the path is wedged.
const ProbeTimeout = time.Second

// ErrSocketBusy means another server still answers on the socket path: a second `codr serve`
// on the same data dir must stop instead of stealing the path from the live one.
var ErrSocketBusy = errors.New("hooks: socket is in use by a running server")

// Probe dials the socket once and hangs up: nil when a server is listening there.
func Probe(path string) error {
	c, err := net.DialTimeout("unix", path, ProbeTimeout)
	if err != nil {
		return err
	}
	return c.Close()
}

// Listen creates the Unix socket (0600). A file left by a dead server is replaced; a path where a
// server still answers is left alone and ErrSocketBusy is returned. Removing it blindly let a second
// serve orphan the live server's socket and every push failed "hook failed" (5 Nov 2026).
func Listen(path string) (net.Listener, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	switch err := Probe(path); {
	case err == nil, errors.Is(err, syscall.EAGAIN):
		return nil, ErrSocketBusy // EAGAIN: alive, its accept queue is just full
	case errors.Is(err, syscall.ECONNREFUSED): // a socket file nobody listens on
		if err := os.Remove(path); err != nil {
			return nil, err
		}
	case errors.Is(err, fs.ErrNotExist):
	default:
		return nil, err
	}
	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		ln.Close()
		return nil, err
	}
	return ln, nil
}

// Serve handles one request per connection until ctx is cancelled.
func (s *Server) Serve(ctx context.Context, ln net.Listener) error {
	go func() {
		<-ctx.Done()
		ln.Close()
	}()
	for {
		c, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				s.wg.Wait()
				return nil
			}
			return err
		}
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			defer c.Close()
			_ = c.SetDeadline(time.Now().Add(60 * time.Second))
			var req Request
			if err := json.NewDecoder(io.LimitReader(c, 1<<20)).Decode(&req); err != nil {
				if !errors.Is(err, io.EOF) { // EOF = a Probe (healthz, doctor) hanging up, not a broken hook
					s.Log.Warn("hook: bad request", "err", err)
				}
				return
			}
			resp := s.Handle(ctx, req)
			_ = json.NewEncoder(c).Encode(resp)
		}()
	}
}

// Handle decides one request.
func (s *Server) Handle(ctx context.Context, req Request) Response {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	lang := req.Lang
	repo, err := s.Store.RepoByID(ctx, req.RepoID)
	if err != nil {
		s.Log.Error("hook: repo lookup", "repo", req.RepoID, "err", err)
		return Response{Message: i18n.T(lang, "hook.internal")}
	}
	switch req.Hook {
	case "pre-receive":
		return s.preReceive(ctx, req, repo)
	case "post-receive":
		return s.postReceive(ctx, req, repo)
	}
	return Response{Message: i18n.T(lang, "hook.internal")}
}

func (s *Server) preReceive(ctx context.Context, req Request, repo store.Repo) Response {
	lang := req.Lang
	ref := repo.Owner + "/" + repo.Name
	// 1. Size: everything git received sits in the quarantine directory until we say yes.
	if q := req.Env["GIT_QUARANTINE_PATH"]; q != "" {
		maxMB, err := s.Store.SettingInt(ctx, "repo.push_max_mb")
		if err != nil {
			s.Log.Error("hook: setting", "err", err)
			return Response{Message: i18n.T(lang, "hook.internal")}
		}
		size, err := dirSize(q)
		if err != nil {
			s.Log.Error("hook: quarantine size", "path", q, "err", err)
			return Response{Message: i18n.T(lang, "hook.internal")}
		}
		if maxMB > 0 && size > maxMB<<20 {
			got := (size + (1 << 20) - 1) >> 20
			s.Log.Warn("hook: push too big", "repo", ref, "mb", got, "max_mb", maxMB)
			return Response{Message: i18n.T(lang, "hook.too_big", ref, got, maxMB)}
		}
	}
	// 2. Protected branches.
	for _, r := range req.Refs {
		branch, ok := strings.CutPrefix(r.Name, "refs/heads/")
		if !ok {
			continue
		}
		rule, err := s.Store.RuleFor(ctx, repo, branch)
		if err != nil {
			s.Log.Error("hook: rule", "err", err)
			return Response{Message: i18n.T(lang, "hook.internal")}
		}
		if !rule.Protected {
			continue
		}
		if r.Deleted() && !rule.AllowDelete {
			return Response{Message: i18n.T(lang, "hook.protected_delete", branch, ref)}
		}
		if !r.Deleted() && rule.RequireChecks { // D89: the branch only moves to a commit CI already passed
			sts, err := s.Store.Statuses(ctx, repo.ID, r.New)
			if err != nil {
				s.Log.Error("hook: statuses", "err", err)
				return Response{Message: i18n.T(lang, "hook.internal")}
			}
			if state := store.Combined(sts); state != "success" {
				if state == "" {
					state = "none"
				}
				return Response{Message: i18n.T(lang, "hook.protected_checks", branch, ref, short(r.New), i18n.T(lang, "hook.checks."+state))}
			}
		}
		if r.Deleted() || r.Created() || rule.AllowForce {
			continue
		}
		ff, err := s.isAncestor(ctx, repo, req.Env, r.Old, r.New)
		if err != nil {
			s.Log.Error("hook: merge-base", "err", err)
			return Response{Message: i18n.T(lang, "hook.internal")}
		}
		if !ff {
			return Response{Message: i18n.T(lang, "hook.protected_force", branch, ref)}
		}
	}
	return Response{OK: true}
}

// isAncestor runs `git merge-base --is-ancestor old new` with the quarantine environment
// so the not-yet-accepted objects are visible.
func (s *Server) isAncestor(ctx context.Context, repo store.Repo, env map[string]string, oldID, newID string) (bool, error) {
	if !isHex(oldID) || !isHex(newID) {
		return false, errors.New("hooks: bad object id")
	}
	cmd := exec.CommandContext(ctx, s.GitBinary, "-C", s.Disk.Path(repo.ID), "merge-base", "--is-ancestor", oldID, newID) // #nosec G204 -- ids validated hex, path from id
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + os.Getenv("HOME"), "LANG=C.UTF-8", "GIT_CONFIG_NOSYSTEM=1"}
	for _, k := range envKeys {
		if v := env[k]; v != "" {
			cmd.Env = append(cmd.Env, k+"="+v)
		}
	}
	err := cmd.Run()
	var exitErr *exec.ExitError
	switch {
	case err == nil:
		return true, nil
	case errors.As(err, &exitErr) && exitErr.ExitCode() == 1:
		return false, nil
	}
	return false, err
}

func (s *Server) postReceive(ctx context.Context, req Request, repo store.Repo) Response {
	ref := repo.Owner + "/" + repo.Name
	_ = s.Store.Touch(ctx, repo.ID)
	var parts []string
	for _, r := range req.Refs {
		parts = append(parts, fmt.Sprintf("%s %s..%s", r.Name, short(r.Old), short(r.New)))
	}
	detail := req.Door + " " + strings.Join(parts, "; ")
	if err := s.Store.Audit(ctx, req.UserID, "git.push", ref, detail); err != nil {
		s.Log.Error("hook: audit", "err", err)
	}
	// one outbox event per ref, like GitHub (D78); the webhook dispatcher in serve reads them
	for _, r := range req.Refs {
		if err := s.Store.AddEvent(ctx, repo.ID, req.UserID, "push", map[string]any{"ref": r.Name, "before": r.Old, "after": r.New, "via": req.Door}); err != nil {
			s.Log.Error("hook: event", "err", err)
		}
	}
	if s.OnEvent != nil {
		s.OnEvent()
	}
	s.Log.Info("push", "repo", ref, "user", req.UserID, "door", req.Door, "refs", len(req.Refs))
	return Response{OK: true}
}

func dirSize(root string) (int64, error) {
	var n int64
	err := filepath.WalkDir(root, func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type().IsRegular() {
			info, err := d.Info()
			if err != nil {
				return err
			}
			n += info.Size()
		}
		return nil
	})
	return n, err
}

func isHex(s string) bool {
	if len(s) != 40 && len(s) != 64 {
		return false
	}
	for _, c := range s {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

func short(id string) string {
	if len(id) > 10 {
		return id[:10]
	}
	return id
}
