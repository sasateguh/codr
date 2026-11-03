// Command amcd is the single binary of the AMCD git forge.
//
// Subcommands: init, migrate, serve, doctor, admin (user|key|repo|collab|token|protect|setting|mirror), hook, version, help.
// Every line printed for a human goes through i18n (locale/*.json); code holds keys only.
package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"codr.my/codr/internal/config"
	"codr.my/codr/internal/i18n"
	"codr.my/codr/internal/version"
)

// cli carries what every subcommand needs.
type cli struct {
	out, errOut io.Writer
	in          io.Reader
	lang        string
	configPath  string
}

// envOr reads a CODR_* variable, falling back to the AMCD_* name used before the rename (D58).
func envOr(name, legacy string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return os.Getenv(legacy)
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

// run is main without the process exit, so tests can drive it.
func run(args []string, in io.Reader, out, errOut io.Writer) int {
	c := &cli{out: out, errOut: errOut, in: in, configPath: envOr("CODR_CONFIG", "AMCD_CONFIG")}
	args = c.takeConfigFlag(args)
	if c.configPath == "" {
		c.configPath = "config.toml"
	}
	c.lang = i18n.Normalize(envOr("CODR_LANG", "AMCD_LANG"), "")
	if c.lang == "" {
		c.lang = config.Default().Site.DefaultLanguage
		if cfg, err := config.Load(c.configPath); err == nil {
			c.lang = i18n.Normalize(cfg.Site.DefaultLanguage, c.lang)
		}
	}
	if len(args) == 0 {
		fmt.Fprint(errOut, c.t("cli.usage"))
		return 2
	}
	var err error
	switch args[0] {
	case "version":
		fmt.Fprintln(out, "codr", version.String())
		return 0
	case "help", "-h", "--help":
		fmt.Fprint(out, c.t("cli.usage"))
		return 0
	case "init":
		err = c.cmdInit(args[1:])
	case "migrate":
		err = c.cmdMigrate(args[1:])
	case "serve":
		err = c.cmdServe(args[1:])
	case "admin":
		err = c.cmdAdmin(args[1:])
	case "hook":
		return c.cmdHook(args[1:])
	case "doctor":
		err = c.cmdDoctor(args[1:])
	case "backup":
		err = c.cmdBackup(args[1:])
	case "restore":
		err = c.cmdRestore(args[1:])
	case "cloud":
		err = c.cmdCloud(args[1:])
	default:
		fmt.Fprintln(errOut, c.t("cli.unknown_command", args[0]))
		fmt.Fprint(errOut, c.t("cli.usage"))
		return 2
	}
	if err != nil {
		var ue usageErr
		if errors.As(err, &ue) {
			fmt.Fprintln(errOut, ue.msg)
			return 2
		}
		fmt.Fprintln(errOut, c.t("cli.error", err))
		return 1
	}
	return 0
}

// usageErr is a user mistake (wrong arguments); exit 2, message already localised.
type usageErr struct{ msg string }

func (u usageErr) Error() string { return u.msg }

func (c *cli) t(key string, args ...any) string { return i18n.T(c.lang, key, args...) }

func (c *cli) usage(key string, args ...any) error { return usageErr{c.t(key, args...)} }

// takeConfigFlag removes --config X / --config=X from args wherever it appears.
func (c *cli) takeConfigFlag(args []string) []string {
	var rest []string
	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "--config" && i+1 < len(args):
			c.configPath = args[i+1]
			i++
		case strings.HasPrefix(args[i], "--config="):
			c.configPath = strings.TrimPrefix(args[i], "--config=")
		default:
			rest = append(rest, args[i])
		}
	}
	return rest
}

// loadConfig loads config.toml or returns a localised usage error.
func (c *cli) loadConfig() (config.Config, error) {
	cfg, err := config.Load(c.configPath)
	if errors.Is(err, os.ErrNotExist) {
		return config.Config{}, c.usage("cli.config_missing", c.configPath)
	}
	return cfg, err
}
