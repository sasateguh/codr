// Package backup writes a consistent snapshot of an install (D41, D54): the DB via VACUUM INTO (WAL-safe),
// config.toml, one git bundle per repository and a manifest with every HEAD. The CLI (`codr backup`) and the
// cloud scheduler (D70) both call Snapshot, so there is one way to take a backup.
package backup

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"codr.my/codr/internal/config"
	"codr.my/codr/internal/lfs"
	"codr.my/codr/internal/repos"
	"codr.my/codr/internal/store"
	"codr.my/codr/internal/version"
)

// Manifest describes one backup so restore can verify it reproduced the instance.
type Manifest struct {
	Version string    `json:"version"`
	Schema  int       `json:"schema"`
	TakenAt string    `json:"taken_at"`
	Repos   []Repo    `json:"repos"`
	LFS     []LFSRepo `json:"lfs,omitempty"` // Git LFS objects per repository (D82), under lfs/ in the snapshot
}

// LFSRepo is how many LFS objects a repository had, so restore can check it got them all.
type LFSRepo struct {
	ID      int64 `json:"id"`
	Objects int   `json:"objects"`
	Bytes   int64 `json:"bytes"`
}

// Repo is one repository in a manifest.
type Repo struct {
	ID     int64  `json:"id"`
	Ref    string `json:"ref"`              // owner/name
	Head   string `json:"head"`             // commit hash of HEAD, "" when empty
	Branch string `json:"branch"`           // symbolic HEAD
	Bundle string `json:"bundle"`           // file name, "" when the repo has no commits
	Mirror bool   `json:"mirror,omitempty"` // read-only mirror: no bundle unless WithMirrors (D54)
	// the wiki (D83): its own bundle and head, "" when the repository has no wiki pages
	WikiBundle string `json:"wiki_bundle,omitempty"`
	WikiHead   string `json:"wiki_head,omitempty"`
}

// Result is what a snapshot produced.
type Result struct {
	Dir            string
	Manifest       Manifest
	MirrorsSkipped int
}

// RepoMissing means a DB row has no folder on disk: such a backup must never pass for a clean one.
type RepoMissing struct{ Ref, Path string }

func (e RepoMissing) Error() string { return "repository folder missing: " + e.Ref + " " + e.Path }

// Options are the inputs of a snapshot.
type Options struct {
	Store       *store.Store
	Disk        repos.Disk
	GitBinary   string
	ConfigPath  string // copied into the snapshot (auth.secret_key unseals mirror tokens)
	DestRoot    string // the snapshot is written to DestRoot/codr-<stamp>
	WithMirrors bool
	LFSRoot     string // data/lfs; "" = no LFS objects in the snapshot
}

