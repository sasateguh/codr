package runner

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"

	"github.com/pelletier/go-toml/v2"
)

// Config is the runner's own file (runner.toml, 0600: it holds the token and the webhook secret).
type Config struct {
	Listen        string `toml:"listen"`         // HTTP address for /hook, /ci/log/… and /healthz
	ForgeURL      string `toml:"forge_url"`      // where the runner reaches Codr (clone + API), e.g. http://codr:3000
	PublicURL     string `toml:"public_url"`     // where people reach the runner's log pages, e.g. https://git.codr.my
	Token         string `toml:"token"`          // a Codr access token (repo:write) of the account the runner acts as
	WebhookSecret string `toml:"webhook_secret"` // the secret of the Codr webhook that calls /hook
	// Secrets: per repository (owner/name) secrets; Codr makes one secret per webhook, so each repository's webhook has
	// its own. A repository listed here is checked against its own secret only; others against webhook_secret.
	Secrets map[string]string `toml:"secrets"`
	Lang    string            `toml:"lang"`     // language of status descriptions and log headers: ms-MY | en-US
	Docker  string            `toml:"docker"`   // docker binary
	Git     string            `toml:"git"`      // git binary
	WorkDir string            `toml:"work_dir"` // checkouts (one folder per job, removed afterwards)
	LogDir  string            `toml:"log_dir"`  // job logs
	// Limits. Every job container runs with no network unless allow_network, no capabilities, no new privileges, a
	// read-only root file system and these bounds.
	Concurrency    int    `toml:"concurrency"`
	TimeoutMinutes int    `toml:"timeout_minutes"`
	Memory         string `toml:"memory"`   // docker --memory, e.g. "1g"
	CPUs           string `toml:"cpus"`     // docker --cpus, e.g. "1.5"
	Pids           int    `toml:"pids"`     // docker --pids-limit
	TmpSize        string `toml:"tmp_size"` // size of the writable /tmp in the container
	AllowNetwork   bool   `toml:"allow_network"`
	MaxJobs        int    `toml:"max_jobs"`   // jobs per ci.toml
	MaxSteps       int    `toml:"max_steps"`  // steps per job
	LogMaxKB       int    `toml:"log_max_kb"` // a job log is cut here
	LogKeepDays    int    `toml:"log_keep_days"`
	QueueMax       int    `toml:"queue_max"` // pushes waiting; more are answered 503
	// Repos limits which repositories this runner serves (owner/name, path.Match globs); empty = every repository whose
	// webhook carries the right secret.
	Repos []string `toml:"repos"`
	// UID:GID the job runs as inside the container (the checkout is owned by the runner's own user).
	User string `toml:"user"`
}

// Defaults fills what the file left out.
func (c *Config) Defaults() {
	set := func(p *string, v string) {
		if *p == "" {
			*p = v
		}
	}
	setI := func(p *int, v int) {
		if *p <= 0 {
			*p = v
		}
	}
	set(&c.Listen, "127.0.0.1:3200")
	set(&c.Lang, "ms-MY")
	set(&c.Docker, "docker")
	set(&c.Git, "git")
	set(&c.Memory, "1g")
	set(&c.CPUs, "1")
	set(&c.TmpSize, "256m")
	setI(&c.Concurrency, 1)
	setI(&c.TimeoutMinutes, 15)
	setI(&c.Pids, 256)
	setI(&c.MaxJobs, 8)
	setI(&c.MaxSteps, 20)
	setI(&c.LogMaxKB, 1024)
	setI(&c.LogKeepDays, 30)
	setI(&c.QueueMax, 50)
}

// LoadConfig reads and checks runner.toml.
func LoadConfig(path string) (Config, error) {
	var c Config
	b, err := os.ReadFile(path) // #nosec G304 -- the operator names the file
	if err != nil {
		return c, err
	}
	dec := toml.NewDecoder(strings.NewReader(string(b)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&c); err != nil {
		var strict *toml.StrictMissingError
		if errors.As(err, &strict) {
			return c, fmt.Errorf("%s: %s", path, strict.String())
		}
		return c, fmt.Errorf("%s: %w", path, err)
	}
	c.Defaults()
	return c, c.Check()
}

// Check reports the first thing that would stop the runner from working.
func (c Config) Check() error {
	for _, u := range []struct{ name, v string }{{"forge_url", c.ForgeURL}, {"public_url", c.PublicURL}} {
		p, err := url.Parse(u.v)
		if err != nil || (p.Scheme != "http" && p.Scheme != "https") || p.Host == "" {
			return fmt.Errorf("%s must be an http(s) URL", u.name)
		}
	}
	switch {
	case c.Token == "":
		return errors.New("token is required (a Codr access token with repo:write)")
	case len(c.WebhookSecret) < 16 && len(c.Secrets) == 0:
		return errors.New("webhook_secret or [secrets] is required (at least 16 characters each)")
	case c.WebhookSecret != "" && len(c.WebhookSecret) < 16:
		return errors.New("webhook_secret must be at least 16 characters")
	case c.WorkDir == "" || c.LogDir == "":
		return errors.New("work_dir and log_dir are required")
	}
	for repo, sec := range c.Secrets {
		if len(sec) < 16 {
			return fmt.Errorf("secrets.%q must be at least 16 characters", repo)
		}
	}
	return nil
}

// SecretFor is the webhook secret that must have signed a delivery for repo ("" = none: refuse).
func (c Config) SecretFor(repo string) string {
	if s, ok := c.Secrets[repo]; ok {
		return s
	}
	return c.WebhookSecret
}
