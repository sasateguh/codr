package web

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"

	"codr.my/codr/internal/gitread"
	"codr.my/codr/internal/store"
)

// Forks (D75). Anyone who can read a repository may copy it into their own namespace or an organisation they
// own. A private source gives a private fork: forking must never be a way to publish code someone kept private.

// forkInfo fills the fork line and button of the repository header.
func (s *Server) forkInfo(r *http.Request, p page, rc *repoCtx) {
	ctx := r.Context()
	if rc.Repo.ForkOf > 0 {
		if src, err := s.Store.RepoByID(ctx, rc.Repo.ForkOf); err == nil {
			var uid int64
			if p.User != nil {
				uid = p.User.ID
			}
			if a, err := s.Store.AccessFor(ctx, uid, src); err == nil && a >= store.AccessRead {
				rc.ForkParent = src.Owner + "/" + src.Name
				rc.CanUpstream = uid > 0 && rc.Access >= store.AccessWrite && !src.Archived && !src.Mirror
			} else {
				rc.ForkHidden = true
			}
		}
	}
	rc.Forks, _ = s.Store.ForkCount(ctx, rc.Repo.ID)
	if p.User != nil && rc.Access >= store.AccessRead {
		// never offer the repository's own namespace: the copy would collide with the repository itself
		for _, o := range s.ownersFor(ctx, *p.User) {
			if o != rc.Repo.Owner {
				rc.ForkTo = append(rc.ForkTo, o)
			}
		}
		rc.CanFork = len(rc.ForkTo) > 0
	}
}

func (s *Server) uiFork(w http.ResponseWriter, r *http.Request) {
	p := s.newPage(w, r, "")
	rc, _, ok := s.openRepo(w, r, p, "")
	if !ok {
		return
	}
	if p.User == nil {
		http.Redirect(w, r, "/login?next=/"+rc.Repo.Owner+"/"+rc.Repo.Name, http.StatusSeeOther)
		return
	}
	if !s.checkCSRF(r) || !rc.CanFork {
		s.errPage(w, r, http.StatusForbidden)
		return
	}
	ctx := r.Context()
	to := r.PostFormValue("owner")
	if to == "" {
		to = p.User.Name
	}
	allowed := false
	for _, o := range rc.ForkTo {
		allowed = allowed || o == to
	}
	if !allowed { // checked on the server: the form only lists what is allowed
		s.errPage(w, r, http.StatusForbidden)
		return
	}
	ownerID := p.User.ID
	if to != p.User.Name {
		org, err := s.Store.OrgByName(ctx, to)
		if err != nil {
			s.errPage(w, r, http.StatusForbidden)
			return
		}
		ownerID = org.ID
	}
	back := "/" + rc.Repo.Owner + "/" + rc.Repo.Name
	if existing, err := s.Store.RepoByPath(ctx, to, rc.Repo.Name); err == nil {
		if existing.ForkOf == rc.Repo.ID {
			http.Redirect(w, r, "/"+to+"/"+rc.Repo.Name, http.StatusSeeOther) // already forked: go there
			return
		}
		s.setFlash(w, "ralat", p.T("ui.fork.name_taken", to+"/"+rc.Repo.Name))
		http.Redirect(w, r, back, http.StatusSeeOther)
		return
	} else if !errors.Is(err, store.ErrNotFound) {
		s.fail(w, r, err)
		return
	}
	private := rc.Repo.Private || r.PostFormValue("visibility") == "private"
	fork, err := s.Store.CreateRepo(ctx, ownerID, rc.Repo.Name, rc.Repo.Description, private, rc.Repo.DefaultBranch)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if err := s.Disk.Fork(ctx, rc.Repo.ID, fork.ID); err != nil {
		_ = s.Store.DeleteRepo(ctx, fork.ID)
		s.fail(w, r, err)
		return
	}
	if s.LFS != nil { // the fork's history points at the source's LFS objects: share them (hard links, D82)
		if _, err := s.LFS.LinkAll(rc.Repo.ID, *s.LFS, fork.ID); err != nil {
			s.Log.Error("fork: lfs objects", "err", err)
		}
	}
	if err := s.Store.SetForkOf(ctx, fork.ID, rc.Repo.ID); err != nil {
		s.fail(w, r, err)
		return
	}
	_ = s.Store.Audit(ctx, p.User.ID, "repo.fork", to+"/"+fork.Name, "from "+rc.Repo.Owner+"/"+rc.Repo.Name)
	s.setFlash(w, "berjaya", p.T("ui.fork.done", rc.Repo.Owner+"/"+rc.Repo.Name))
	http.Redirect(w, r, "/"+to+"/"+fork.Name, http.StatusSeeOther)
}

