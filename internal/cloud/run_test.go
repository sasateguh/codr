package cloud

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"

	"codr.my/codr/internal/backup"
	"codr.my/codr/internal/repos"
	"codr.my/codr/internal/secret"
	"codr.my/codr/internal/store"
)

// fakeS3 is an in-memory bucket. It checks what a real one checks first: a SigV4 header is present and
// the signed payload hash is the hash of the body.
type fakeS3 struct {
	mu      sync.Mutex
	objects map[string][]byte
	corrupt bool // flip one byte of every GET
}

func (f *fakeS3) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !strings.HasPrefix(r.Header.Get("Authorization"), "AWS4-HMAC-SHA256 Credential=AK/") {
		http.Error(w, "no signature", http.StatusForbidden)
		return
	}
	key := strings.TrimPrefix(r.URL.Path, "/bucket/")
	switch {
	case r.Method == "PUT":
		b, _ := io.ReadAll(r.Body)
		sum := sha256.Sum256(b)
		if hex.EncodeToString(sum[:]) != r.Header.Get("x-amz-content-sha256") {
			http.Error(w, "XAmzContentSHA256Mismatch", http.StatusBadRequest)
			return
		}
		f.objects[key] = b
	case r.Method == "GET" && r.URL.Query().Get("list-type") == "2":
		prefix := r.URL.Query().Get("prefix")
		var keys []string
		for k := range f.objects {
			if strings.HasPrefix(k, prefix) {
				keys = append(keys, k)
			}
		}
		sort.Strings(keys)
		fmt.Fprint(w, `<ListBucketResult>`)
		for _, k := range keys {
			fmt.Fprintf(w, `<Contents><Key>%s</Key><Size>%d</Size><LastModified>2026-10-16T00:00:00.000Z</LastModified></Contents>`, k, len(f.objects[k]))
		}
		fmt.Fprint(w, `<IsTruncated>false</IsTruncated></ListBucketResult>`)
	case r.Method == "GET":
		b, ok := f.objects[key]
		if !ok {
			http.Error(w, "NoSuchKey", http.StatusNotFound)
			return
		}
		if f.corrupt {
			b = append([]byte(nil), b...)
			b[len(b)/2] ^= 1
		}
		_, _ = w.Write(b)
	case r.Method == "DELETE":
		delete(f.objects, key)
		w.WriteHeader(http.StatusNoContent)
	}
}

func run(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@example.invalid", "-c", "init.defaultBranch=main"}, args...)...)
	cmd.Dir = dir
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + dir, "GIT_CONFIG_NOSYSTEM=1"}
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v %s", args, err, out)
	}
}

// TestRunEndToEnd: a real snapshot of a store with a repository goes to the bucket encrypted, is fetched
// back and checked, old archives are pruned to Keep, and a bucket that returns damaged bytes fails the run.
func TestRunEndToEnd(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "codr.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	if _, _, err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	u, _ := st.CreateUser(ctx, "aiman", "", "h", true, "en-US")
	r, _ := st.CreateRepo(ctx, u.ID, "proj", "", true, "main")
	disk := repos.Disk{Root: filepath.Join(dir, "repos"), GitBinary: "git"}
	if err := disk.Create(ctx, r.ID, "main"); err != nil {
		t.Fatal(err)
	}
	w := filepath.Join(dir, "w")
	_ = os.MkdirAll(w, 0o700)
	run(t, w, "init", "-q")
	_ = os.WriteFile(filepath.Join(w, "a.txt"), []byte("satu\n"), 0o600)
	run(t, w, "add", ".")
	run(t, w, "commit", "-q", "-m", "pertama")
	run(t, w, "push", "-q", disk.Path(r.ID), "main")
	cfgPath := filepath.Join(dir, "config.toml")
	_ = os.WriteFile(cfgPath, []byte("[auth]\nsecret_key = 'x'\n"), 0o600)

	hexSecret, _ := secret.Generate()
	sk, _ := secret.Parse(hexSecret)
	recovery, _ := NewRecoveryKey()
	sealedKey, _ := sk.Seal(recovery)
	sealedSecret, _ := sk.Seal("SECRET")
	fake := &fakeS3{objects: map[string][]byte{}}
	srv := httptest.NewServer(fake)
	defer srv.Close()
	if err := st.SetCloudConfig(ctx, store.CloudConfig{Enabled: true, Endpoint: srv.URL, Region: "auto", Bucket: "bucket", Prefix: "codr", AccessKey: "AK",
		SecretSealed: sealedSecret, KeySealed: sealedKey, KeyHint: KeyHint(recovery), Keep: 2, IntervalHours: 24}); err != nil {
		t.Fatal(err)
	}
	job := &Job{Store: st, Disk: disk, GitBinary: "git", ConfigPath: cfgPath, WorkDir: filepath.Join(dir, "tmp"), Secret: sk}
	var objects []string
	for i := 0; i < 3; i++ {
		out, err := job.Run(ctx, "test")
		if err != nil {
			t.Fatalf("run %d: %v", i, err)
		}
		if out.Repos != 1 || out.Bytes == 0 || !strings.HasPrefix(out.Object, "codr/codr-") {
			t.Fatalf("outcome: %+v", out)
		}
		objects = append(objects, out.Object)
		// timestamps are per second: make the next key distinct
		if i < 2 {
			fake.mu.Lock()
			b := fake.objects[out.Object]
			delete(fake.objects, out.Object)
			renamed := strings.Replace(out.Object, "codr-", fmt.Sprintf("codr-%d", i), 1)
			fake.objects[renamed] = b
			fake.mu.Unlock()
		}
	}
	if len(fake.objects) != 2 {
		t.Fatalf("keep=2 must leave 2 archives, bucket has %d", len(fake.objects))
	}
	// nothing in the bucket is readable without the key
	for k, b := range fake.objects {
		if bytes.Contains(b, []byte("pertama")) || bytes.Contains(b, []byte("manifest")) {
			t.Fatalf("%s holds plaintext", k)
		}
	}
	// the owner's restore path: fetch with the recovery key, a valid snapshot comes out
	key, _ := ParseRecoveryKey(recovery)
	cl := &Client{Endpoint: srv.URL, Region: "auto", Bucket: "bucket", AccessKey: "AK", SecretKey: "SECRET"}
	snap, err := Fetch(ctx, cl, objects[2], key, filepath.Join(dir, "pulih"))
	if err != nil {
		t.Fatal(err)
	}
	if m, err := backup.ReadManifest(snap); err != nil || len(m.Repos) != 1 || m.Repos[0].Head == "" {
		t.Fatalf("restored manifest: %+v %v", m, err)
	}
	runs, _ := st.CloudRuns(ctx, 10)
	if len(runs) != 3 || runs[0].Status != "ok" {
		t.Fatalf("runs: %+v", runs)
	}
	// a bucket handing back damaged bytes fails the restore test and the run is recorded as failed
	fake.corrupt = true
	if _, err := job.Run(ctx, "test"); err == nil || !strings.Contains(err.Error(), "restore test") {
		t.Fatalf("damaged download must fail the run: %v", err)
	}
	if runs, _ := st.CloudRuns(ctx, 1); runs[0].Status != "failed" || !strings.Contains(runs[0].Detail, "restore test") {
		t.Fatalf("failed run not recorded: %+v", runs)
	}
	// not configured: refused before anything is written
	_ = st.SetCloudConfig(ctx, store.CloudConfig{Keep: 2, IntervalHours: 24, Region: "auto"})
	if _, err := job.Run(ctx, "test"); err != ErrNotConfigured {
		t.Fatalf("unconfigured run: %v", err)
	}
}
