package web

import (
	"context"
	"errors"
	"net/http"
	"path"
	"strconv"
	"strings"

	"codr.my/codr/internal/ghimport"
	"codr.my/codr/internal/gitread"
	"codr.my/codr/internal/lfs"
	"codr.my/codr/internal/names"
	"codr.my/codr/internal/store"
)

type repoRow struct {
	store.Repo
	Branch string
	Head   string
	When   string // RFC for title; relative computed in template via Ago
}

type listData struct {
	Owner   string
	Query   string
	Sort    string
	Repos   []repoRow
	Total   int
	Page    int
	Pages   int
	SSHHost string
	Profile *profileData // the owner's year of activity, on /{owner} page 1 only (D62)
	Org     *orgHeader   // /{org}: the organisation header (D74)
	Start   *startData   // first-steps checklist on the home page until every step is done (D64)
	Dash    bool         // signed-in home: activity feed + recent repositories instead of the full list (D68)
	Feed    []activity
	Side    []repoRow
}

// dashRepos is how many recent repositories the dashboard sidebar lists.
const dashRepos = 12

// startData is the getting-started checklist; each step is read from what exists, never stored.
type startData struct {
	Key, Repo, Push bool
}

// uiHome lists what the viewer may see: public repos for a visitor, own + shared + public when logged in.
func (s *Server) uiHome(w http.ResponseWriter, r *http.Request) { s.repoList(w, r, "", nil) }

func (s *Server) uiOwner(w http.ResponseWriter, r *http.Request) {
	owner := r.PathValue("owner")
	if names.User(owner) != nil {
		s.errPage(w, r, http.StatusNotFound)
		return
	}
	u, err := s.Store.UserByName(r.Context(), owner)
	if err != nil {
		s.errPage(w, r, http.StatusNotFound)
		return
	}
	s.repoList(w, r, owner, &u)
}

func (s *Server) repoList(w http.ResponseWriter, r *http.Request, owner string, person *store.User) {
	p := s.newPage(w, r, "")
	var uid int64
	if p.User != nil {
		uid = p.User.ID
	}
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if len(q) > 100 {
		q = q[:100]
	}
	sort := r.URL.Query().Get("isih")
	if sort != "name" {
		sort = "recent"
	}
	size, err := s.Store.SettingInt(r.Context(), "ui.page_size")
	if err != nil || size < 1 {
		size = 50
	}
	pg, _ := strconv.Atoi(r.URL.Query().Get("muka"))
	if pg < 1 {
		pg = 1
	}
	repos, total, err := s.Store.ReposVisible(r.Context(), uid, owner, q, sort, int(size), (pg-1)*int(size))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	d := listData{Owner: owner, Query: q, Sort: sort, Total: total, Page: pg, Pages: (total + int(size) - 1) / int(size), SSHHost: s.UI.SSHHost}
	if owner == "" && p.User != nil && pg == 1 && q == "" {
		d.Start = s.firstSteps(r.Context(), *p.User)
		if r.URL.Query().Get("semua") != "1" {
			d.Dash = true
			d.Feed = s.feed(r.Context(), uid, p.Lang, p.Now)
		}
	}
	if person != nil && person.IsOrg && pg == 1 && q == "" { // an organisation: its own header, no activity calendar
		h := &orgHeader{Name: person.Name}
		if ms, err := s.Store.OrgMembers(r.Context(), person.ID); err == nil {
			h.Members = len(ms)
			for _, m := range ms {
				if p.User != nil && m.UserID == p.User.ID && m.Role == "owner" {
					h.CanManage = true
				}
			}
		}
		if ts, err := s.Store.Teams(r.Context(), person.ID); err == nil {
			h.Teams = len(ts)
		}
		h.CanManage = h.CanManage || (p.User != nil && p.User.IsAdmin)
		d.Org = h
	} else if person != nil && pg == 1 && q == "" {
		d.Profile = s.profile(r.Context(), uid, *person, p.Lang, p.Now)
	}
	for _, rp := range repos {
		row := repoRow{Repo: rp}
		g := s.git(rp.ID)
		if branch, has, err := g.Head(r.Context()); err == nil && has {
			row.Branch = branch
			if c, err := g.Commit(r.Context(), "refs/heads/"+branch); err == nil {
				row.Head = c.Short()
			}
		}
		d.Repos = append(d.Repos, row)
	}
	if d.Dash { // after the rows are built: the sidebar is the first rows of the same list
		d.Side = d.Repos
		if len(d.Side) > dashRepos {
			d.Side = d.Side[:dashRepos]
		}
	}
	if owner != "" {
		p.Title = p.T("ui.repos.of", owner) + " · " + p.SiteName
	} else {
		p.Title = p.T("ui.repos.title") + " · " + p.SiteName
	}
	p.Data = d
	s.render(w, r, http.StatusOK, "repos.html", p)
}

