package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"codr.my/codr/internal/auth"
	"codr.my/codr/internal/cloud"
	"codr.my/codr/internal/config"
	"codr.my/codr/internal/gitcmd"
	"codr.my/codr/internal/gitread"
	"codr.my/codr/internal/hooks"
	"codr.my/codr/internal/lfs"
	"codr.my/codr/internal/mirror"
	"codr.my/codr/internal/notify"
	"codr.my/codr/internal/repos"
	"codr.my/codr/internal/secret"
	"codr.my/codr/internal/sshd"
	"codr.my/codr/internal/store"
	"codr.my/codr/internal/web"
	"codr.my/codr/internal/webhook"
	"codr.my/codr/internal/xref"
)

// cmdInit writes config.toml, creates data/, applies migrations and generates the host key.
func (c *cli) cmdInit(args []string) error {
	fs := flag.NewFlagSet("init", flag.ContinueOnError)
	fs.SetOutput(c.errOut)
	dir := fs.String("dir", ".", "")
	if err := fs.Parse(args); err != nil {
		return c.usage("cli.bad_flag", args)
	}
	abs, err := filepath.Abs(*dir)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(abs, 0o700); err != nil {
		return err
	}
	path := filepath.Join(abs, "config.toml")
	if _, err := os.Stat(path); err == nil {
		return c.usage("init.exists", path)
	}
	cfg := config.Default()
	if cfg.Auth.SecretKey, err = secret.Generate(); err != nil {
		return err
	}
	if err := config.Write(path, cfg); err != nil {
		return err
	}
	cfg.Dir = abs
	st, err := store.Open(cfg.DBPath())
	if err != nil {
		return err
	}
	defer st.Close()
	if _, _, err := st.Migrate(context.Background()); err != nil {
		return err
	}
	if err := os.MkdirAll(cfg.ReposDir(), 0o700); err != nil {
		return err
	}
	if _, err := sshd.LoadOrCreateHostKey(cfg.SSHDir()); err != nil {
		return err
	}
	c.configPath = path
	fmt.Fprintln(c.out, c.t("init.done", abs, path, cfg.DataDir(), cfg.Server.SSHAddr))
	return nil
}