// pullHead is THE place a pull request's head is resolved (D75): a branch of this repository, or a fork's branch
// fetched fresh into refs/codr/pull/<id> of this repository (hidden from git clients). false = it is gone.
func (s *Server) pullHead(ctx context.Context, rc repoCtx, g gitread.Repo, p store.Pull) (string, bool) {
	if p.HeadRepoID == 0 {
		if !branchExists(rc, p.HeadBranch) {
			return "", false
		}
		return "refs/heads/" + p.HeadBranch, true
	}
	ref := "refs/codr/pull/" + strconv.FormatInt(p.ID, 10)
	if p.Open() && s.Disk.Exists(p.HeadRepoID) {
		if err := s.Disk.FetchBranch(ctx, p.HeadRepoID, p.HeadBranch, rc.Repo.ID, ref); err != nil {
			return "", false
		}
	}
	if _, err := g.Resolve(ctx, ref); err != nil {
		return "", false
	}
	return ref, true
}

// headLabel names a pull request's head for people: the branch, or owner/name:branch from a fork.
func (s *Server) headLabel(ctx context.Context, p store.Pull) string {
	if p.HeadRepoID == 0 {
		return p.HeadBranch
	}
	if src, err := s.Store.RepoByID(ctx, p.HeadRepoID); err == nil {
		return src.Owner + "/" + src.Name + ":" + p.HeadBranch
	}
	return p.HeadBranch
}

// upstreamCtx checks a pull request from this fork into its source: signed in, may push to the fork, may read the
// source, the source takes pull requests. It returns the source and its git view.
func (s *Server) upstreamCtx(w http.ResponseWriter, r *http.Request, pg page, rc repoCtx) (store.Repo, gitread.Repo, []gitread.Ref, bool) {
	ctx := r.Context()
	if pg.User == nil {
		http.Redirect(w, r, "/login?next="+r.URL.Path, http.StatusSeeOther)
		return store.Repo{}, gitread.Repo{}, nil, false
	}
	if rc.Repo.ForkOf == 0 || rc.Access < store.AccessWrite {
		s.errPage(w, r, http.StatusNotFound)
		return store.Repo{}, gitread.Repo{}, nil, false
	}
	src, err := s.Store.RepoByID(ctx, rc.Repo.ForkOf)
	if err != nil {
		s.errPage(w, r, http.StatusNotFound)
		return src, gitread.Repo{}, nil, false
	}
	if a, err := s.Store.AccessFor(ctx, pg.User.ID, src); err != nil || a < store.AccessRead || src.Archived || src.Mirror {
		s.errPage(w, r, http.StatusNotFound)
		return src, gitread.Repo{}, nil, false
	}
	sg := s.git(src.ID)
	branches, err := sg.Branches(ctx)
	if err != nil {
		s.fail(w, r, err)
		return src, sg, nil, false
	}
	return src, sg, branches, true
}

func refHas(refs []gitread.Ref, name string) bool {
	for _, b := range refs {
		if b.Name == name {
			return true
		}
	}
	return false
}

// upstreamPreview compares a fork branch with a source branch inside the source repository, through a temporary
// hidden ref that is deleted at once (the preview must not leave the fork's branch in the source).
func (s *Server) upstreamPreview(ctx context.Context, rc repoCtx, src store.Repo, sg gitread.Repo, d *pullFormData, viewer int64) error {
	if ex, err := s.Store.OpenPullFrom(ctx, src.ID, rc.Repo.ID, d.Head, d.Base); err == nil {
		d.Existing = &ex
	} else if !errors.Is(err, store.ErrNotFound) {
		return err
	}
	tmp := "refs/codr/compare/" + strconv.FormatInt(viewer, 10) + "-" + strconv.FormatInt(rc.Repo.ID, 10)
	if err := s.Disk.FetchBranch(ctx, rc.Repo.ID, d.Head, src.ID, tmp); err != nil {
		return err
	}
	defer func() { _ = s.Disk.DeleteRef(context.WithoutCancel(ctx), src.ID, tmp) }()
	var err error
	if d.Commits, err = sg.LogRange(ctx, d.Base, tmp, 50); err != nil {
		return err
	}
	if len(d.Commits) == 0 {
		d.Same = true
		return nil
	}
	diff, err := sg.DiffRange(ctx, d.Base, tmp, 1)
	if err != nil {
		return err
	}
	d.Files, d.Added, d.Deleted = len(diff.Stats), diff.Added, diff.Deleted
	if _, conflicts, err := sg.MergeTree(ctx, d.Base, tmp); errors.Is(err, gitread.ErrConflict) {
		d.Conflict = conflicts
	} else if err != nil {
		return err
	}
	return nil
}