// Snapshot writes a new snapshot; on any error nothing is left behind.
func Snapshot(ctx context.Context, o Options) (res Result, err error) {
	stamp := time.Now().UTC().Format("20060102-150405")
	dest := filepath.Join(o.DestRoot, "codr-"+stamp)
	if err := os.MkdirAll(filepath.Join(dest, "repos"), 0o700); err != nil {
		return res, err
	}
	defer func() {
		if err != nil {
			_ = os.RemoveAll(dest)
		}
	}()
	if _, err := o.Store.DB().ExecContext(ctx, `VACUUM INTO ?`, filepath.Join(dest, config.DBName)); err != nil {
		return res, fmt.Errorf("vacuum into: %w", err)
	}
	raw, err := os.ReadFile(o.ConfigPath) // #nosec G304 -- operator path
	if err != nil {
		return res, err
	}
	if err := os.WriteFile(filepath.Join(dest, "config.toml"), raw, 0o600); err != nil {
		return res, err
	}
	m := Manifest{Version: version.String(), TakenAt: store.Now()}
	if m.Schema, err = o.Store.Version(ctx); err != nil {
		return res, err
	}
	all, err := o.Store.Repos(ctx, "")
	if err != nil {
		return res, err
	}
	mirrors, err := o.Store.Mirrors(ctx)
	if err != nil {
		return res, err
	}
	isMirror := map[int64]bool{}
	for _, mm := range mirrors {
		isMirror[mm.RepoID] = true
	}
	for _, r := range all {
		mr := Repo{ID: r.ID, Ref: r.Owner + "/" + r.Name, Mirror: isMirror[r.ID]}
		p := o.Disk.Path(r.ID)
		if !o.Disk.Exists(r.ID) {
			return res, RepoMissing{Ref: mr.Ref, Path: p}
		}
		if out, err := GitOut(o.GitBinary, "-C", p, "symbolic-ref", "--short", "HEAD"); err == nil {
			mr.Branch = out
		}
		if out, err := GitOut(o.GitBinary, "-C", p, "rev-parse", "--verify", "-q", "HEAD^{commit}"); err == nil && out != "" {
			mr.Head = out
			if mr.Mirror && !o.WithMirrors {
				res.MirrorsSkipped++
			} else {
				mr.Bundle = strconv.FormatInt(r.ID, 10) + ".bundle"
				if _, err := GitOut(o.GitBinary, "-C", p, "bundle", "create", filepath.Join(dest, "repos", mr.Bundle), "--all"); err != nil {
					return res, fmt.Errorf("bundle %s: %w", mr.Ref, err)
				}
			}
		}
		if wp := o.Disk.WikiPath(r.ID); dirExists(wp) {
			if out, err := GitOut(o.GitBinary, "-C", wp, "rev-parse", "--verify", "-q", "refs/heads/main^{commit}"); err == nil && out != "" {
				mr.WikiHead, mr.WikiBundle = out, strconv.FormatInt(r.ID, 10)+".wiki.bundle"
				if _, err := GitOut(o.GitBinary, "-C", wp, "bundle", "create", filepath.Join(dest, "repos", mr.WikiBundle), "--all"); err != nil {
					return res, fmt.Errorf("bundle wiki %s: %w", mr.Ref, err)
				}
			}
		}
		m.Repos = append(m.Repos, mr)
	}
	if o.LFSRoot != "" {
		// LFS objects never change once written, so hard links make the snapshot instant and free on the same disk;
		// a tar of the snapshot (nightly archive, cloud) still carries the bytes (D82)
		src, dst := lfs.Store{Root: o.LFSRoot}, lfs.Store{Root: filepath.Join(dest, "lfs")}
		for _, r := range all {
			n, b, err := src.Usage(r.ID)
			if err != nil {
				return res, fmt.Errorf("lfs %d: %w", r.ID, err)
			}
			if n == 0 {
				continue
			}
			if _, err := src.LinkAll(r.ID, dst, r.ID); err != nil {
				return res, fmt.Errorf("lfs %d: %w", r.ID, err)
			}
			m.LFS = append(m.LFS, LFSRepo{ID: r.ID, Objects: n, Bytes: b})
		}
	}
	mj, _ := json.MarshalIndent(m, "", "  ")
	if err := os.WriteFile(filepath.Join(dest, "manifest.json"), mj, 0o600); err != nil {
		return res, err
	}
	_ = o.Store.Audit(ctx, 0, "backup.create", dest, strconv.Itoa(len(m.Repos))+" repos")
	return Result{Dir: dest, Manifest: m, MirrorsSkipped: res.MirrorsSkipped}, nil
}

// ReadManifest loads the manifest of an extracted snapshot.
func ReadManifest(dir string) (Manifest, error) {
	var m Manifest
	raw, err := os.ReadFile(filepath.Join(dir, "manifest.json")) // #nosec G304 -- operator path
	if err != nil {
		return m, err
	}
	err = json.Unmarshal(raw, &m)
	return m, err
}

// GitOut runs git with a minimal environment and returns trimmed output.
func GitOut(bin string, args ...string) (string, error) {
	cmd := exec.Command(bin, args...) // #nosec G204 -- fixed verbs, paths from ids
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + os.Getenv("HOME"), "LANG=C.UTF-8", "GIT_CONFIG_NOSYSTEM=1"}
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", errors.New(strings.TrimSpace(string(out)))
	}
	return strings.TrimSpace(string(out)), nil
}

func dirExists(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.IsDir()
}