func (s *Server) git(id int64) gitread.Repo {
	return gitread.Repo{Path: s.Disk.Path(id), Git: s.Git.Binary, Timeout: s.UI.GitTimeout}
}

// repoCtx is what every repo page shares.
type repoCtx struct {
	Repo        store.Repo
	Access      store.Access
	Ref         string // requested ref (branch/tag/hash) or default branch
	Branch      string // default branch
	Has         bool   // has commits
	Branches    []gitread.Ref
	Tags        []gitread.Ref
	Tab         string
	SSHURL      string
	HTTPURL     string
	ForkParent  string   // "owner/name" of the source when the viewer may read it (D75)
	ForkHidden  bool     // a fork whose source the viewer cannot read: said without the name
	Forks       int      // how many forks were made from this repository
	CanFork     bool     // signed in, may read, and has somewhere to put it
	CanUpstream bool     // a fork the viewer may push to, whose source they can read: offer a pull request to it
	ForkTo      []string // where a fork may go: the person and the organisations they own
	OpenIssues  int      // for the Issues tab count
	OpenPulls   int      // for the PR tab count
	Watch       string   // the viewer's watch level (D79): all | participating | ignore; "" when signed out
	Viewer      int64    // the signed-in viewer's id, 0 = anonymous (cross-repository links check it, D87)
}

// CanAdmin: the viewer administers this repository (shows the Webhooks tab).
func (rc repoCtx) CanAdmin() bool { return rc.Access >= store.AccessAdmin }

// openRepo authorises and loads the shared repo context; access < Read answers 404.
func (s *Server) openRepo(w http.ResponseWriter, r *http.Request, p page, ref string) (repoCtx, gitread.Repo, bool) {
	owner, name := r.PathValue("owner"), strings.TrimSuffix(r.PathValue("repo"), ".git")
	if names.User(owner) != nil || names.Repo(name) != nil {
		s.errPage(w, r, http.StatusNotFound)
		return repoCtx{}, gitread.Repo{}, false
	}
	var uid int64
	if p.User != nil {
		uid = p.User.ID
	}
	repo, err := s.Store.RepoByPath(r.Context(), owner, name)
	access := store.AccessNone
	if err == nil {
		access, err = s.Store.AccessFor(r.Context(), uid, repo)
	}
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		s.fail(w, r, err)
		return repoCtx{}, gitread.Repo{}, false
	}
	if access < store.AccessRead {
		s.errPage(w, r, http.StatusNotFound)
		return repoCtx{}, gitread.Repo{}, false
	}
	g := s.git(repo.ID)
	rc := repoCtx{Repo: repo, Access: access, SSHURL: "ssh://git@" + s.UI.SSHHost + "/" + owner + "/" + name + ".git",
		HTTPURL: s.UI.HTTPHost + "/" + owner + "/" + name + ".git"}
	if p.User != nil {
		rc.Viewer = p.User.ID
		rc.Watch, _ = s.Store.WatchLevel(r.Context(), p.User.ID, repo)
	}
	rc.Branch, rc.Has, err = g.Head(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return rc, g, false
	}
	s.forkInfo(r, p, &rc)
	rc.OpenIssues, _, _ = s.Store.IssueCounts(r.Context(), repo.ID)
	rc.OpenPulls, _, _ = s.Store.PullCounts(r.Context(), repo.ID)
	if rc.Has {
		rc.Branches, _ = g.Branches(r.Context())
		rc.Tags, _ = g.Tags(r.Context())
	}
	rc.Ref = ref
	if rc.Ref == "" {
		rc.Ref = rc.Branch
	}
	if !gitread.ValidRef(rc.Ref) {
		s.errPage(w, r, http.StatusNotFound)
		return rc, g, false
	}
	return rc, g, true
}

