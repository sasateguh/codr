// Package config loads config.toml — the infrastructure layer of settings (D14):
// paths, listen addresses, process limits, hashing cost. Values that an admin
// changes at runtime (site name, registration, limits) live in the settings table instead.
package config

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"strings"

	toml "github.com/pelletier/go-toml/v2"
)

// Config mirrors config.toml. Field tags are the TOML keys.
type Config struct {
	Server Server `toml:"server"`
	Git    Git    `toml:"git"`
	Auth   Auth   `toml:"auth"`
	Site   Site   `toml:"site"`

	// Dir is the directory holding config.toml; relative paths resolve against it.
	Dir string `toml:"-"`
}

// Server holds paths and listen addresses.
type Server struct {
	DataDir  string `toml:"data_dir"`
	HTTPAddr string `toml:"http_addr"`
	// HTTPHost is the public host name shown in HTTPS clone URLs (scheme included).
	HTTPHost string `toml:"http_host"`
	SSHAddr  string `toml:"ssh_addr"`
	// SSHHost is the public host name shown in clone URLs (may include :port).
	SSHHost string `toml:"ssh_host"`
	// SSHExecWaitSeconds bounds handshake + time until a session sends its git command.
	SSHExecWaitSeconds int `toml:"ssh_exec_wait_seconds"`
	// HTTPReadHeaderSeconds / HTTPIdleSeconds are the HTTP server's slow-client bounds
	// (bodies are unbounded in time: a clone may legitimately take long).
	HTTPReadHeaderSeconds int `toml:"http_read_header_seconds"`
	HTTPIdleSeconds       int `toml:"http_idle_seconds"`
	// TrustedProxies lists CIDRs (or single addresses) whose connections carry the real client
	// in ClientIPHeader — a Cloudflare Tunnel's cloudflared container, a reverse proxy. Empty =
	// nobody is trusted and the TCP peer is the client (D44). Behind a tunnel with this empty,
	// every visitor shares one address and one failed-login counter.
	TrustedProxies []string `toml:"trusted_proxies"`
	// ClientIPHeader is read only when the peer is in TrustedProxies.
	ClientIPHeader string `toml:"client_ip_header"`
	// EdgeRanges are the CDN edge networks (Cloudflare's published list by default) that may set
	// ClientIPHeader. When a trusted proxy forwards X-Forwarded-For (traefik does, D58), its last hop must
	// be inside these ranges for the header to count; anyone reaching the proxy directly is counted as the
	// address the proxy saw. Empty = no edge check (the D44 behaviour, right for a tunnel only).
	EdgeRanges []string `toml:"edge_ranges"`
}

// Git holds how the git binary is run.
type Git struct {
	Binary         string `toml:"binary"`
	TimeoutSeconds int    `toml:"timeout_seconds"`
	// MaxInputMB is receive.maxInputSize: git aborts a pack above this before any hook runs.
	// The product limit (settings repo.push_max_mb, localised message) is lower; this is the
	// disk-filling bound. 0 = none.
	MaxInputMB int64 `toml:"max_input_mb"`
	// MirrorTickSeconds is how often serve looks for mirrors that are due (their own interval
	// is a product setting: mirror.default_interval_minutes / per mirror).
	MirrorTickSeconds int `toml:"mirror_tick_seconds"`
}

// Auth holds password hashing cost and SSH auth limits.
type Auth struct {
	Argon2Time      uint32 `toml:"argon2_time"`
	Argon2MemoryKiB uint32 `toml:"argon2_memory_kib"`
	Argon2Threads   uint8  `toml:"argon2_threads"`
	PasswordMinLen  int    `toml:"password_min_len"`
	SessionHours    int    `toml:"session_hours"`
	SSHMaxAuthTries int    `toml:"ssh_max_auth_tries"`
	// HTTPFailMax failed token attempts per client address inside HTTPFailWindowSeconds block it.
	HTTPFailMax           int `toml:"http_fail_max"`
	HTTPFailWindowSeconds int `toml:"http_fail_window_seconds"`
	// SecretKey (64 hex) seals mirror tokens in the DB (PRD §9). Written by `amcd init`;
	// an older install without it can still run — doctor says so and imports with a token refuse.
	SecretKey string `toml:"secret_key"`
}

// Site holds the defaults used before the settings table is reachable (CLI language).
type Site struct {
	DefaultLanguage string `toml:"default_language"`
	// LandingHosts are host names that get the product landing page instead of the forge (D56):
	// the first is canonical, the others redirect to it. Empty = no landing page.
	LandingHosts []string `toml:"landing_hosts"`
	// RedirectHosts moves an old name to a new one, "from=to" (D58): every request to from gets 301 (GET/HEAD) or
	// 308 (anything else, method kept) to the same path on to. git follows the redirect of its first request and
	// then talks to the new name, so clones and pushes through an old URL keep working.
	RedirectHosts []string `toml:"redirect_hosts"`
}

