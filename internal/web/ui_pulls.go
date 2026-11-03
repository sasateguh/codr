package web

import (
	"errors"
	"html/template"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"

	"codr.my/codr/internal/gitread"
	"codr.my/codr/internal/store"
)

// Pull requests (v1.2, FR-22, D53): head branch → base branch inside one repo, with the diff
// from the merge base, a commit list, comments, reviews and a merge button. Who may do what is
// decided here, once (pullCan), before any write:
//   - read: whoever may read the repo
//   - open: write access (the head branch had to be pushed anyway); never on archived or mirror repos
//   - comment: same rule as issues (issueCan create)
//   - review: write access and not the author
//   - close / reopen / edit: author or write access
//   - merge: write access, no conflicts, and when the base branch is protected and setting
//     pull.require_review is on, at least one standing approval from someone other than the author

const pullTitleMax = 200

type pullsData struct {
	repoCtx
	State      string
	Query      string
	Assignee   string             // ?penerima= filter (D85)
	Assigned   map[int64][]string // assignees per listed number
	Pulls      []store.Pull
	Open       int
	ClosedN    int
	Total      int
	Page       int
	Pages      int
	CanCreate  bool
	ReadyLogin bool
}

// Link is this list with one state and page; the search and assignee filters stay.
func (d pullsData) Link(state string, page int) string {
	return listLink(state, 0, d.Query, d.Assignee, page)
}

// Mine is this list filtered to pull requests assigned to name (or unfiltered when it already is).
func (d pullsData) Mine(name string) string {
	if d.Assignee == name {
		name = ""
	}
	return listLink(d.State, 0, d.Query, name, 1)
}

type pullFormData struct {
	repoCtx
	Title    string
	Body     string
	Head     string
	Base     string
	Error    string
	Commits  []gitread.Commit
	Files    int
	Added    int
	Deleted  int
	Conflict []string
	Same     bool // head == base or nothing to merge
	Existing *store.Pull
	// a pull request from this fork into its source (D75)
	Upstream     bool
	UpstreamPath string        // owner/name of the source
	BaseBranches []gitread.Ref // the source's branches
}

type pullData struct {
	Checks    []store.CommitStatus // commit statuses on the head (D81)
	Assignees []store.Assignee     // D85
	CanAssign bool
	repoCtx
	Pull         store.Pull
	BodyHTML     template.HTML
	Comments     []commentView
	Reviews      []reviewView
	Verdicts     map[string]string
	Commits      []gitread.Commit
	Diff         gitread.Diff
	Files        bool // rendering the files page
	Conflict     []string
	HeadGone     bool
	BaseGone     bool
	NeedReview   bool // merge blocked: approval required
	NeedChecks   bool // merge blocked: the base branch requires green checks on the head (D89)
	CanComment   bool
	CanReview    bool
	CanClose     bool
	CanReopen    bool
	CanEdit      bool
	CanMerge     bool
	Mergeable    bool
	Error        string
	Draft        string
	Max          int64
	Fold         int64                 // ui.diff_fold_lines
	Split        bool                  // side-by-side view (?papar=belah)
	Notes        map[string][]noteView // line comments under their line (files page)
	Outdated     []noteView            // line comments whose line left the diff
	AllNotes     []noteView            // every line comment (conversation)
	Ask          lineAsk               // the line whose comment form is open
	AINotes      []aiNoteView          // assistant answers, newest first (D71)
	AIReady      bool                  // this viewer may ask the assistant here
	AIHost       string                // where a request would go, said next to the buttons
	HeadRef      string                // where the head is read from (pullHead, D75)
	HeadLabel    string                // the head as people read it: branch, or owner/name:branch from a fork
	HeadRepoPath string                // owner/name of the repository holding the head branch
}

type reviewView struct {
	store.Review
	HTML template.HTML
}

// pullCan returns (create, review, manage) for the viewer on this repo.
func (s *Server) pullCan(p page, rc repoCtx) (create, manage bool) {
	if p.User == nil || rc.Repo.Archived || rc.Repo.Mirror {
		return false, false
	}
	manage = rc.Access >= store.AccessWrite
	return manage, manage
}