type treeData struct {
	repoCtx
	Path      string
	Crumbs    []crumb
	Entries   []gitread.Entry
	Last      map[string]*gitread.Commit // newest commit per entry name; missing = beyond the walk window
	Latest    *gitread.Commit
	Commits   int
	README    string
	ReadmeOK  bool
	Readme    any // template.HTML
	Removed   bool
	Root      bool
	Mirror    *mirrorInfo
	About     *aboutData // languages, size, licence (repo home only, D67)
	CanImport bool       // admin on a github.com mirror: offer the issue/PR import (D72)
}

func (s *Server) uiRepo(w http.ResponseWriter, r *http.Request) {
	if strings.HasSuffix(r.PathValue("repo"), ".git") {
		http.Redirect(w, r, "/"+r.PathValue("owner")+"/"+strings.TrimSuffix(r.PathValue("repo"), ".git"), http.StatusMovedPermanently)
		return
	}
	s.treePage(w, r, "", "")
}

func (s *Server) uiTree(w http.ResponseWriter, r *http.Request) {
	s.treePage(w, r, r.PathValue("ref"), r.PathValue("path"))
}

func (s *Server) treePage(w http.ResponseWriter, r *http.Request, ref, p string) {
	pg := s.newPage(w, r, "")
	rc, g, ok := s.openRepo(w, r, pg, ref)
	if !ok {
		return
	}
	cp, good := gitread.CleanPath(strings.TrimSuffix(p, "/"))
	if !good {
		s.errPage(w, r, http.StatusNotFound)
		return
	}
	rc.Tab = "code"
	d := treeData{repoCtx: rc, Path: cp, Crumbs: crumbs(cp), Root: cp == "", Mirror: s.mirrorInfo(r.Context(), rc.Repo)}
	if pg.User != nil && pg.User.IsAdmin && rc.Repo.Mirror && cp == "" {
		if m, err := s.Store.MirrorOf(r.Context(), rc.Repo.ID); err == nil {
			if o, _ := ghimport.ParseGitHubURL(m.URL); o != "" {
				d.CanImport = true
			}
		}
	}
	pg.Title = rc.Repo.Owner + "/" + rc.Repo.Name + " · " + pg.SiteName
	if rc.Has {
		entries, err := g.Tree(r.Context(), rc.Ref, cp)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		d.Entries = entries
		names := make([]string, len(entries))
		for i, e := range entries {
			names[i] = e.Name
		}
		window, _ := s.Store.SettingInt(r.Context(), "ui.tree_history_commits")
		if last, err := g.LastCommits(r.Context(), rc.Ref, cp, names, int(window)); err == nil {
			d.Last = make(map[string]*gitread.Commit, len(last))
			for n, c := range last {
				d.Last[n] = &c
			}
		}
		if c, err := g.Commit(r.Context(), rc.Ref); err == nil {
			d.Latest = &c
		}
		if cp == "" {
			d.About = s.about(r.Context(), g, rc.Ref, entries)
			d.Commits, _ = g.Count(r.Context(), rc.Ref)
			if name, found := g.README(r.Context(), rc.Ref); found {
				max, _ := s.Store.SettingInt(r.Context(), "ui.blob_max_kb")
				if b, err := g.Blob(r.Context(), rc.Ref, name, max<<10); err == nil && !b.Binary {
					d.README = name
					if strings.HasSuffix(strings.ToLower(name), ".md") {
						if h, removed, err := renderMarkdown(b.Content, &mdBase{Owner: rc.Repo.Owner, Repo: rc.Repo.Name, Ref: rc.Ref}); err == nil {
							d.Readme, d.Removed, d.ReadmeOK = h, removed, true
						}
					} else {
						d.Readme, d.ReadmeOK = string(b.Content), true
					}
				}
			}
		}
	}
	s.takeFlash(w, r, &pg)
	pg.Data = d
	s.render(w, r, http.StatusOK, "tree.html", pg)
}

type blobData struct {
	repoCtx
	Path       string
	Crumbs     []crumb
	Name       string
	Blob       gitread.Blob
	Lines      []codeLine
	MaxShown   string
	TooBig     bool
	Markdown   any
	Removed    bool
	IsMarkdown bool
	LFS        *lfs.Pointer // the file is a Git LFS pointer (D82)
	LFSStored  bool         // and its object is on this server
}

