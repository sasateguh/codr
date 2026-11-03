// Package sshd is the SSH door of AMCD: public-key auth only, no shell, no pty, no
// forwarding, and exactly one of two git commands per session (PRD §9). Permission is
// decided by store.AccessFor before git is started.
package sshd

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"

	"codr.my/codr/internal/gitcmd"
	"codr.my/codr/internal/i18n"
	"codr.my/codr/internal/repos"
	"codr.my/codr/internal/store"
)

// Server serves git over SSH.
type Server struct {
	Store        *store.Store
	Disk         repos.Disk
	Git          gitcmd.Runner
	HostKey      ssh.Signer
	MaxAuthTries int
	// ExecWait bounds how long a session may sit without sending "exec".
	ExecWait time.Duration
	// MaxInputBytes is the coarse receive.maxInputSize bound from config.toml (0 = none).
	MaxInputBytes int64
	Log           *slog.Logger
	// LFSAuth answers git-lfs-authenticate (D82): JSON with the LFS URL and a short token; nil = refused.
	LFSAuth func(ctx context.Context, uid int64, owner, repo, op string) ([]byte, error)

	wg sync.WaitGroup
}

const (
	extUser = "codr-user-id"
	extKey  = "codr-key-id"
	extLang = "codr-lang"
)

func (s *Server) config() *ssh.ServerConfig {
	cfg := &ssh.ServerConfig{
		MaxAuthTries:      s.MaxAuthTries,
		ServerVersion:     "SSH-2.0-Codr",
		PublicKeyCallback: s.publicKey,
		// PasswordCallback and KeyboardInteractiveCallback stay nil: keys only (D8).
	}
	cfg.AddHostKey(s.HostKey)
	return cfg
}

// publicKey maps a presented key to a user. Unknown keys fail; the error text is never
// sent to the client by x/crypto/ssh, only logged here.
func (s *Server) publicKey(conn ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	fp := ssh.FingerprintSHA256(key)
	k, err := s.Store.SSHKeyByFingerprint(ctx, fp)
	if err != nil {
		return nil, fmt.Errorf("sshd: key %s unknown", fp)
	}
	stored, _, _, _, err := ssh.ParseAuthorizedKey([]byte(k.PublicKey))
	if err != nil || !bytes.Equal(stored.Marshal(), key.Marshal()) {
		return nil, errors.New("sshd: key mismatch")
	}
	u, err := s.Store.UserByID(ctx, k.UserID)
	if err != nil || u.Disabled || u.IsOrg {
		return nil, errors.New("sshd: user unavailable")
	}
	_ = s.Store.TouchSSHKey(ctx, k.ID)
	s.Log.Info("ssh auth", "user", u.Name, "key", k.ID, "remote", conn.RemoteAddr().String())
	return &ssh.Permissions{Extensions: map[string]string{
		extUser: strconv.FormatInt(u.ID, 10),
		extKey:  strconv.FormatInt(k.ID, 10),
		extLang: u.Lang,
	}}, nil
}

// Serve accepts connections until ctx is cancelled or the listener fails, then waits
// for in-flight sessions.
func (s *Server) Serve(ctx context.Context, ln net.Listener) error {
	cfg := s.config()
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
			s.handleConn(ctx, c, cfg)
		}()
	}
}

func (s *Server) handleConn(ctx context.Context, c net.Conn, cfg *ssh.ServerConfig) {
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(s.ExecWait))
	sconn, chans, reqs, err := ssh.NewServerConn(c, cfg)
	if err != nil {
		s.Log.Debug("ssh handshake failed", "remote", c.RemoteAddr().String(), "err", err)
		return
	}
	defer sconn.Close()
	_ = c.SetDeadline(time.Time{})
	go ssh.DiscardRequests(reqs) // replies false to every global request (no tcpip-forward)
	uid, _ := strconv.ParseInt(sconn.Permissions.Extensions[extUser], 10, 64)
	kid, _ := strconv.ParseInt(sconn.Permissions.Extensions[extKey], 10, 64)
	lang := sconn.Permissions.Extensions[extLang]
	for nc := range chans {
		if nc.ChannelType() != "session" {
			_ = nc.Reject(ssh.UnknownChannelType, "only session channels")
			continue
		}
		ch, chReqs, err := nc.Accept()
		if err != nil {
			return
		}
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			s.session(ctx, ch, chReqs, uid, kid, lang)
		}()
	}
}

// session handles one channel: optional GIT_PROTOCOL env, then exactly one exec.
func (s *Server) session(ctx context.Context, ch ssh.Channel, reqs <-chan *ssh.Request, uid, kid int64, lang string) {
	defer ch.Close()
	var gitProtocol string
	timer := time.NewTimer(s.ExecWait)
	defer timer.Stop()
	for {
		select {
		case <-timer.C:
			return
		case <-ctx.Done():
			return
		case req, ok := <-reqs:
			if !ok {
				return
			}
			switch req.Type {
			case "env":
				var kv struct{ Name, Value string }
				if ssh.Unmarshal(req.Payload, &kv) == nil && kv.Name == "GIT_PROTOCOL" && ValidGitProtocol(kv.Value) {
					gitProtocol = kv.Value
					reply(req, true)
				} else {
					reply(req, false)
				}
			case "exec":
				var p struct{ Command string }
				if err := ssh.Unmarshal(req.Payload, &p); err != nil {
					reply(req, false)
					return
				}
				reply(req, true)
				code := s.exec(ctx, ch, uid, kid, lang, p.Command, gitProtocol)
				exit(ch, code)
				return
			case "shell", "pty-req", "subsystem", "x11-req", "auth-agent-req@openssh.com":
				reply(req, false)
				if req.Type == "shell" {
					fmt.Fprintln(ch.Stderr(), i18n.T(lang, "ssh.no_shell"))
					exit(ch, 1)
					return
				}
			default:
				reply(req, false)
			}
		}
	}
}