func (s *Server) pullsPath(rc repoCtx) string {
	return "/" + rc.Repo.Owner + "/" + rc.Repo.Name + "/pr"
}

func (s *Server) uiPulls(w http.ResponseWriter, r *http.Request) {
	pg := s.newPage(w, r, "")
	rc, _, ok := s.openRepo(w, r, pg, "")
	if !ok {
		return
	}
	rc.Tab = "pulls"
	q := r.URL.Query()
	d := pullsData{repoCtx: rc, State: "open", Query: strings.TrimSpace(q.Get("q"))}
	if q.Get("keadaan") == "closed" {
		d.State = "closed"
	}
	if utf8.RuneCountInString(d.Query) > 100 {
		d.Query = string([]rune(d.Query)[:100])
	}
	size, err := s.Store.SettingInt(r.Context(), "pull.page_size")
	if err != nil || size < 1 {
		size = 30
	}
	d.Page, _ = strconv.Atoi(q.Get("muka"))
	if d.Page < 1 {
		d.Page = 1
	}
	assignee, aname := s.assigneeFilter(r, "penerima")
	d.Assignee = aname
	if d.Pulls, d.Total, err = s.Store.Pulls(r.Context(), rc.Repo.ID, d.State, d.Query, assignee, int(size), (d.Page-1)*int(size)); err != nil {
		s.fail(w, r, err)
		return
	}
	nums := make([]int64, 0, len(d.Pulls))
	for _, p := range d.Pulls {
		nums = append(nums, p.Number)
	}
	if d.Assigned, err = s.Store.AssigneeNames(r.Context(), rc.Repo.ID, nums); err != nil {
		s.fail(w, r, err)
		return
	}
	d.Pages = (d.Total + int(size) - 1) / int(size)
	if d.Open, d.ClosedN, err = s.Store.PullCounts(r.Context(), rc.Repo.ID); err != nil {
		s.fail(w, r, err)
		return
	}
	d.CanCreate, _ = s.pullCan(pg, rc)
	d.ReadyLogin = pg.User == nil && !rc.Repo.Archived && !rc.Repo.Mirror
	s.takeFlash(w, r, &pg)
	pg.Title = pg.T("ui.pull.title", rc.Repo.Owner+"/"+rc.Repo.Name) + " · " + pg.SiteName
	s.render(w, r, http.StatusOK, "pulls.html", pg.with(d))
}

// branchExists reports whether name is one of the repo's branches.
func branchExists(rc repoCtx, name string) bool {
	for _, b := range rc.Branches {
		if b.Name == name {
			return true
		}
	}
	return false
}

// comparePreview fills the new-pull form with what head would add to base.
func (s *Server) comparePreview(r *http.Request, g gitread.Repo, rc repoCtx, d *pullFormData) error {
	if d.Head == d.Base {
		d.Same = true
		return nil
	}
	if ex, err := s.Store.OpenPullFor(r.Context(), rc.Repo.ID, d.Head, d.Base); err == nil {
		d.Existing = &ex
	} else if !errors.Is(err, store.ErrNotFound) {
		return err
	}
	var err error
	if d.Commits, err = g.LogRange(r.Context(), d.Base, d.Head, 50); err != nil {
		return err
	}
	if len(d.Commits) == 0 {
		d.Same = true
		return nil
	}
	diff, err := g.DiffRange(r.Context(), d.Base, d.Head, 1)
	if err != nil {
		return err
	}
	d.Files, d.Added, d.Deleted = len(diff.Stats), diff.Added, diff.Deleted
	if _, conflicts, err := g.MergeTree(r.Context(), d.Base, d.Head); errors.Is(err, gitread.ErrConflict) {
		d.Conflict = conflicts
	} else if err != nil {
		return err
	}
	return nil
}

