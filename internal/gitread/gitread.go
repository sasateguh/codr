// Package gitread answers the web UI's read-only questions about a bare repository by
// running the git binary (D2). Every argument that comes from a URL is validated here and
// passed after `--end-of-options` / `--`, so a ref or path can never become a flag.
package gitread

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path"
	"strconv"
	"strings"
	"time"
)

// Repo is one bare repository on disk.
type Repo struct {
	Path    string
	Git     string
	Timeout time.Duration
}

// Errors the UI maps to pages.
var (
	ErrBadRef   = errors.New("gitread: bad ref")
	ErrBadPath  = errors.New("gitread: bad path")
	ErrNotFound = errors.New("gitread: not found")
	ErrEmpty    = errors.New("gitread: repository has no commits")
)

// Ref is a branch or tag.
type Ref struct {
	Name string
	Hash string
	When time.Time
}

// Commit is the metadata of one commit.
type Commit struct {
	Hash      string
	Parents   []string
	Author    string
	Email     string
	When      time.Time
	Committer string
	Subject   string
	Body      string
}

// Short is the 7-character hash.
func (c Commit) Short() string { return short(c.Hash) }

// Entry is one row of a tree listing.
type Entry struct {
	Mode string
	Type string // blob | tree | commit (submodule)
	Hash string
	Name string
	Size int64 // -1 for trees
}

// IsDir reports whether the entry is a tree.
func (e Entry) IsDir() bool { return e.Type == "tree" }

// Blob is file content with its limits applied.
type Blob struct {
	Size      int64
	Content   []byte
	Binary    bool
	Truncated bool
}

const maxOutput = 64 << 20 // bound on any single git output read into memory

func (r Repo) run(ctx context.Context, stdin []byte, args ...string) ([]byte, error) {
	out, msg, code, err := r.exec(ctx, stdin, args...)
	if err != nil {
		return nil, err
	}
	if code != 0 {
		switch {
		case strings.Contains(msg, "unknown revision"), strings.Contains(msg, "Not a valid object name"),
			strings.Contains(msg, "does not exist"), strings.Contains(msg, "bad revision"), strings.Contains(msg, "fatal: path"),
			strings.Contains(msg, "Needed a single revision"), strings.Contains(msg, "exists on disk, but not in"):
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("git %s: exit status %d: %s", args[0], code, msg)
	}
	return out, nil
}

// exec runs git and returns stdout, trimmed stderr and the exit code; err is only set when git
// could not run at all (missing binary, timeout). Callers that read output on a non-zero exit
// (merge-tree reports conflicts with status 1) use this directly; everything else uses run.
func (r Repo) exec(ctx context.Context, stdin []byte, args ...string) (out []byte, stderr string, code int, err error) {
	return r.execEnv(ctx, nil, stdin, args...)
}

// execEnv is exec with extra environment entries (commit identities: GIT_AUTHOR_*, GIT_COMMITTER_*).
func (r Repo) execEnv(ctx context.Context, env []string, stdin []byte, args ...string) (out []byte, stderr string, code int, err error) {
	ctx, cancel := context.WithTimeout(ctx, r.Timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, r.Git, append([]string{"-C", r.Path}, args...)...) // #nosec G204 -- fixed verbs; user values validated and after -- / --end-of-options
	cmd.Env = append([]string{"PATH=" + os.Getenv("PATH"), "HOME=" + os.Getenv("HOME"), "LANG=C.UTF-8", "GIT_CONFIG_NOSYSTEM=1", "TZ=UTC"}, env...)
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	var ob, eb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &limitWriter{w: &ob, n: maxOutput}, &eb
	runErr := cmd.Run()
	stderr = strings.TrimSpace(eb.String())
	if runErr != nil {
		var ee *exec.ExitError
		if errors.As(runErr, &ee) && ctx.Err() == nil {
			return ob.Bytes(), stderr, ee.ExitCode(), nil
		}
		return nil, stderr, -1, fmt.Errorf("git %s: %w: %s", args[0], runErr, stderr)
	}
	return ob.Bytes(), stderr, 0, nil
}

// ValidRef accepts branch/tag names and hex hashes; anything that could be an option or an
// escape is refused before git sees it.
func ValidRef(ref string) bool {
	if ref == "" || len(ref) > 256 || strings.HasPrefix(ref, "-") || strings.Contains(ref, "..") ||
		strings.HasPrefix(ref, "/") || strings.HasSuffix(ref, "/") || strings.HasSuffix(ref, ".lock") {
		return false
	}
	for _, c := range ref {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '.' || c == '_' || c == '-' || c == '/' || c == '+') {
			return false
		}
	}
	return true
}

