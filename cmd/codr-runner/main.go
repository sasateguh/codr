// Command codr-runner is the CI runner of Codr (D89): a separate binary that receives push webhooks, runs the jobs of
// .codr/ci.toml in locked-down containers and reports them as commit statuses. Every line printed for a human goes
// through i18n (locale/*.json).
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"codr.my/codr/internal/i18n"
	"codr.my/codr/internal/runner"
	"codr.my/codr/internal/version"
)

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }

func run(args []string, out, errOut io.Writer) int {
	lang := i18n.Normalize(os.Getenv("CODR_LANG"), "ms-MY")
	if len(args) == 0 {
		fmt.Fprintln(errOut, i18n.T(lang, "runner.cli.usage"))
		return 2
	}
	fs := flag.NewFlagSet(args[0], flag.ContinueOnError)
	fs.SetOutput(errOut)
	path := fs.String("config", envOr("CODR_RUNNER_CONFIG", "runner.toml"), "runner.toml")
	switch args[0] {
	case "version":
		fmt.Fprintln(out, "codr-runner", version.String())
		return 0
	case "serve", "check":
		if err := fs.Parse(args[1:]); err != nil {
			return 2
		}
	default:
		fmt.Fprintln(errOut, i18n.T(lang, "runner.cli.usage"))
		return 2
	}
	cfg, err := runner.LoadConfig(*path)
	if err != nil {
		fmt.Fprintln(errOut, err)
		return 1
	}
	lang = i18n.Normalize(cfg.Lang, lang)
	if args[0] == "check" {
		return check(cfg, *path, lang, out)
	}
	if err := serve(cfg, lang, out); err != nil {
		fmt.Fprintln(errOut, err)
		return 1
	}
	return 0
}

func envOr(name, def string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return def
}

func serve(cfg runner.Config, lang string, out io.Writer) error {
	for _, d := range []string{cfg.WorkDir, cfg.LogDir} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return err
		}
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	rn := &runner.Runner{Cfg: cfg, Log: log}
	rn.Start(ctx)
	ln, err := net.Listen("tcp", cfg.Listen)
	if err != nil {
		return err
	}
	srv := &http.Server{Handler: rn.Handler(), ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 60 * time.Second}
	fmt.Fprintln(out, i18n.T(lang, "runner.cli.listening", ln.Addr().String(), cfg.ForgeURL, cfg.Concurrency))
	errs := make(chan error, 1)
	go func() { errs <- srv.Serve(ln) }()
	select {
	case <-ctx.Done():
	case err = <-errs:
	}
	shut, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = srv.Shutdown(shut)
	stop()
	rn.Wait()
	if errors.Is(err, http.ErrServerClosed) {
		err = nil
	}
	return err
}

// check prints one line per check, like `codr doctor`, and exits 1 when any failed.
func check(cfg runner.Config, path, lang string, out io.Writer) int {
	failed := false
	line := func(ok bool, msg string) {
		mark := i18n.T(lang, "runner.cli.check_ok")
		if !ok {
			mark, failed = i18n.T(lang, "runner.cli.check_fail"), true
		}
		fmt.Fprintf(out, "%-6s  %s\n", mark, msg)
	}
	line(true, i18n.T(lang, "runner.cli.check_config", path))
	ok := true
	for _, d := range []string{cfg.WorkDir, cfg.LogDir} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			ok = false
		} else if f, err := os.CreateTemp(d, ".check-*"); err != nil {
			ok = false
		} else {
			f.Close()
			_ = os.Remove(f.Name())
		}
	}
	line(ok, i18n.T(lang, "runner.cli.check_dirs"))
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	v, err := exec.CommandContext(ctx, cfg.Docker, "version", "--format", "{{.Server.Version}}").Output() // #nosec G204 -- the operator's docker binary
	line(err == nil, i18n.T(lang, "runner.cli.check_docker", strings.TrimSpace(string(v))))
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(cfg.ForgeURL, "/")+"/api/v1/user", nil)
	req.Header.Set("Authorization", "Bearer "+cfg.Token)
	var me struct {
		Login string `json:"login"`
	}
	resp, err := http.DefaultClient.Do(req)
	if err == nil {
		err = fmt.Errorf("HTTP %d", resp.StatusCode)
		if resp.StatusCode == http.StatusOK {
			err = json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&me)
		}
		resp.Body.Close()
	}
	line(err == nil, i18n.T(lang, "runner.cli.check_forge", me.Login))
	if failed {
		return 1
	}
	return 0
}