func (s *Server) uiPullNewForm(w http.ResponseWriter, r *http.Request) {
	pg := s.newPage(w, r, "")
	rc, g, ok := s.openRepo(w, r, pg, "")
	if !ok {
		return
	}
	if pg.User == nil {
		http.Redirect(w, r, "/login?next="+r.URL.Path, http.StatusSeeOther)
		return
	}
	if can, _ := s.pullCan(pg, rc); !can {
		s.errPage(w, r, http.StatusForbidden)
		return
	}
	rc.Tab = "pulls"
	d := pullFormData{repoCtx: rc, Head: r.URL.Query().Get("head"), Base: r.URL.Query().Get("base")}
	if d.Base == "" || !branchExists(rc, d.Base) {
		d.Base = rc.Branch
	}
	if d.Head == "" || !branchExists(rc, d.Head) {
		d.Head = ""
		for _, b := range rc.Branches { // first non-default branch as a sensible default
			if b.Name != d.Base {
				d.Head = b.Name
				break
			}
		}
	}
	if d.Head != "" {
		if err := s.comparePreview(r, g, rc, &d); err != nil {
			s.fail(w, r, err)
			return
		}
		if len(d.Commits) > 0 && d.Title == "" {
			d.Title = d.Commits[len(d.Commits)-1].Subject // oldest commit's subject
		}
	}
	pg.Title = pg.T("ui.pull.new") + " · " + pg.SiteName
	s.render(w, r, http.StatusOK, "pull_new.html", pg.with(d))
}

func (s *Server) uiPullCreate(w http.ResponseWriter, r *http.Request) {
	pg := s.newPage(w, r, "")
	rc, g, ok := s.openRepo(w, r, pg, "")
	if !ok {
		return
	}
	if pg.User == nil {
		http.Redirect(w, r, "/login?next="+s.pullsPath(rc)+"/baharu", http.StatusSeeOther)
		return
	}
	if can, _ := s.pullCan(pg, rc); !can || !s.checkCSRF(r) {
		s.errPage(w, r, http.StatusForbidden)
		return
	}
	rc.Tab = "pulls"
	r.Body = http.MaxBytesReader(w, r.Body, int64(s.bodyLimit(r))+4096)
	d := pullFormData{repoCtx: rc, Title: strings.TrimSpace(r.PostFormValue("title")), Body: strings.ReplaceAll(r.PostFormValue("body"), "\r\n", "\n"),
		Head: r.PostFormValue("head"), Base: r.PostFormValue("base")}
	bad := func(msg string) {
		d.Error = msg
		if branchExists(rc, d.Head) && branchExists(rc, d.Base) {
			_ = s.comparePreview(r, g, rc, &d)
		}
		pg.Title = pg.T("ui.pull.new") + " · " + pg.SiteName
		s.render(w, r, http.StatusBadRequest, "pull_new.html", pg.with(d))
	}
	switch {
	case !branchExists(rc, d.Head) || !branchExists(rc, d.Base) || d.Head == d.Base:
		bad(pg.T("ui.pull.bad_branches"))
		return
	case d.Title == "" || utf8.RuneCountInString(d.Title) > pullTitleMax:
		bad(pg.T("ui.issue.bad_title", pullTitleMax))
		return
	case len(d.Body) > s.bodyLimit(r):
		bad(pg.T("ui.issue.bad_body", s.bodyLimit(r)>>10))
		return
	}
	if ex, err := s.Store.OpenPullFor(r.Context(), rc.Repo.ID, d.Head, d.Base); err == nil {
		http.Redirect(w, r, s.pullsPath(rc)+"/"+strconv.FormatInt(ex.Number, 10), http.StatusSeeOther)
		return
	}
	cs, err := g.LogRange(r.Context(), d.Base, d.Head, 1)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if len(cs) == 0 {
		bad(pg.T("ui.pull.nothing"))
		return
	}
	p, err := s.Store.CreatePull(r.Context(), rc.Repo.ID, pg.User.ID, d.Title, d.Body, d.Head, d.Base)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	_ = s.Store.Audit(r.Context(), pg.User.ID, "pr.open", rc.Repo.Owner+"/"+rc.Repo.Name+"#"+strconv.FormatInt(p.Number, 10), d.Head+" → "+d.Base)
	p.Author = pg.User.Name
	s.emit(r.Context(), rc.Repo.ID, pg.User.ID, "pull_request", pullEvent(p, "", "opened"))
	s.setFlash(w, "berjaya", pg.T("ui.pull.opened", p.Number))
	http.Redirect(w, r, s.pullsPath(rc)+"/"+strconv.FormatInt(p.Number, 10), http.StatusSeeOther)
}

