package cloud

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"

	"codr.my/codr/internal/backup"
	"codr.my/codr/internal/repos"
	"codr.my/codr/internal/secret"
	"codr.my/codr/internal/store"
)

// ErrNotConfigured means a setting the run needs is empty; ErrBusy means a run is already going.
var (
	ErrNotConfigured = errors.New("cloud: backup is not configured")
	ErrBusy          = errors.New("cloud: a backup is already running")
)

// Job runs cloud backups for one install. One run at a time per Job.
type Job struct {
	Store      *store.Store
	Disk       repos.Disk
	GitBinary  string
	ConfigPath string     // copied into each snapshot
	LFSRoot    string     // data/lfs: LFS objects go into each snapshot too (D82)
	WorkDir    string     // temp space on the data disk
	Secret     secret.Key // auth.secret_key: unseals the bucket secret and the recovery key
	HTTP       *http.Client
	mu         sync.Mutex
	running    bool
}

// Outcome is what a successful run produced.
type Outcome struct {
	Object string
	Bytes  int64
	Repos  int
	Pruned int
}

// Running reports whether a run is in progress.
func (j *Job) Running() bool {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.running
}

// Configured reports whether every value a run needs is present.
func Configured(c store.CloudConfig) bool {
	return c.Endpoint != "" && c.Bucket != "" && c.AccessKey != "" && c.SecretSealed != "" && c.KeySealed != ""
}

// Client builds the S3 client from the stored configuration.
func (j *Job) Client(c store.CloudConfig) (*Client, error) {
	if !Configured(c) {
		return nil, ErrNotConfigured
	}
	sk, err := j.Secret.Open(c.SecretSealed)
	if err != nil {
		return nil, fmt.Errorf("cloud: bucket secret: %w", err)
	}
	return &Client{Endpoint: c.Endpoint, Region: c.Region, Bucket: c.Bucket, AccessKey: c.AccessKey, SecretKey: sk, HTTP: j.HTTP}, nil
}

// ObjectPrefix is where this install's archives live in the bucket.
func ObjectPrefix(c store.CloudConfig) string {
	p := strings.Trim(c.Prefix, "/")
	if p == "" {
		return "codr-"
	}
	return p + "/codr-"
}

// Run takes a snapshot, uploads it encrypted, downloads it back and proves it opens and matches, then
// prunes old archives beyond Keep. The run is recorded in cloud_backup_runs either way.
func (j *Job) Run(ctx context.Context, trigger string) (out Outcome, err error) {
	j.mu.Lock()
	if j.running {
		j.mu.Unlock()
		return out, ErrBusy
	}
	j.running = true
	j.mu.Unlock()
	defer func() { j.mu.Lock(); j.running = false; j.mu.Unlock() }()

	cfg, err := j.Store.CloudConfig(ctx)
	if err != nil {
		return out, err
	}
	id, err := j.Store.StartCloudRun(ctx, trigger)
	if err != nil {
		return out, err
	}
	defer func() {
		if err != nil {
			_ = j.Store.FinishCloudRun(context.WithoutCancel(ctx), id, "failed", out.Object, out.Bytes, out.Repos, err.Error())
		} else {
			_ = j.Store.FinishCloudRun(context.WithoutCancel(ctx), id, "ok", out.Object, out.Bytes, out.Repos, fmt.Sprintf("verified; %d pruned", out.Pruned))
		}
	}()
	cl, err := j.Client(cfg)
	if err != nil {
		return out, err
	}
	hexKey, err := j.Secret.Open(cfg.KeySealed)
	if err != nil {
		return out, fmt.Errorf("cloud: recovery key: %w", err)
	}
	key, err := ParseRecoveryKey(hexKey)
	if err != nil {
		return out, err
	}
	if err := os.MkdirAll(j.WorkDir, 0o700); err != nil {
		return out, err
	}
	tmp, err := os.MkdirTemp(j.WorkDir, "cloud-")
	if err != nil {
		return out, err
	}
	defer os.RemoveAll(tmp)

	snap, err := backup.Snapshot(ctx, backup.Options{Store: j.Store, Disk: j.Disk, GitBinary: j.GitBinary, ConfigPath: j.ConfigPath, DestRoot: tmp, LFSRoot: j.LFSRoot})
	if err != nil {
		return out, err
	}
	out.Repos = len(snap.Manifest.Repos)
	name := filepath.Base(snap.Dir) + ".tar.gz.enc"
	encPath := filepath.Join(tmp, name)
	if err := sealDir(snap.Dir, encPath, key); err != nil {
		return out, err
	}
	if st, err := os.Stat(encPath); err == nil {
		out.Bytes = st.Size()
	}
	out.Object = ObjectPrefix(cfg) + strings.TrimPrefix(name, "codr-")
	if err := cl.Put(ctx, out.Object, encPath); err != nil {
		return out, err
	}
	// the restore test: what is in the bucket, not what we meant to send
	got, err := Fetch(ctx, cl, out.Object, key, filepath.Join(tmp, "pulih"))
	if err != nil {
		return out, fmt.Errorf("cloud: restore test: %w", err)
	}
	m, err := backup.ReadManifest(got)
	if err != nil {
		return out, fmt.Errorf("cloud: restore test: %w", err)
	}
	if err := sameManifest(snap.Manifest, m); err != nil {
		return out, fmt.Errorf("cloud: restore test: %w", err)
	}
	out.Pruned, err = Prune(ctx, cl, ObjectPrefix(cfg), cfg.Keep)
	if err != nil {
		return out, fmt.Errorf("cloud: prune: %w", err)
	}
	_ = j.Store.Audit(ctx, 0, "backup.cloud", out.Object, fmt.Sprintf("%d repos, %d bytes, verified", out.Repos, out.Bytes))
	return out, nil
}