func (s *Server) uiBlob(w http.ResponseWriter, r *http.Request) {
	pg := s.newPage(w, r, "")
	rc, g, ok := s.openRepo(w, r, pg, r.PathValue("ref"))
	if !ok {
		return
	}
	cp, good := gitread.CleanPath(r.PathValue("path"))
	if !good || cp == "" {
		s.errPage(w, r, http.StatusNotFound)
		return
	}
	maxKB, _ := s.Store.SettingInt(r.Context(), "ui.blob_max_kb")
	b, err := g.Blob(r.Context(), rc.Ref, cp, maxKB<<10)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	rc.Tab = "code"
	d := blobData{repoCtx: rc, Path: cp, Crumbs: crumbs(cp), Name: path.Base(cp), Blob: b, MaxShown: humanSize(maxKB << 10), TooBig: b.Truncated}
	if ptr, ok := lfs.ParsePointer(b.Content); ok && !b.Truncated {
		d.LFS = &ptr
		if s.LFS != nil {
			_, d.LFSStored = s.LFS.Stat(rc.Repo.ID, ptr.OID)
		}
		pg.Data = d
		s.render(w, r, http.StatusOK, "blob.html", pg)
		return
	}
	if !b.Binary && !b.Truncated {
		if strings.HasSuffix(strings.ToLower(cp), ".md") && r.URL.Query().Get("papar") != "kod" {
			if h, removed, err := renderMarkdown(b.Content, &mdBase{Owner: rc.Repo.Owner, Repo: rc.Repo.Name, Ref: rc.Ref, Dir: strings.TrimPrefix(path.Dir(cp), ".")}); err == nil {
				d.Markdown, d.Removed, d.IsMarkdown = h, removed, true
			}
		}
		if !d.IsMarkdown {
			d.Lines = highlight(d.Name, string(b.Content))
		}
	}
	pg.Title = cp + " · " + rc.Repo.Owner + "/" + rc.Repo.Name + " · " + pg.SiteName
	pg.Data = d
	s.render(w, r, http.StatusOK, "blob.html", pg)
}

// uiRaw serves file bytes: text as text/plain, everything else as a download. Never HTML.
func (s *Server) uiRaw(w http.ResponseWriter, r *http.Request) {
	pg := s.newPage(w, r, "")
	rc, g, ok := s.openRepo(w, r, pg, r.PathValue("ref"))
	if !ok {
		return
	}
	cp, good := gitread.CleanPath(r.PathValue("path"))
	if !good || cp == "" {
		s.errPage(w, r, http.StatusNotFound)
		return
	}
	b, err := g.Blob(r.Context(), rc.Ref, cp, maxOutputBytes)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	name := path.Base(cp)
	if ptr, ok := lfs.ParsePointer(b.Content); ok && !b.Truncated && s.LFS != nil {
		if _, stored := s.LFS.Stat(rc.Repo.ID, ptr.OID); stored {
			s.serveLFSObject(w, r, rc.Repo.ID, ptr.OID, name) // the real content, not the pointer (D82)
			return
		}
	}
	if b.Binary || b.Truncated {
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Disposition", "attachment; filename=\""+strings.ReplaceAll(name, "\"", "")+"\"")
	} else {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("Content-Disposition", "inline; filename=\""+strings.ReplaceAll(name, "\"", "")+"\"")
	}
	w.Header().Set("Content-Length", strconv.Itoa(len(b.Content)))
	_, _ = w.Write(b.Content)
}

const maxOutputBytes = 64 << 20

// firstSteps reports the getting-started checklist for u, or nil once every step is done (D64).
func (s *Server) firstSteps(ctx context.Context, u store.User) *startData {
	var st startData
	if keys, err := s.Store.SSHKeys(ctx, u.ID); err == nil && len(keys) > 0 {
		st.Key = true
	} else if toks, err := s.Store.Tokens(ctx, u.ID); err == nil && len(toks) > 0 {
		st.Key = true
	}
	if _, n, err := s.Store.ReposVisible(ctx, u.ID, u.Name, "", "recent", 1, 0); err == nil && n > 0 {
		st.Repo = true
	}
	if rows, err := s.Store.ActivityOf(ctx, u.ID, "", []string{"git.push"}, 1); err == nil && len(rows) > 0 {
		st.Push = true
	}
	if st.Key && st.Repo && st.Push {
		return nil
	}
	return &st
}