// Redirects parses site.redirect_hosts into from → to.
func (c Config) Redirects() (map[string]string, error) {
	out := map[string]string{}
	for _, raw := range c.Site.RedirectHosts {
		from, to, ok := strings.Cut(strings.TrimSpace(raw), "=")
		if !ok || !bareHost(from) || !bareHost(to) || from == to {
			return nil, fmt.Errorf("site.redirect_hosts: %q must be old.host=new.host (bare lower-case names)", raw)
		}
		out[from] = to
	}
	return out, nil
}

func bareHost(h string) bool {
	return h != "" && h == strings.ToLower(h) && !strings.ContainsAny(h, ":/ =")
}

// Default returns the configuration written by `amcd init`. This is the single
// place where these numbers exist; after init the file on disk is the source.
func Default() Config {
	return Config{
		Server: Server{DataDir: "data", HTTPAddr: "127.0.0.1:3000", HTTPHost: "http://localhost:3000", SSHAddr: "127.0.0.1:2222", SSHHost: "localhost:2222", SSHExecWaitSeconds: 30, HTTPReadHeaderSeconds: 15, HTTPIdleSeconds: 120, TrustedProxies: []string{}, ClientIPHeader: "CF-Connecting-IP",
			EdgeRanges: []string{"173.245.48.0/20", "103.21.244.0/22", "103.22.200.0/22", "103.31.4.0/22", "141.101.64.0/18", "108.162.192.0/18",
				"190.93.240.0/20", "188.114.96.0/20", "197.234.240.0/22", "198.41.128.0/17", "162.158.0.0/15", "104.16.0.0/13", "104.24.0.0/14",
				"172.64.0.0/13", "131.0.72.0/22", "2400:cb00::/32", "2606:4700::/32", "2803:f800::/32", "2405:b500::/32", "2405:8100::/32",
				"2a06:98c0::/29", "2c0f:f248::/32"}},
		Git:  Git{Binary: "git", TimeoutSeconds: 3600, MaxInputMB: 2048, MirrorTickSeconds: 60},
		Auth: Auth{Argon2Time: 3, Argon2MemoryKiB: 64 * 1024, Argon2Threads: 2, PasswordMinLen: 12, SessionHours: 720, SSHMaxAuthTries: 4, HTTPFailMax: 20, HTTPFailWindowSeconds: 600},
		Site: Site{DefaultLanguage: "en-US", LandingHosts: []string{}, RedirectHosts: []string{}},
	}
}

// Load reads and validates config.toml at path.
func Load(path string) (Config, error) {
	raw, err := os.ReadFile(path) // #nosec G304 -- path is the operator's --config flag
	if err != nil {
		return Config{}, err
	}
	c := Default()
	if err := toml.Unmarshal(raw, &c); err != nil {
		return Config{}, fmt.Errorf("config %s: %w", path, err)
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return Config{}, err
	}
	c.Dir = filepath.Dir(abs)
	if err := c.Validate(); err != nil {
		return Config{}, fmt.Errorf("config %s: %w", path, err)
	}
	if err := c.adoptLegacyDB(); err != nil {
		return Config{}, fmt.Errorf("rename %s to %s: %w", LegacyDBName, DBName, err)
	}
	return c, nil
}

// Validate rejects values that would make the server unsafe or unusable.
func (c Config) Validate() error {
	switch {
	case c.Server.DataDir == "":
		return errors.New("server.data_dir is empty")
	case c.Server.SSHAddr == "" || c.Server.HTTPAddr == "":
		return errors.New("server.ssh_addr / server.http_addr is empty")
	case c.Server.HTTPHost == "" || !strings.HasPrefix(c.Server.HTTPHost, "http://") && !strings.HasPrefix(c.Server.HTTPHost, "https://"):
		return errors.New("server.http_host must start with http:// or https://")
	case c.Git.MaxInputMB < 0:
		return errors.New("git.max_input_mb must be >= 0")
	case c.Git.MirrorTickSeconds <= 0:
		return errors.New("git.mirror_tick_seconds must be > 0")
	case c.Server.SSHExecWaitSeconds <= 0:
		return errors.New("server.ssh_exec_wait_seconds must be > 0")
	case c.Git.Binary == "":
		return errors.New("git.binary is empty")
	case c.Git.TimeoutSeconds <= 0:
		return errors.New("git.timeout_seconds must be > 0")
	case c.Auth.Argon2Time == 0 || c.Auth.Argon2MemoryKiB < 8*1024 || c.Auth.Argon2Threads == 0:
		return errors.New("auth.argon2_* too weak")
	case c.Auth.PasswordMinLen < 8:
		return errors.New("auth.password_min_len must be >= 8")
	case c.Auth.SessionHours <= 0 || c.Auth.SSHMaxAuthTries <= 0:
		return errors.New("auth.session_hours and auth.ssh_max_auth_tries must be > 0")
	case c.Auth.HTTPFailMax <= 0 || c.Auth.HTTPFailWindowSeconds <= 0:
		return errors.New("auth.http_fail_max and auth.http_fail_window_seconds must be > 0")
	case c.Server.HTTPReadHeaderSeconds <= 0 || c.Server.HTTPIdleSeconds <= 0:
		return errors.New("server.http_read_header_seconds and server.http_idle_seconds must be > 0")
	case c.Auth.SecretKey != "" && (len(c.Auth.SecretKey) != 64 || !isHex(c.Auth.SecretKey)):
		return errors.New("auth.secret_key must be 64 hex characters (or absent)")
	case c.Site.DefaultLanguage == "":
		return errors.New("site.default_language is empty")
	case len(c.Server.TrustedProxies) > 0 && strings.TrimSpace(c.Server.ClientIPHeader) == "":
		return errors.New("server.client_ip_header is empty while server.trusted_proxies is set")
	}
	if _, err := c.Redirects(); err != nil {
		return err
	}
	for _, h := range c.Site.LandingHosts {
		if !bareHost(h) {
			return fmt.Errorf("site.landing_hosts: %q must be a bare lower-case host name (no scheme, port or path)", h)
		}
	}
	if _, err := c.TrustedNets(); err != nil {
		return err
	}
	if _, err := c.EdgeNets(); err != nil {
		return err
	}
	return nil
}