// CleanPath normalises a tree path from a URL: no leading slash, no "." or ".." parts.
func CleanPath(p string) (string, bool) {
	if p == "" {
		return "", true
	}
	if strings.HasPrefix(p, "/") || strings.ContainsAny(p, "\x00\n") || len(p) > 4096 {
		return "", false
	}
	for _, part := range strings.Split(p, "/") {
		if part == "" || part == "." || part == ".." || strings.HasPrefix(part, "-") && len(part) > 0 && path.Clean(p) != p {
			return "", false
		}
	}
	c := path.Clean(p)
	if c != p || strings.HasPrefix(c, "../") {
		return "", false
	}
	return c, true
}

// Head returns the default branch name and whether it has commits.
func (r Repo) Head(ctx context.Context) (branch string, hasCommits bool, err error) {
	out, err := r.run(ctx, nil, "symbolic-ref", "--short", "HEAD")
	if err != nil {
		return "", false, err
	}
	branch = strings.TrimSpace(string(out))
	if _, err := r.run(ctx, nil, "rev-parse", "--verify", "-q", "--end-of-options", "refs/heads/"+branch+"^{commit}"); err != nil {
		if errors.Is(err, ErrNotFound) || strings.Contains(err.Error(), "exit status 1") {
			return branch, false, nil
		}
		return branch, false, err
	}
	return branch, true, nil
}