func (s *Server) uiUpstreamForm(w http.ResponseWriter, r *http.Request) {
	pg := s.newPage(w, r, "")
	rc, _, ok := s.openRepo(w, r, pg, "")
	if !ok {
		return
	}
	src, sg, srcBranches, ok := s.upstreamCtx(w, r, pg, rc)
	if !ok {
		return
	}
	rc.Tab = "pulls"
	d := pullFormData{repoCtx: rc, Upstream: true, UpstreamPath: src.Owner + "/" + src.Name, BaseBranches: srcBranches,
		Head: r.URL.Query().Get("head"), Base: r.URL.Query().Get("base")}
	if !refHas(srcBranches, d.Base) {
		d.Base = src.DefaultBranch
	}
	if !branchExists(rc, d.Head) {
		d.Head = ""
		if len(rc.Branches) > 0 {
			d.Head = rc.Branch
		}
	}
	if d.Head != "" && refHas(srcBranches, d.Base) {
		if err := s.upstreamPreview(r.Context(), rc, src, sg, &d, pg.User.ID); err != nil {
			s.fail(w, r, err)
			return
		}
		if len(d.Commits) > 0 && d.Title == "" {
			d.Title = d.Commits[len(d.Commits)-1].Subject
		}
	}
	pg.Title = pg.T("ui.fork.pr_title", d.UpstreamPath) + " · " + pg.SiteName
	s.render(w, r, http.StatusOK, "pull_new.html", pg.with(d))
}

func (s *Server) uiUpstreamCreate(w http.ResponseWriter, r *http.Request) {
	pg := s.newPage(w, r, "")
	rc, _, ok := s.openRepo(w, r, pg, "")
	if !ok {
		return
	}
	src, sg, srcBranches, ok := s.upstreamCtx(w, r, pg, rc)
	if !ok {
		return
	}
	if !s.checkCSRF(r) {
		s.errPage(w, r, http.StatusForbidden)
		return
	}
	ctx := r.Context()
	r.Body = http.MaxBytesReader(w, r.Body, int64(s.bodyLimit(r))+4096)
	d := pullFormData{repoCtx: rc, Upstream: true, UpstreamPath: src.Owner + "/" + src.Name, BaseBranches: srcBranches,
		Title: strings.TrimSpace(r.PostFormValue("title")), Body: strings.ReplaceAll(r.PostFormValue("body"), "\r\n", "\n"),
		Head: r.PostFormValue("head"), Base: r.PostFormValue("base")}
	bad := func(msg string) {
		d.Error = msg
		pg.Title = pg.T("ui.fork.pr_title", d.UpstreamPath) + " · " + pg.SiteName
		s.render(w, r, http.StatusBadRequest, "pull_new.html", pg.with(d))
	}
	switch {
	case !branchExists(rc, d.Head) || !refHas(srcBranches, d.Base):
		bad(pg.T("ui.pull.bad_branches"))
		return
	case d.Title == "" || utf8.RuneCountInString(d.Title) > pullTitleMax:
		bad(pg.T("ui.issue.bad_title", pullTitleMax))
		return
	case len(d.Body) > s.bodyLimit(r):
		bad(pg.T("ui.issue.bad_body", s.bodyLimit(r)>>10))
		return
	}
	if err := s.upstreamPreview(ctx, rc, src, sg, &d, pg.User.ID); err != nil {
		s.fail(w, r, err)
		return
	}
	srcPulls := "/" + src.Owner + "/" + src.Name + "/pr/"
	if d.Existing != nil {
		http.Redirect(w, r, srcPulls+strconv.FormatInt(d.Existing.Number, 10), http.StatusSeeOther)
		return
	}
	if d.Same {
		bad(pg.T("ui.pull.nothing"))
		return
	}
	p, err := s.Store.CreatePullFrom(ctx, src.ID, pg.User.ID, d.Title, d.Body, rc.Repo.ID, d.Head, d.Base)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if err := s.Disk.FetchBranch(ctx, rc.Repo.ID, d.Head, src.ID, "refs/codr/pull/"+strconv.FormatInt(p.ID, 10)); err != nil {
		s.fail(w, r, err)
		return
	}
	_ = s.Store.Audit(ctx, pg.User.ID, "pr.open", src.Owner+"/"+src.Name+"#"+strconv.FormatInt(p.Number, 10),
		rc.Repo.Owner+"/"+rc.Repo.Name+":"+d.Head+" → "+d.Base)
	p.Author = pg.User.Name
	s.emit(ctx, src.ID, pg.User.ID, "pull_request", pullEvent(p, rc.Repo.Owner+"/"+rc.Repo.Name+":"+d.Head, "opened"))
	s.setFlash(w, "berjaya", pg.T("ui.pull.opened", p.Number))
	http.Redirect(w, r, srcPulls+strconv.FormatInt(p.Number, 10), http.StatusSeeOther)
}