// DataDir returns the absolute data directory.
func (c Config) DataDir() string {
	if filepath.IsAbs(c.Server.DataDir) {
		return c.Server.DataDir
	}
	return filepath.Join(c.Dir, c.Server.DataDir)
}

// DBPath is the SQLite file.
func (c Config) DBPath() string { return filepath.Join(c.DataDir(), DBName) }

// DBName is the SQLite file in the data folder; LegacyDBName is its name before the rename to Codr (D58).
const (
	DBName       = "codr.db"
	LegacyDBName = "amcd.db"
)

// adoptLegacyDB renames data/amcd.db (with its -wal and -shm) to codr.db when only the old name exists.
// It runs on every Load, so any command finds the database. It is NOT safe while another process has the
// old file open: the deploy script stops the old container before the first codr binary touches the data (D58).
func (c Config) adoptLegacyDB() error {
	cur, old := c.DBPath(), filepath.Join(c.DataDir(), LegacyDBName)
	if _, err := os.Stat(cur); err == nil {
		return nil
	}
	if _, err := os.Stat(old); err != nil {
		return nil
	}
	for _, suf := range []string{"-wal", "-shm"} {
		if _, err := os.Stat(old + suf); err == nil {
			if err := os.Rename(old+suf, cur+suf); err != nil {
				return err
			}
		}
	}
	return os.Rename(old, cur)
}

// ReposDir holds bare repositories, one per numeric id (D27).
func (c Config) ReposDir() string { return filepath.Join(c.DataDir(), "repos") }

// LFSDir holds Git LFS objects (D82), one folder per repository id.
func (c Config) LFSDir() string { return filepath.Join(c.DataDir(), "lfs") }

// SSHDir holds generated host keys.
func (c Config) SSHDir() string { return filepath.Join(c.DataDir(), "ssh") }

// HooksDir holds the hook scripts git runs (forced core.hooksPath).
func (c Config) HooksDir() string { return filepath.Join(c.DataDir(), "hooks") }

// HookSocket is the Unix socket hooks talk to (D9). Unix socket paths are limited to ~108
// bytes (sun_path); a data dir nested deep enough makes bind fail with "invalid argument", so
// such installs get a short socket in the OS temp dir named by a hash of the data dir. It is
// recreated on every serve start, so a wiped /tmp costs nothing.
func (c Config) HookSocket() string {
	p := filepath.Join(c.DataDir(), "hook.sock")
	if len(p) <= maxSocketPath {
		return p
	}
	sum := sha256.Sum256([]byte(c.DataDir()))
	return filepath.Join(os.TempDir(), "codr-"+hex.EncodeToString(sum[:6])+".sock")
}

const maxSocketPath = 100

func isHex(s string) bool {
	_, err := hex.DecodeString(s)
	return err == nil
}

// Write serialises c to path (used by `amcd init`). Refuses to overwrite.
func Write(path string, c Config) error {
	raw, err := toml.Marshal(c)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) // #nosec G304 -- operator path
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.Write(raw)
	return err
}

// TrustedNets parses server.trusted_proxies; a bare address means that one host.
func (c Config) TrustedNets() ([]netip.Prefix, error) {
	return parseNets("server.trusted_proxies", c.Server.TrustedProxies)
}

// EdgeNets parses server.edge_ranges.
func (c Config) EdgeNets() ([]netip.Prefix, error) {
	return parseNets("server.edge_ranges", c.Server.EdgeRanges)
}

func parseNets(key string, list []string) ([]netip.Prefix, error) {
	out := make([]netip.Prefix, 0, len(list))
	for _, raw := range list {
		raw = strings.TrimSpace(raw)
		if p, err := netip.ParsePrefix(raw); err == nil {
			out = append(out, p.Masked())
			continue
		}
		a, err := netip.ParseAddr(raw)
		if err != nil {
			return nil, fmt.Errorf("%s: %q is not an address or CIDR", key, raw)
		}
		out = append(out, netip.PrefixFrom(a, a.BitLen()))
	}
	return out, nil
}