// Resolve turns a ref into a commit hash.
func (r Repo) Resolve(ctx context.Context, ref string) (string, error) {
	if !ValidRef(ref) {
		return "", ErrBadRef
	}
	out, err := r.run(ctx, nil, "rev-parse", "--verify", "-q", "--end-of-options", ref+"^{commit}")
	if err != nil {
		if strings.Contains(err.Error(), "exit status 1") {
			return "", ErrNotFound
		}
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

func (r Repo) refs(ctx context.Context, prefix string) ([]Ref, error) {
	out, err := r.run(ctx, nil, "for-each-ref", "--sort=-creatordate", "--format=%(objectname)%00%(refname:short)%00%(creatordate:unix)", prefix)
	if err != nil {
		return nil, err
	}
	var refs []Ref
	for _, line := range strings.Split(strings.TrimRight(string(out), "\n"), "\n") {
		f := strings.Split(line, "\x00")
		if len(f) != 3 {
			continue
		}
		ts, _ := strconv.ParseInt(f[2], 10, 64)
		refs = append(refs, Ref{Hash: f[0], Name: f[1], When: time.Unix(ts, 0).UTC()})
	}
	return refs, nil
}

// Branches lists branches, most recently committed first.
func (r Repo) Branches(ctx context.Context) ([]Ref, error) { return r.refs(ctx, "refs/heads/") }

// Tags lists tags, newest first.
func (r Repo) Tags(ctx context.Context) ([]Ref, error) { return r.refs(ctx, "refs/tags/") }

const logFormat = "%H%x00%P%x00%an%x00%ae%x00%at%x00%cn%x00%s%x00%b%x01"

func parseCommits(out []byte) []Commit {
	var cs []Commit
	for _, rec := range bytes.Split(out, []byte{1}) {
		rec = bytes.TrimLeft(rec, "\n")
		if len(rec) == 0 {
			continue
		}
		f := strings.SplitN(string(rec), "\x00", 8)
		if len(f) != 8 {
			continue
		}
		ts, _ := strconv.ParseInt(f[4], 10, 64)
		c := Commit{Hash: f[0], Author: f[2], Email: f[3], When: time.Unix(ts, 0).UTC(), Committer: f[5], Subject: f[6], Body: strings.TrimRight(f[7], "\n")}
		if f[1] != "" {
			c.Parents = strings.Fields(f[1])
		}
		cs = append(cs, c)
	}
	return cs
}

// Log returns n commits reachable from ref (optionally touching path), skipping skip.
func (r Repo) Log(ctx context.Context, ref, p string, n, skip int) ([]Commit, error) {
	if !ValidRef(ref) {
		return nil, ErrBadRef
	}
	cp, ok := CleanPath(p)
	if !ok {
		return nil, ErrBadPath
	}
	args := []string{"log", "--format=" + logFormat, "-n", strconv.Itoa(n), "--skip=" + strconv.Itoa(skip), "--end-of-options", ref}
	if cp != "" {
		args = append(args, "--", cp)
	}
	out, err := r.run(ctx, nil, args...)
	if err != nil {
		return nil, err
	}
	return parseCommits(out), nil
}

// Commit fetches one commit's metadata.
func (r Repo) Commit(ctx context.Context, ref string) (Commit, error) {
	cs, err := r.Log(ctx, ref, "", 1, 0)
	if err != nil {
		return Commit{}, err
	}
	if len(cs) == 0 {
		return Commit{}, ErrNotFound
	}
	return cs[0], nil
}

// Count returns the number of commits reachable from ref.
func (r Repo) Count(ctx context.Context, ref string) (int, error) {
	if !ValidRef(ref) {
		return 0, ErrBadRef
	}
	out, err := r.run(ctx, nil, "rev-list", "--count", "--end-of-options", ref)
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(strings.TrimSpace(string(out)))
}

// Tree lists a directory at ref: trees first, then blobs, each group by name.
func (r Repo) Tree(ctx context.Context, ref, p string) ([]Entry, error) {
	if !ValidRef(ref) {
		return nil, ErrBadRef
	}
	cp, ok := CleanPath(p)
	if !ok {
		return nil, ErrBadPath
	}
	spec := ref
	if cp != "" {
		spec = ref + ":" + cp
	}
	out, err := r.run(ctx, nil, "ls-tree", "-l", "-z", "--end-of-options", spec)
	if err != nil {
		return nil, err
	}
	var dirs, files []Entry
	for _, rec := range bytes.Split(out, []byte{0}) {
		if len(rec) == 0 {
			continue
		}
		meta, name, ok := strings.Cut(string(rec), "\t")
		if !ok {
			continue
		}
		f := strings.Fields(meta)
		if len(f) != 4 {
			continue
		}
		e := Entry{Mode: f[0], Type: f[1], Hash: f[2], Name: name, Size: -1}
		if f[3] != "-" {
			e.Size, _ = strconv.ParseInt(f[3], 10, 64)
		}
		if e.IsDir() {
			dirs = append(dirs, e)
		} else {
			files = append(files, e)
		}
	}
	return append(dirs, files...), nil
}

// Blob reads a file at ref, keeping at most max bytes. Binary = NUL in the first 8 KiB.
func (r Repo) Blob(ctx context.Context, ref, p string, max int64) (Blob, error) {
	if !ValidRef(ref) {
		return Blob{}, ErrBadRef
	}
	cp, ok := CleanPath(p)
	if !ok || cp == "" {
		return Blob{}, ErrBadPath
	}
	spec := ref + ":" + cp
	sz, err := r.run(ctx, nil, "cat-file", "-s", "--end-of-options", spec)
	if err != nil {
		return Blob{}, err
	}
	b := Blob{}
	b.Size, _ = strconv.ParseInt(strings.TrimSpace(string(sz)), 10, 64)
	typ, err := r.run(ctx, nil, "cat-file", "-t", "--end-of-options", spec)
	if err != nil {
		return Blob{}, err
	}
	if strings.TrimSpace(string(typ)) != "blob" {
		return Blob{}, ErrNotFound
	}
	if b.Size > max {
		b.Truncated = true
	}
	out, err := r.run(ctx, nil, "cat-file", "blob", "--end-of-options", spec)
	if err != nil {
		return Blob{}, err
	}
	head := out
	if len(head) > 8192 {
		head = head[:8192]
	}
	b.Binary = bytes.IndexByte(head, 0) >= 0
	if int64(len(out)) > max {
		out = out[:max]
		b.Truncated = true
	}
	b.Content = out
	return b, nil
}

// README finds the root README at ref (case-insensitive, .md preferred).
func (r Repo) README(ctx context.Context, ref string) (string, bool) {
	entries, err := r.Tree(ctx, ref, "")
	if err != nil {
		return "", false
	}
	best := ""
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		n := strings.ToLower(e.Name)
		if n == "readme.md" {
			return e.Name, true
		}
		if n == "readme" || n == "readme.txt" || strings.HasPrefix(n, "readme.") {
			if best == "" {
				best = e.Name
			}
		}
	}
	return best, best != ""
}

func short(h string) string {
	if len(h) > 7 {
		return h[:7]
	}
	return h
}

type limitWriter struct {
	w *bytes.Buffer
	n int
}

func (l *limitWriter) Write(p []byte) (int, error) {
	if l.w.Len()+len(p) > l.n {
		return 0, errors.New("gitread: output too large")
	}
	return l.w.Write(p)
}