// cmdMigrate applies pending migrations, or with --check only reports them (exit 1 if any).
func (c *cli) cmdMigrate(args []string) error {
	fs := flag.NewFlagSet("migrate", flag.ContinueOnError)
	fs.SetOutput(c.errOut)
	check := fs.Bool("check", false, "")
	if err := fs.Parse(args); err != nil {
		return c.usage("cli.bad_flag", args)
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
	ctx := context.Background()
	if *check {
		v, pending, err := st.Pending(ctx)
		if err != nil {
			return err
		}
		if pending == 0 {
			fmt.Fprintln(c.out, c.t("migrate.check_ok", v))
			return nil
		}
		return fmt.Errorf("%s", c.t("migrate.check_pending", v, pending))
	}
	v, n, err := st.Migrate(ctx)
	if err != nil {
		return err
	}
	fmt.Fprintln(c.out, c.t("migrate.done", v, n))
	return nil
}

// cmdServe runs the three doors — SSH, HTTP and the hook socket — until SIGINT/SIGTERM.
func (c *cli) cmdServe(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	fs.SetOutput(c.errOut)
	if err := fs.Parse(args); err != nil {
		return c.usage("cli.bad_flag", args)
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
	ctx := context.Background()
	if _, pending, err := st.Pending(ctx); err != nil {
		return err
	} else if pending > 0 {
		v, _ := st.Version(ctx)
		return fmt.Errorf("%s", c.t("migrate.check_pending", v, pending))
	}
	hostKey, err := sshd.LoadOrCreateHostKey(cfg.SSHDir())
	if err != nil {
		return err
	}
	// Hook scripts point at THIS binary; rewritten on every start so upgrades take effect (D9).
	self, err := os.Executable()
	if err != nil {
		return err
	}
	if self, err = filepath.EvalSymlinks(self); err != nil {
		return err
	}
	if err := hooks.WriteScripts(cfg.HooksDir(), self); err != nil {
		return err
	}
	logger := slog.New(slog.NewTextHandler(c.errOut, nil))
	disk := repos.Disk{Root: cfg.ReposDir(), GitBinary: cfg.Git.Binary}
	git := gitcmd.Runner{Binary: cfg.Git.Binary, Timeout: time.Duration(cfg.Git.TimeoutSeconds) * time.Second,
		HooksDir: cfg.HooksDir(), HookSocket: cfg.HookSocket()}
	maxInput := cfg.Git.MaxInputMB << 20

	hookSrv := &hooks.Server{Store: st, Disk: disk, GitBinary: cfg.Git.Binary, Log: logger}
	hookLn, err := hooks.Listen(cfg.HookSocket())
	if err != nil {
		return err
	}
	sshSrv := &sshd.Server{Store: st, Disk: disk, Git: git, HostKey: hostKey, MaxAuthTries: cfg.Auth.SSHMaxAuthTries,
		ExecWait: time.Duration(cfg.Server.SSHExecWaitSeconds) * time.Second, MaxInputBytes: maxInput, Log: logger}
	sshLn, err := net.Listen("tcp", cfg.Server.SSHAddr)
	if err != nil {
		return err
	}
	trusted, err := cfg.TrustedNets()
	if err != nil {
		return err
	}
	edge, err := cfg.EdgeNets()
	if err != nil {
		return err
	}
	moved, err := cfg.Redirects()
	if err != nil {
		return err
	}
	webSrv := &web.Server{Store: st, Disk: disk, Git: git, MaxInputBytes: maxInput, DefaultLang: cfg.Site.DefaultLanguage,
		Limiter: &web.Limiter{Max: cfg.Auth.HTTPFailMax, Window: time.Duration(cfg.Auth.HTTPFailWindowSeconds) * time.Second}, Log: logger,
		TrustedProxies: trusted, ClientIPHeader: cfg.Server.ClientIPHeader, EdgeRanges: edge, RedirectHosts: moved, LandingHosts: cfg.Site.LandingHosts,
		UI: web.UI{SessionTTL: time.Duration(cfg.Auth.SessionHours) * time.Hour,
			Argon:          auth.Params{Time: cfg.Auth.Argon2Time, Memory: cfg.Auth.Argon2MemoryKiB, Threads: cfg.Auth.Argon2Threads},
			PasswordMinLen: cfg.Auth.PasswordMinLen,
			SecureCookies:  strings.HasPrefix(cfg.Server.HTTPHost, "https://"), SSHHost: cfg.Server.SSHHost, HTTPHost: cfg.Server.HTTPHost,
			GitTimeout: time.Duration(cfg.Git.TimeoutSeconds) * time.Second}}
	if err := webSrv.Init(); err != nil {
		return err
	}
	httpLn, err := net.Listen("tcp", cfg.Server.HTTPAddr)
	if err != nil {
		return err
	}
	httpSrv := &http.Server{Handler: webSrv.Handler(),
		ReadHeaderTimeout: time.Duration(cfg.Server.HTTPReadHeaderSeconds) * time.Second,
		IdleTimeout:       time.Duration(cfg.Server.HTTPIdleSeconds) * time.Second,
		ErrorLog:          slog.NewLogLogger(logger.Handler(), slog.LevelWarn)}
	fmt.Fprintln(c.out, c.t("serve.ssh_listening", sshLn.Addr().String(), cfg.Server.SSHHost))
	fmt.Fprintln(c.out, c.t("serve.http_listening", httpLn.Addr().String(), cfg.Server.HTTPHost))

	key, err := secret.Parse(cfg.Auth.SecretKey)
	if err != nil {
		return err
	}
	syncer := &mirror.Syncer{Store: st, Disk: disk, Git: cfg.Git.Binary, Timeout: time.Duration(cfg.Git.TimeoutSeconds) * time.Second, Key: key, Log: logger}
	webSrv.Mirror = syncer
	job := &cloud.Job{Store: st, Disk: disk, GitBinary: cfg.Git.Binary, ConfigPath: c.configPath,
		WorkDir: filepath.Join(cfg.DataDir(), ".cloud-tmp"), Secret: key, LFSRoot: cfg.LFSDir()}
	_ = st.FailStaleCloudRuns(context.Background(), "serve stopped during the run")
	webSrv.Cloud = job
	hookTimeout := webSrv.UI.GitTimeout
	dispatcher := &webhook.Dispatcher{Store: st, Key: key, Log: logger, Builder: webhook.Builder{Store: st, HTTPHost: cfg.Server.HTTPHost,
		SSHHost: cfg.Server.SSHHost, Git: func(id int64) gitread.Repo {
			return gitread.Repo{Path: disk.Path(id), Git: cfg.Git.Binary, Timeout: hookTimeout}
		}}}
	webSrv.Webhooks = dispatcher
	notifier := &notify.Notifier{Store: st, Key: key, Log: logger, Mailer: &notify.Mailer{HTTPHost: cfg.Server.HTTPHost}}
	webSrv.Notifier = notifier
	closer := &xref.Closer{Store: st, Log: logger, OnEvent: func() { dispatcher.Kick(); notifier.Kick() },
		Git: func(id int64) gitread.Repo {
			return gitread.Repo{Path: disk.Path(id), Git: cfg.Git.Binary, Timeout: hookTimeout}
		}}
	webSrv.Closer = closer
	webSrv.LFS = &lfs.Store{Root: cfg.LFSDir()}
	sshSrv.LFSAuth = webSrv.LFSAuthenticate
	hookSrv.OnEvent = func() { dispatcher.Kick(); notifier.Kick(); closer.Kick() }

	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()
	go syncer.Run(ctx, time.Duration(cfg.Git.MirrorTickSeconds)*time.Second)
	go job.Schedule(ctx, 10*time.Minute, logger)
	go dispatcher.Run(ctx, webhook.Tick)
	go notifier.Run(ctx, notify.Tick)
	go closer.Run(ctx, xref.Tick)
	errs := make(chan error, 3)
	go func() { errs <- hookSrv.Serve(ctx, hookLn) }()
	go func() { errs <- sshSrv.Serve(ctx, sshLn) }()
	go func() {
		err := httpSrv.Serve(httpLn)
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
		errs <- err
	}()
	var first error
	select {
	case <-ctx.Done():
	case first = <-errs:
		stop()
	}
	fmt.Fprintln(c.out, c.t("serve.stopping"))
	shutCtx, cancel := context.WithTimeout(context.Background(), time.Duration(cfg.Server.SSHExecWaitSeconds)*time.Second)
	defer cancel()
	_ = httpSrv.Shutdown(shutCtx)
	for i := 0; i < 2; i++ {
		if err := <-errs; err != nil && first == nil {
			first = err
		}
	}
	_ = os.Remove(cfg.HookSocket())
	return first
}
