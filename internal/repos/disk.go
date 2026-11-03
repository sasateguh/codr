// Package repos manages bare repositories on disk. Paths are derived from the numeric
// repo id (D27), never from user input, so no name can ever escape the repos directory.
package repos

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// Disk is the on-disk repository root.
type Disk struct {
	Root      string // e.g. <data>/repos
	GitBinary string
}

// Path returns the bare repo directory for an id: <root>/<id>.git.
func (d Disk) Path(id int64) string {
	return filepath.Join(d.Root, strconv.FormatInt(id, 10)+".git")
}

// Create runs `git init --bare` for id with the given initial branch.
func (d Disk) Create(ctx context.Context, id int64, defaultBranch string) error {
	if id <= 0 {
		return errors.New("repos: id must be positive")
	}
	if strings.ContainsAny(defaultBranch, " \t\n~^:?*[\\") || strings.HasPrefix(defaultBranch, "-") || defaultBranch == "" {
		return errors.New("repos: bad branch name")
	}
	if err := os.MkdirAll(d.Root, 0o700); err != nil {
		return err
	}
	p := d.Path(id)
	if _, err := os.Stat(p); err == nil {
		return fmt.Errorf("repos: %s already exists", p)
	}
	cmd := exec.CommandContext(ctx, d.GitBinary, "init", "--bare", "--quiet", "--initial-branch="+defaultBranch, p) // #nosec G204 -- fixed verbs; path from id, branch validated above
	cmd.Env = minimalEnv()
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git init: %w: %s", err, strings.TrimSpace(string(out)))
	}
	// Hooks and config are owned by AMCD (PRD §9): users never upload hooks.
	return os.WriteFile(filepath.Join(p, "description"), []byte("codr repo "+strconv.FormatInt(id, 10)+"\n"), 0o600)
}

// WikiPath is the wiki repository of a repository: <root>/<id>.wiki.git (D83).
func (d Disk) WikiPath(id int64) string {
	return filepath.Join(d.Root, strconv.FormatInt(id, 10)+".wiki.git")
}

// Delete removes the bare repo directory (and its wiki). It refuses anything outside Root.
func (d Disk) Delete(id int64) error {
	p := d.Path(id)
	rel, err := filepath.Rel(d.Root, p)
	if err != nil || strings.HasPrefix(rel, "..") {
		return errors.New("repos: refusing to delete outside root")
	}
	if _, err := os.Stat(filepath.Join(p, "HEAD")); err != nil {
		return fmt.Errorf("repos: %s is not a bare repo: %w", p, err)
	}
	if err := os.RemoveAll(d.WikiPath(id)); err != nil {
		return err
	}
	return os.RemoveAll(p)
}

// Exists reports whether the bare repo for id is on disk.
func (d Disk) Exists(id int64) bool {
	_, err := os.Stat(filepath.Join(d.Path(id), "HEAD"))
	return err == nil
}

// SetDefaultBranch points HEAD at refs/heads/<branch>.
func (d Disk) SetDefaultBranch(ctx context.Context, id int64, branch string) error {
	if strings.ContainsAny(branch, " \t\n~^:?*[\\") || strings.HasPrefix(branch, "-") || branch == "" {
		return errors.New("repos: bad branch name")
	}
	cmd := exec.CommandContext(ctx, d.GitBinary, "-C", d.Path(id), "symbolic-ref", "HEAD", "refs/heads/"+branch) // #nosec G204 -- validated
	cmd.Env = minimalEnv()
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git symbolic-ref: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// minimalEnv gives git only what it needs: no inherited GIT_* overrides from the operator's shell.
func minimalEnv() []string {
	env := []string{"PATH=" + os.Getenv("PATH"), "HOME=" + os.Getenv("HOME"), "LANG=C.UTF-8", "GIT_CONFIG_NOSYSTEM=1"}
	return env
}

// Fork makes dstID a copy of srcID (D75): every branch and tag, objects hard-linked where the filesystem allows
// (git never rewrites an object, so sharing them is safe), no remote left behind, HEAD as in the source.
func (d Disk) Fork(ctx context.Context, srcID, dstID int64) error {
	if srcID <= 0 || dstID <= 0 || srcID == dstID {
		return errors.New("repos: bad fork ids")
	}
	src, dst := d.Path(srcID), d.Path(dstID)
	if _, err := os.Stat(dst); err == nil {
		return fmt.Errorf("repos: %s already exists", dst)
	}
	run := func(args ...string) error {
		cmd := exec.CommandContext(ctx, d.GitBinary, args...) // #nosec G204 -- fixed verbs; paths from ids
		cmd.Env = minimalEnv()
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("git %s: %w: %s", args[0], err, strings.TrimSpace(string(out)))
		}
		return nil
	}
	if err := run("clone", "--bare", "--local", "--quiet", "--", src, dst); err != nil {
		_ = os.RemoveAll(dst)
		return err
	}
	if err := run("-C", dst, "remote", "remove", "origin"); err != nil {
		_ = os.RemoveAll(dst)
		return err
	}
	return os.WriteFile(filepath.Join(dst, "description"), []byte("codr repo "+strconv.FormatInt(dstID, 10)+"\n"), 0o600)
}

// FetchBranch copies branch of srcID into ref of dstID (cross-repository pull requests, D75). The ref is
// replaced each time; nothing else in dst changes.
func (d Disk) FetchBranch(ctx context.Context, srcID int64, branch string, dstID int64, ref string) error {
	if strings.ContainsAny(branch, " \t\n~^:?*[\\") || strings.HasPrefix(branch, "-") || branch == "" || !strings.HasPrefix(ref, "refs/codr/") {
		return errors.New("repos: bad branch or ref")
	}
	cmd := exec.CommandContext(ctx, d.GitBinary, "-C", d.Path(dstID), "fetch", "--quiet", "--no-tags", "--", d.Path(srcID), "+refs/heads/"+branch+":"+ref) // #nosec G204 -- validated
	cmd.Env = minimalEnv()
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git fetch: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// DeleteRef removes one of Codr's own refs (refs/codr/…) from id.
func (d Disk) DeleteRef(ctx context.Context, id int64, ref string) error {
	if !strings.HasPrefix(ref, "refs/codr/") || strings.ContainsAny(ref, " \t\n~^:?*[\\") || strings.Contains(ref, "..") {
		return errors.New("repos: bad ref")
	}
	cmd := exec.CommandContext(ctx, d.GitBinary, "-C", d.Path(id), "update-ref", "-d", ref) // #nosec G204 -- validated
	cmd.Env = minimalEnv()
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git update-ref -d: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}