// openPull loads repo + pull #n from the path; false = already answered.
func (s *Server) openPull(w http.ResponseWriter, r *http.Request, pg page) (repoCtx, gitread.Repo, store.Pull, bool) {
	rc, g, ok := s.openRepo(w, r, pg, "")
	if !ok {
		return rc, g, store.Pull{}, false
	}
	n, err := strconv.ParseInt(r.PathValue("n"), 10, 64)
	if err != nil || n < 1 {
		s.errPage(w, r, http.StatusNotFound)
		return rc, g, store.Pull{}, false
	}
	p, err := s.Store.PullByNumber(r.Context(), rc.Repo.ID, n)
	if errors.Is(err, store.ErrNotFound) {
		// shared numbering: #n may be an issue
		if _, ierr := s.Store.IssueByNumber(r.Context(), rc.Repo.ID, n); ierr == nil {
			http.Redirect(w, r, s.issuesPath(rc)+"/"+strconv.FormatInt(n, 10), http.StatusSeeOther)
			return rc, g, store.Pull{}, false
		}
		s.errPage(w, r, http.StatusNotFound)
		return rc, g, store.Pull{}, false
	}
	if err != nil {
		s.fail(w, r, err)
		return rc, g, store.Pull{}, false
	}
	rc.Tab = "pulls"
	return rc, g, p, true
}

// pullState works out whether the pull can be merged right now and why not.
func (s *Server) pullState(r *http.Request, pg page, g gitread.Repo, rc repoCtx, d *pullData) error {
	p := d.Pull
	// labels first: closed and merged pull requests show them too (the early return below skips the rest)
	d.HeadLabel = s.headLabel(r.Context(), p)
	d.HeadRepoPath = rc.Repo.Owner + "/" + rc.Repo.Name
	if p.HeadRepoID > 0 {
		if src, err := s.Store.RepoByID(r.Context(), p.HeadRepoID); err == nil {
			d.HeadRepoPath = src.Owner + "/" + src.Name
		}
	}
	d.CanComment, _ = s.issueCan(r, pg, rc)
	_, manage := s.pullCan(pg, rc)
	author := pg.User != nil && pg.User.ID == p.AuthorID
	d.CanClose = p.Open() && (manage || author) && !rc.Repo.Archived
	d.CanReopen = p.State == "closed" && (manage || author) && !rc.Repo.Archived
	d.CanEdit = (manage || author) && !rc.Repo.Archived
	d.CanReview = p.Open() && manage && !author
	if d.Verdicts == nil {
		var err error
		if d.Verdicts, err = s.Store.Verdicts(r.Context(), p.ID); err != nil {
			return err
		}
	}
	if !p.Open() {
		return nil
	}
	var headOK bool
	d.HeadRef, headOK = s.pullHead(r.Context(), rc, g, p)
	d.HeadGone, d.BaseGone = !headOK, !branchExists(rc, p.BaseBranch)
	if d.HeadGone || d.BaseGone {
		return nil
	}
	_, conflicts, err := g.MergeTree(r.Context(), p.BaseBranch, d.HeadRef)
	switch {
	case errors.Is(err, gitread.ErrConflict):
		d.Conflict = conflicts
		if len(d.Conflict) == 0 {
			d.Conflict = []string{"?"}
		}
	case err != nil:
		return err
	default:
		d.Mergeable = true
	}
	if !manage || !d.Mergeable {
		return nil
	}
	rule, err := s.Store.RuleFor(r.Context(), rc.Repo, p.BaseBranch)
	if err != nil {
		return err
	}
	if rule.Protected {
		if need, _ := s.Store.SettingBool(r.Context(), "pull.require_review"); need {
			approved := false
			for name, v := range d.Verdicts {
				approved = approved || (v == "approve" && name != p.Author)
			}
			d.NeedReview = !approved
		}
	}
	if rule.RequireChecks { // D89: the head commit must carry statuses and all of them must be success
		holder := rc.Repo.ID
		if p.HeadRepoID > 0 {
			holder = p.HeadRepoID // a fork's runner reports on the fork
		}
		sha, err := g.Resolve(r.Context(), d.HeadRef)
		if err != nil {
			return err
		}
		green, err := s.Store.ChecksGreen(r.Context(), holder, sha)
		if err != nil {
			return err
		}
		d.NeedChecks = !green
	}
	d.CanMerge = !d.NeedReview && !d.NeedChecks
	return nil
}