func sealDir(dir, dest string, key []byte) error {
	f, err := os.OpenFile(dest, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) // #nosec G304 -- our temp dir
	if err != nil {
		return err
	}
	defer f.Close()
	w, err := Seal(f, key)
	if err != nil {
		return err
	}
	if err := Pack(w, dir); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	return f.Sync()
}

// Fetch downloads object, opens it with key and unpacks it under dest; it returns the snapshot folder.
func Fetch(ctx context.Context, cl *Client, object string, key []byte, dest string) (string, error) {
	if err := os.MkdirAll(dest, 0o700); err != nil {
		return "", err
	}
	file := filepath.Join(dest, path.Base(object))
	f, err := os.OpenFile(file, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600) // #nosec G304 -- caller's empty folder
	if err != nil {
		return "", err
	}
	defer func() { f.Close(); os.Remove(file) }()
	if err := cl.Get(ctx, object, f); err != nil {
		return "", err
	}
	if _, err := f.Seek(0, 0); err != nil {
		return "", err
	}
	r, err := Open(f, key)
	if err != nil {
		return "", err
	}
	return Unpack(r, dest)
}

// Prune deletes the oldest archives under prefix beyond keep (keys carry a UTC timestamp, so they sort).
func Prune(ctx context.Context, cl *Client, prefix string, keep int) (int, error) {
	objs, err := cl.List(ctx, prefix)
	if err != nil {
		return 0, err
	}
	var ours []Object
	for _, o := range objs {
		if strings.HasSuffix(o.Key, ".tar.gz.enc") {
			ours = append(ours, o)
		}
	}
	n := 0
	for i := 0; i+keep < len(ours); i++ {
		if err := cl.Delete(ctx, ours[i].Key); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

func sameManifest(a, b backup.Manifest) error {
	if len(a.Repos) != len(b.Repos) || a.Schema != b.Schema {
		return fmt.Errorf("manifest differs: %d/%d repos, schema %d/%d", len(a.Repos), len(b.Repos), a.Schema, b.Schema)
	}
	for i := range a.Repos {
		if a.Repos[i] != b.Repos[i] {
			return fmt.Errorf("manifest differs at %s", a.Repos[i].Ref)
		}
	}
	return nil
}
