// Package wiki keeps a repository's wiki as its own bare git repository (<id>.wiki.git, D83): one Markdown file per
// page at the root of branch main. The web writes pages with git plumbing (hash-object, mktree, commit-tree and a
// compare-and-swap update-ref), so there is no working tree and two editors cannot overwrite each other silently.
// It runs the git binary (D2), never parses git objects itself.
package wiki

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// Errors the pages map to messages.
var (
	ErrNotFound = errors.New("wiki: no such page")
	ErrConflict = errors.New("wiki: the page changed since it was opened")
	ErrBadName  = errors.New("wiki: bad page name")
)

const (
	branch = "refs/heads/main"
	zero   = "0000000000000000000000000000000000000000"
	// Home is the page shown at /wiki.
	Home = "Home"
)

// Wiki is one wiki repository.
type Wiki struct {
	Path    string // the bare repository
	Git     string
	Timeout time.Duration
}

// Page is one page of the current wiki.
type Page struct {
	Name  string // slug, the file name without .md
	Title string
	Size  int64
}

// Rev is one commit touching a page.
type Rev struct {
	Hash, Author, Email, Subject string
	When                         time.Time
}

// Author is who a save is recorded as.
type Author struct{ Name, Email string }

func (w Wiki) git(ctx context.Context, stdin []byte, env []string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, w.Timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, w.Git, append([]string{"-C", w.Path}, args...)...) // #nosec G204 -- fixed git binary; args built here, names validated
	cmd.Env = append([]string{"PATH=" + os.Getenv("PATH"), "HOME=" + os.Getenv("HOME"), "LANG=C.UTF-8", "GIT_CONFIG_NOSYSTEM=1"}, env...)
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		return out.Bytes(), errors.New("git " + args[0] + ": " + strings.TrimSpace(errb.String()))
	}
	return out.Bytes(), nil
}

// Exists reports whether the wiki repository has been created.
func (w Wiki) Exists() bool {
	fi, err := os.Stat(w.Path)
	return err == nil && fi.IsDir()
}

func (w Wiki) ensure(ctx context.Context) error {
	if w.Exists() {
		return nil
	}
	if err := os.MkdirAll(w.Path, 0o700); err != nil {
		return err
	}
	_, err := w.git(ctx, nil, nil, "init", "--quiet", "--bare", "--initial-branch=main")
	return err
}

// Head is the current commit of the wiki ("" when it has no pages yet).
func (w Wiki) Head(ctx context.Context) (string, error) {
	if !w.Exists() {
		return "", nil
	}
	out, err := w.git(ctx, nil, nil, "rev-parse", "--verify", "-q", branch)
	if err != nil {
		return "", nil // no commit yet
	}
	return strings.TrimSpace(string(out)), nil
}

type entry struct{ blob, name string }

func (w Wiki) tree(ctx context.Context, commit string) (map[string]entry, error) {
	out := map[string]entry{}
	if commit == "" {
		return out, nil
	}
	raw, err := w.git(ctx, nil, nil, "ls-tree", "-z", commit)
	if err != nil {
		return nil, err
	}
	for _, rec := range bytes.Split(raw, []byte{0}) {
		meta, name, ok := bytes.Cut(rec, []byte{'\t'})
		f := strings.Fields(string(meta))
		if !ok || len(f) != 3 || f[1] != "blob" {
			continue
		}
		out[string(name)] = entry{blob: f[2], name: string(name)}
	}
	return out, nil
}

// Pages lists the pages at a commit ("" = current), by title.
func (w Wiki) Pages(ctx context.Context) ([]Page, error) {
	head, err := w.Head(ctx)
	if err != nil || head == "" {
		return nil, err
	}
	raw, err := w.git(ctx, nil, nil, "ls-tree", "-z", "-l", head)
	if err != nil {
		return nil, err
	}
	var out []Page
	for _, rec := range bytes.Split(raw, []byte{0}) {
		meta, name, ok := bytes.Cut(rec, []byte{'\t'})
		f := strings.Fields(string(meta))
		n, isMD := strings.CutSuffix(string(name), ".md")
		if !ok || len(f) != 4 || f[1] != "blob" || !isMD {
			continue
		}
		size, _ := strconv.ParseInt(f[3], 10, 64)
		out = append(out, Page{Name: n, Title: TitleOf(n), Size: size})
	}
	sort.Slice(out, func(i, j int) bool {
		if (out[i].Name == Home) != (out[j].Name == Home) {
			return out[i].Name == Home
		}
		return strings.ToLower(out[i].Title) < strings.ToLower(out[j].Title)
	})
	return out, nil
}

// Read returns a page's content at a commit (rev "" = current) and the commit it was read at.
func (w Wiki) Read(ctx context.Context, name, rev string) ([]byte, string, error) {
	if !ValidName(name) {
		return nil, "", ErrBadName
	}
	at := rev
	if at == "" {
		var err error
		if at, err = w.Head(ctx); err != nil || at == "" {
			return nil, "", ErrNotFound
		}
	} else if !isHex40(at) {
		return nil, "", ErrNotFound
	}
	b, err := w.git(ctx, nil, nil, "cat-file", "blob", at+":"+name+".md")
	if err != nil {
		return nil, "", ErrNotFound
	}
	return b, at, nil
}