// exec authorises and runs one git command, returning the exit status for the client.
func (s *Server) exec(ctx context.Context, ch ssh.Channel, uid, kid int64, lang, line, gitProtocol string) int {
	cmd, err := ParseCommand(line)
	if err != nil {
		if la, lerr := ParseLFSAuth(line); lerr == nil && s.LFSAuth != nil {
			out, err := s.LFSAuth(ctx, uid, la.Owner, la.Repo, la.Operation)
			if err != nil {
				fmt.Fprintln(ch.Stderr(), i18n.T(lang, "ssh.not_found", la.Owner+"/"+la.Repo))
				return 1
			}
			_, _ = ch.Write(append(out, '\n'))
			s.Log.Info("git-lfs-authenticate", "repo", la.Owner+"/"+la.Repo, "user", uid, "op", la.Operation)
			return 0
		}
		s.Log.Warn("ssh command rejected", "user", uid, "command", line)
		fmt.Fprintln(ch.Stderr(), i18n.T(lang, "ssh.bad_command"))
		return 1
	}
	ref := cmd.Owner + "/" + cmd.Repo
	dbctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	repo, err := s.Store.RepoByPath(dbctx, cmd.Owner, cmd.Repo)
	isWiki := false
	if errors.Is(err, store.ErrNotFound) { // owner/name.wiki = the wiki of owner/name, read-only (D83)
		if base, ok := strings.CutSuffix(cmd.Repo, ".wiki"); ok {
			if br, berr := s.Store.RepoByPath(dbctx, cmd.Owner, base); berr == nil {
				repo, err, isWiki = br, nil, true
			}
		}
	}
	var access store.Access
	if err == nil {
		access, err = s.Store.AccessFor(dbctx, uid, repo)
	}
	cancel()
	switch {
	case errors.Is(err, store.ErrNotFound), err == nil && access < store.AccessRead:
		fmt.Fprintln(ch.Stderr(), i18n.T(lang, "ssh.not_found", ref))
		return 1
	case err != nil:
		s.Log.Error("ssh access lookup", "err", err)
		fmt.Fprintln(ch.Stderr(), i18n.T(lang, "ssh.internal"))
		return 1
	case cmd.Service == gitcmd.ReceivePack && access < store.AccessWrite:
		fmt.Fprintln(ch.Stderr(), i18n.T(lang, "ssh.write_denied", ref))
		return 1
	case cmd.Service == gitcmd.ReceivePack && repo.Archived:
		fmt.Fprintln(ch.Stderr(), i18n.T(lang, "ssh.archived", ref))
		return 1
	case cmd.Service == gitcmd.ReceivePack && repo.Mirror:
		fmt.Fprintln(ch.Stderr(), i18n.T(lang, "ssh.mirror", ref))
		return 1
	}
	dir := s.Disk.Path(repo.ID)
	if isWiki {
		if cmd.Service == gitcmd.ReceivePack {
			fmt.Fprintln(ch.Stderr(), i18n.T(lang, "ssh.wiki_readonly", ref))
			return 1
		}
		dir = s.Disk.WikiPath(repo.ID)
		if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
			fmt.Fprintln(ch.Stderr(), i18n.T(lang, "ssh.not_found", ref))
			return 1
		}
	} else if !s.Disk.Exists(repo.ID) {
		s.Log.Error("repo missing on disk", "repo", ref, "path", s.Disk.Path(repo.ID))
		fmt.Fprintln(ch.Stderr(), i18n.T(lang, "ssh.internal"))
		return 1
	}
	push := gitcmd.Push{RepoID: repo.ID, UserID: uid, Lang: lang, Door: "ssh", MaxInputBytes: s.MaxInputBytes}
	if cmd.Service == gitcmd.ReceivePack {
		dbctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		push.Fsck, err = s.Store.SettingBool(dbctx, "repo.receive_fsck")
		cancel()
		if err != nil {
			s.Log.Error("ssh setting lookup", "err", err)
			fmt.Fprintln(ch.Stderr(), i18n.T(lang, "ssh.internal"))
			return 1
		}
	}
	// Audit + updated_at are written by the post-receive hook (one place for every door, D9).
	code, err := s.Git.Run(ctx, cmd.Service, dir, gitProtocol, push, ch, ch, ch.Stderr())
	if err != nil {
		s.Log.Error("git run", "repo", ref, "service", cmd.Service, "err", err)
		fmt.Fprintln(ch.Stderr(), i18n.T(lang, "ssh.internal"))
		return 1
	}
	s.Log.Info("git", "service", cmd.Service, "repo", ref, "user", uid, "key", kid, "exit", code)
	return code
}

func reply(req *ssh.Request, ok bool) {
	if req.WantReply {
		_ = req.Reply(ok, nil)
	}
}

func exit(ch ssh.Channel, code int) {
	_, _ = ch.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{uint32(code)})) // #nosec G115 -- exit codes are 0..255
}
