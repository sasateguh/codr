package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWriteLoadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "config.toml")
	if err := Write(p, Default()); err != nil {
		t.Fatal(err)
	}
	if err := Write(p, Default()); err == nil {
		t.Fatal("second Write must refuse to overwrite")
	}
	c, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if c.DataDir() != filepath.Join(dir, "data") || c.DBPath() != filepath.Join(dir, "data", "codr.db") {
		t.Fatalf("paths: %s %s", c.DataDir(), c.DBPath())
	}
	if c.Auth.PasswordMinLen < 8 || c.Git.Binary == "" {
		t.Fatal("defaults missing")
	}
}

// TestAdoptLegacyDB: an install from before the rename keeps its database (D58).
func TestAdoptLegacyDB(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "config.toml")
	if err := Write(p, Default()); err != nil {
		t.Fatal(err)
	}
	data := filepath.Join(dir, "data")
	_ = os.MkdirAll(data, 0o700)
	for _, f := range []string{"amcd.db", "amcd.db-wal"} {
		if err := os.WriteFile(filepath.Join(data, f), []byte(f), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := Load(p); err != nil {
		t.Fatal(err)
	}
	for f, want := range map[string]string{"codr.db": "amcd.db", "codr.db-wal": "amcd.db-wal"} {
		if b, err := os.ReadFile(filepath.Join(data, f)); err != nil || string(b) != want {
			t.Fatalf("%s: %q %v", f, b, err)
		}
	}
	if _, err := os.Stat(filepath.Join(data, "amcd.db")); !os.IsNotExist(err) {
		t.Fatal("old name must be gone")
	}
	// both present: nothing moves
	_ = os.WriteFile(filepath.Join(data, "amcd.db"), []byte("lain"), 0o600)
	if _, err := Load(p); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(data, "codr.db")); string(b) != "amcd.db" {
		t.Fatal("codr.db overwritten")
	}
}

func TestValidateRejectsWeak(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(p, []byte("[auth]\nargon2_memory_kib = 16\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(p); err == nil {
		t.Fatal("weak argon2 must be rejected")
	}
	if err := os.WriteFile(p, []byte("[git]\ntimeout_seconds = 0\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(p); err == nil {
		t.Fatal("zero timeout must be rejected")
	}
}

func TestHookSocketShortensLongPaths(t *testing.T) {
	c := Default()
	c.Dir = "/x"
	if got := c.HookSocket(); got != "/x/data/hook.sock" {
		t.Fatalf("short path: %s", got)
	}
	c.Dir = "/" + strings.Repeat("sangat-panjang/", 10)
	got := c.HookSocket()
	if len(got) > maxSocketPath || !strings.HasPrefix(got, os.TempDir()) || !strings.HasSuffix(got, ".sock") {
		t.Fatalf("long path not shortened: %s", got)
	}
	if c.HookSocket() != got {
		t.Fatal("must be stable for the same data dir")
	}
}

func TestTrustedProxiesParsedAndValidated(t *testing.T) {
	c := Default()
	c.Server.TrustedProxies = []string{"10.0.16.0/24", " 198.51.100.7 ", "2001:db8::/32"}
	nets, err := c.TrustedNets()
	if err != nil || len(nets) != 3 || nets[1].Bits() != 32 || nets[2].Bits() != 32 {
		t.Fatalf("parse: %v %v", nets, err)
	}
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	c.Server.ClientIPHeader = ""
	if err := c.Validate(); err == nil {
		t.Fatal("trusted proxies without a header must be rejected")
	}
	c = Default()
	c.Server.TrustedProxies = []string{"cloudflared"}
	if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "trusted_proxies") {
		t.Fatalf("a hostname is not an address: %v", err)
	}
	// init writes the keys so an operator finds them in config.toml, not only in docs
	p := filepath.Join(t.TempDir(), "config.toml")
	if err := Write(p, Default()); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(p)
	if !strings.Contains(string(raw), "trusted_proxies = []") || !strings.Contains(string(raw), "client_ip_header = ") {
		t.Fatalf("config.toml lacks proxy keys:\n%s", raw)
	}
}