// Save writes a page. base is the commit the editor started from ("" for a new page); when the wiki moved since,
// the save still goes through if this page did not change in between, otherwise ErrConflict. oldName renames.
func (w Wiki) Save(ctx context.Context, name, oldName string, content []byte, who Author, message, base string) (string, error) {
	if !ValidName(name) || (oldName != "" && !ValidName(oldName)) {
		return "", ErrBadName
	}
	if err := w.ensure(ctx); err != nil {
		return "", err
	}
	return w.write(ctx, who, message, base, func(t map[string]entry, baseTree map[string]entry) error {
		probe := name
		if oldName != "" {
			probe = oldName
		}
		if base != "" && t[probe+".md"] != baseTree[probe+".md"] {
			return ErrConflict
		}
		if oldName == "" && base == "" {
			if _, exists := t[name+".md"]; exists {
				return ErrConflict // a new page whose name was taken meanwhile
			}
		}
		if oldName != "" && oldName != name {
			if _, exists := t[name+".md"]; exists {
				return ErrConflict
			}
			delete(t, oldName+".md")
		}
		out, err := w.git(ctx, content, nil, "hash-object", "-w", "--stdin")
		if err != nil {
			return err
		}
		t[name+".md"] = entry{blob: strings.TrimSpace(string(out)), name: name + ".md"}
		return nil
	})
}

// Delete removes a page.
func (w Wiki) Delete(ctx context.Context, name string, who Author, message, base string) (string, error) {
	if !ValidName(name) {
		return "", ErrBadName
	}
	return w.write(ctx, who, message, base, func(t, baseTree map[string]entry) error {
		if _, ok := t[name+".md"]; !ok {
			return ErrNotFound
		}
		if base != "" && t[name+".md"] != baseTree[name+".md"] {
			return ErrConflict
		}
		delete(t, name+".md")
		return nil
	})
}

func (w Wiki) write(ctx context.Context, who Author, message, base string, edit func(t, baseTree map[string]entry) error) (string, error) {
	cur, err := w.Head(ctx)
	if err != nil {
		return "", err
	}
	if base != "" && !isHex40(base) {
		return "", ErrConflict
	}
	t, err := w.tree(ctx, cur)
	if err != nil {
		return "", err
	}
	baseTree := t
	if base != "" && base != cur {
		if baseTree, err = w.tree(ctx, base); err != nil {
			return "", ErrConflict
		}
	}
	if err := edit(t, baseTree); err != nil {
		return "", err
	}
	names := make([]string, 0, len(t))
	for n := range t {
		names = append(names, n)
	}
	sort.Strings(names)
	var mk bytes.Buffer
	for _, n := range names {
		mk.WriteString("100644 blob " + t[n].blob + "\t" + n + "\x00")
	}
	treeOut, err := w.git(ctx, mk.Bytes(), nil, "mktree", "-z")
	if err != nil {
		return "", err
	}
	env := []string{"GIT_AUTHOR_NAME=" + who.Name, "GIT_AUTHOR_EMAIL=" + who.Email, "GIT_COMMITTER_NAME=" + who.Name, "GIT_COMMITTER_EMAIL=" + who.Email}
	args := []string{"commit-tree", strings.TrimSpace(string(treeOut)), "-m", message}
	if cur != "" {
		args = append(args, "-p", cur)
	}
	commitOut, err := w.git(ctx, nil, env, args...)
	if err != nil {
		return "", err
	}
	commit := strings.TrimSpace(string(commitOut))
	old := cur
	if old == "" {
		old = zero
	}
	// compare-and-swap: if anyone moved the branch since Head, this fails instead of losing their page
	if _, err := w.git(ctx, nil, nil, "update-ref", branch, commit, old); err != nil {
		return "", ErrConflict
	}
	return commit, nil
}

// History lists the commits that touched a page, newest first.
func (w Wiki) History(ctx context.Context, name string, n int) ([]Rev, error) {
	if !ValidName(name) {
		return nil, ErrBadName
	}
	head, err := w.Head(ctx)
	if err != nil || head == "" {
		return nil, err
	}
	out, err := w.git(ctx, nil, nil, "log", "--max-count="+strconv.Itoa(n), "--format=%H%x00%an%x00%ae%x00%at%x00%s%x1e", head, "--", name+".md")
	if err != nil {
		return nil, err
	}
	var revs []Rev
	for _, rec := range strings.Split(string(out), "\x1e") {
		f := strings.Split(strings.TrimSpace(rec), "\x00")
		if len(f) != 5 {
			continue
		}
		sec, _ := strconv.ParseInt(f[3], 10, 64)
		revs = append(revs, Rev{Hash: f[0], Author: f[1], Email: f[2], When: time.Unix(sec, 0).UTC(), Subject: f[4]})
	}
	return revs, nil
}

// Slug turns a title into a page name: letters and digits of any script, spaces become '-', at most 100 characters.
func Slug(title string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.TrimSpace(title) {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_':
			b.WriteRune(r)
			dash = false
		case !dash && b.Len() > 0:
			b.WriteRune('-')
			dash = true
		}
		if utf8.RuneCountInString(b.String()) >= 100 {
			break
		}
	}
	return strings.Trim(b.String(), "-_")
}

// ValidName reports whether a page name is one Slug can produce (and so is safe as a file name and a URL segment).
func ValidName(n string) bool { return n != "" && Slug(n) == n }

// TitleOf is the title shown for a page name.
func TitleOf(name string) string { return strings.ReplaceAll(name, "-", " ") }

func isHex40(s string) bool {
	if len(s) != 40 {
		return false
	}
	for _, c := range s {
		if !strings.ContainsRune("0123456789abcdef", c) {
			return false
		}
	}
	return true
}
