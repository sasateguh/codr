package gitcmd

import (
	"bytes"
	"context"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestParse(t *testing.T) {
	for w, want := range map[string]Service{"git-upload-pack": UploadPack, "upload-pack": UploadPack, "git-receive-pack": ReceivePack, "receive-pack": ReceivePack} {
		if got, ok := Parse(w); !ok || got != want {
			t.Errorf("Parse(%q) = %q %v", w, got, ok)
		}
	}
	for _, w := range []string{"", "git", "ls", "git-shell", "upload-archive", "git-upload-archive", "GIT-UPLOAD-PACK"} {
		if _, ok := Parse(w); ok {
			t.Errorf("Parse(%q) must be rejected", w)
		}
	}
}

func TestRunRefusesUnknownService(t *testing.T) {
	r := Runner{Binary: "git", Timeout: time.Second}
	if _, err := r.Run(context.Background(), Service("ls"), "/", "", Push{}, strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{}); err == nil {
		t.Fatal("unknown service must be refused before exec")
	}
}

func TestRunUploadPackOnMissingRepoExitsNonZero(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	r := Runner{Binary: "git", Timeout: 5 * time.Second}
	var out, errb bytes.Buffer
	code, err := r.Run(context.Background(), UploadPack, t.TempDir()+"/nope.git", "", Push{}, strings.NewReader(""), &out, &errb)
	if err != nil {
		t.Fatal(err)
	}
	if code == 0 {
		t.Fatalf("expected non-zero exit, stderr=%s", errb.String())
	}
}
