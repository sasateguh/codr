package main

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
)

func TestRunBasics(t *testing.T) {
	t.Setenv("CODR_LANG", "en-US")
	cases := []struct {
		args []string
		code int
		out  string
	}{
		{[]string{"version"}, 0, "codr "},
		{[]string{"help"}, 0, "Usage:"},
		{[]string{}, 2, "Usage:"},
		{[]string{"bogus"}, 2, "unknown command: bogus"},
		{[]string{"--config", "/nonexistent/config.toml", "migrate"}, 2, "config not found"},
	}
	for _, c := range cases {
		var out, errOut bytes.Buffer
		if got := run(c.args, strings.NewReader(""), &out, &errOut); got != c.code {
			t.Errorf("run(%v) code = %d, want %d (%s %s)", c.args, got, c.code, out.String(), errOut.String())
		}
		if !bytes.Contains(append(out.Bytes(), errOut.Bytes()...), []byte(c.out)) {
			t.Errorf("run(%v) output lacks %q: %q %q", c.args, c.out, out.String(), errOut.String())
		}
	}
}

func TestLanguageSwitch(t *testing.T) {
	t.Setenv("CODR_LANG", "ms")
	var out, errOut bytes.Buffer
	run([]string{"bogus"}, strings.NewReader(""), &out, &errOut)
	if !strings.Contains(errOut.String(), "tidak dikenali") {
		t.Fatalf("expected Malay output, got %q", errOut.String())
	}
}

// TestAdminFlow drives init → user → key → repo → collab → rm through the real CLI.
func TestAdminFlow(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	t.Setenv("CODR_LANG", "en-US")
	dir := t.TempDir()
	cfg := filepath.Join(dir, "config.toml")
	do := func(stdin string, wantCode int, args ...string) string {
		t.Helper()
		var out, errOut bytes.Buffer
		code := run(append([]string{"--config", cfg}, args...), strings.NewReader(stdin), &out, &errOut)
		if code != wantCode {
			t.Fatalf("run(%v) code = %d, want %d\nout: %s\nerr: %s", args, code, wantCode, out.String(), errOut.String())
		}
		return out.String() + errOut.String()
	}
	if got := do("", 0, "init", "--dir", dir); !strings.Contains(got, "Codr set up") {
		t.Fatalf("init: %s", got)
	}
	do("", 2, "init", "--dir", dir) // refuses to overwrite
	if got := do("", 0, "migrate", "--check"); !strings.Contains(got, "up to date") {
		t.Fatalf("migrate --check: %s", got)
	}
	do("short\n", 2, "admin", "user", "add", "aiman")                 // password too short
	do("correct horse battery\n", 2, "admin", "user", "add", "Aiman") // bad name
	do("correct horse battery\n", 2, "admin", "user", "add", "admin") // reserved
	if got := do("correct horse battery\n", 0, "admin", "user", "add", "aiman", "--admin", "--lang", "ms"); !strings.Contains(got, "admin=true") {
		t.Fatalf("user add: %s", got)
	}
	do("correct horse battery\n", 1, "admin", "user", "add", "aiman") // duplicate
	if got := do("", 0, "admin", "user", "ls"); !strings.Contains(got, "aiman") || !strings.Contains(got, "ms-MY") {
		t.Fatalf("user ls: %s", got)
	}
	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	sshPub, _ := ssh.NewPublicKey(pub)
	authorized := string(ssh.MarshalAuthorizedKey(sshPub))
	if got := do(authorized, 0, "admin", "key", "add", "aiman", "--title", "laptop"); !strings.Contains(got, "SHA256:") {
		t.Fatalf("key add: %s", got)
	}
	do(authorized, 2, "admin", "key", "add", "aiman") // duplicate key
	do("not a key", 2, "admin", "key", "add", "aiman")
	do(authorized, 2, "admin", "key", "add", "nobody")
	if got := do("", 0, "admin", "key", "ls", "aiman"); !strings.Contains(got, "laptop") {
		t.Fatalf("key ls: %s", got)
	}
	do("", 2, "admin", "key", "rm", "aiman", "99") // no such key
	do("", 2, "admin", "key", "rm", "aiman", "x")  // not a number
	if got := do("", 0, "admin", "key", "rm", "aiman", "1"); !strings.Contains(got, "1") {
		t.Fatalf("key rm: %s", got)
	}
	if got := do("", 0, "admin", "key", "ls", "aiman"); strings.Contains(got, "laptop") {
		t.Fatalf("key still listed after rm: %s", got)
	}
	do(authorized, 0, "admin", "key", "add", "aiman", "--title", "laptop") // re-add for the tests below
	got := do("", 0, "admin", "repo", "add", "aiman/proj", "--desc", "first")
	if !strings.Contains(got, "private") || !strings.Contains(got, "ssh://git@") {
		t.Fatalf("repo add: %s", got)
	}
	if _, err := os.Stat(filepath.Join(dir, "data", "repos", "1.git", "HEAD")); err != nil {
		t.Fatalf("bare repo missing on disk: %v", err)
	}
	do("", 1, "admin", "repo", "add", "aiman/proj")
	do("", 2, "admin", "repo", "add", "aiman/Proj")
	do("", 2, "admin", "repo", "add", "aiman")
	do("", 2, "admin", "repo", "add", "aiman/../x")
	if got := do("", 0, "admin", "org", "add", "syarikat", "--owner", "aiman"); !strings.Contains(got, "organisation syarikat created") {
		t.Fatalf("org add: %s", got)
	}
	do("", 2, "admin", "org", "add", "aiman", "--owner", "aiman") // a person's name is taken
	if got := do("", 0, "admin", "org", "ls"); !strings.Contains(got, "syarikat  1 members  owners: aiman") {
		t.Fatalf("org ls: %s", got)
	}
	if got := do("", 0, "admin", "user", "ls"); strings.Contains(got, "syarikat") {
		t.Fatalf("user ls lists an organisation: %s", got)
	}
	if got := do("", 0, "admin", "repo", "desc", "aiman/proj", "Codr", "example"); !strings.Contains(got, "aiman/proj updated") {
		t.Fatalf("repo desc: %s", got)
	}
	do("", 2, "admin", "repo", "desc", "aiman/nope", "x")
	if got := do("", 0, "admin", "repo", "ls"); !strings.Contains(got, "aiman/proj") {
		t.Fatalf("repo ls: %s", got)
	}
	do("correct horse battery\n", 0, "admin", "user", "add", "bob")
	if got := do("", 0, "admin", "collab", "add", "aiman/proj", "bob", "--role", "write"); !strings.Contains(got, "bob is now write") {
		t.Fatalf("collab add: %s", got)
	}
	do("", 2, "admin", "collab", "add", "aiman/proj", "bob", "--role", "owner")
	if got := do("", 2, "admin", "repo", "rm", "aiman/proj"); !strings.Contains(got, "--yes") {
		t.Fatalf("repo rm without --yes: %s", got)
	}
	if got := do("", 0, "admin", "repo", "rm", "aiman/proj", "--yes"); !strings.Contains(got, "deleted") {
		t.Fatalf("repo rm: %s", got)
	}
	if _, err := os.Stat(filepath.Join(dir, "data", "repos", "1.git")); !os.IsNotExist(err) {
		t.Fatal("bare repo should be removed from disk")
	}
	do("", 2, "admin", "repo", "rm", "aiman/proj", "--yes")
	if got := do("", 0, "admin", "repo", "ls"); strings.Contains(got, "aiman/proj") {
		t.Fatalf("repo ls after rm: %s", got)
	}
}