func (s *Server) renderPull(w http.ResponseWriter, r *http.Request, pg page, g gitread.Repo, rc repoCtx, p store.Pull, d pullData, code int) {
	d.repoCtx, d.Pull = rc, p
	d.BodyHTML = s.issueHTML(p.Body, rc)
	cs, err := s.Store.PullComments(r.Context(), p.ID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	for _, c := range cs {
		d.Comments = append(d.Comments, commentView{Comment: c, HTML: s.issueHTML(c.Body, rc)})
	}
	rs, err := s.Store.Reviews(r.Context(), p.ID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	for _, rv := range rs {
		d.Reviews = append(d.Reviews, reviewView{Review: rv, HTML: s.issueHTML(rv.Body, rc)})
	}
	if d.Assignees, err = s.Store.Assignees(r.Context(), rc.Repo.ID, p.Number); err != nil {
		s.fail(w, r, err)
		return
	}
	_, d.CanAssign = s.pullCan(pg, rc)
	if err := s.pullState(r, pg, g, rc, &d); err != nil {
		s.fail(w, r, err)
		return
	}
	// the checks CI reported on the head commit (D81); after pullState, which is what finds the head (D94: before it,
	// HeadRef was always empty and the pull request page never showed a check)
	if d.HeadRef != "" && !d.HeadGone {
		if sha, err := g.Resolve(r.Context(), d.HeadRef); err == nil {
			holder := rc.Repo.ID
			if p.HeadRepoID > 0 {
				holder = p.HeadRepoID // a fork's runner reports on the fork (D89)
			}
			d.Checks, _ = s.Store.Statuses(r.Context(), holder, sha)
		}
	}
	if p.Open() && !d.HeadGone && !d.BaseGone {
		if d.Commits, err = g.LogRange(r.Context(), p.BaseBranch, d.HeadRef, 100); err != nil {
			s.fail(w, r, err)
			return
		}
	}
	name := "pull.html"
	if d.Files {
		name = "pull_files.html"
		if p.Open() && !d.HeadGone && !d.BaseGone {
			d.Max, _ = s.Store.SettingInt(r.Context(), "pull.diff_max_lines")
			d.Fold, _ = s.Store.SettingInt(r.Context(), "ui.diff_fold_lines")
			d.Split = r.URL.Query().Get("papar") == "belah"
			if d.Diff, err = g.DiffRange(r.Context(), p.BaseBranch, d.HeadRef, int(d.Max)); err != nil {
				s.fail(w, r, err)
				return
			}
		}
	}
	head := ""
	if !d.HeadGone {
		head, _ = g.Resolve(r.Context(), d.HeadRef)
	}
	s.aiState(r, rc, &d, head)
	if err := s.attachNotes(r, g, rc, p, &d); err != nil {
		s.fail(w, r, err)
		return
	}
	pg.Title = "#" + strconv.FormatInt(p.Number, 10) + " " + p.Title + " · " + rc.Repo.Name + " · " + pg.SiteName
	s.render(w, r, code, name, pg.with(d))
}

func (s *Server) uiPull(w http.ResponseWriter, r *http.Request) {
	pg := s.newPage(w, r, "")
	rc, g, p, ok := s.openPull(w, r, pg)
	if !ok {
		return
	}
	s.markThreadRead(r, &pg, rc, "pull", p.Number)
	s.takeFlash(w, r, &pg)
	s.renderPull(w, r, pg, g, rc, p, pullData{}, http.StatusOK)
}

func (s *Server) uiPullFiles(w http.ResponseWriter, r *http.Request) {
	pg := s.newPage(w, r, "")
	rc, g, p, ok := s.openPull(w, r, pg)
	if !ok {
		return
	}
	s.renderPull(w, r, pg, g, rc, p, pullData{Files: true}, http.StatusOK)
}
