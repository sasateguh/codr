package web

import (
	"context"
	"errors"
	"html/template"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"codr.my/codr/internal/store"
	"codr.my/codr/internal/xref"
)

// Issues (v1.1, D50): per-repo issues with Markdown bodies, comments, labels and open/closed state.
// Who may do what is decided here, once, before any write:
//   - read the list / an issue: whoever may read the repo (openRepo already answers 404 otherwise)
//   - open an issue / comment: a signed-in user with write access, a collaborator on a private repo,
//     or anyone signed in on a public repo when setting issue.public_create is true; never on an archived repo
//   - close / reopen / edit: the author or write access
//   - labels (create, delete, assign): write access

const issueTitleMax = 200

type issuesData struct {
	repoCtx
	State      string
	Query      string
	LabelID    int64
	Assignee   string             // ?penerima= filter (D85)
	Assigned   map[int64][]string // assignees per listed number
	Issues     []store.Issue
	Labels     []store.Label
	Open       int
	ClosedN    int
	Total      int
	Page       int
	Pages      int
	CanCreate  bool
	CanManage  bool
	ReadyLogin bool // anonymous viewer: show "log in to open an issue"
}

// Link is this list with one state, label and page; the search and assignee filters stay.
func (d issuesData) Link(state string, label int64, page int) string {
	return listLink(state, label, d.Query, d.Assignee, page)
}

// Mine is this list filtered to issues assigned to name (or unfiltered when it already is).
func (d issuesData) Mine(name string) string {
	if d.Assignee == name {
		name = ""
	}
	return listLink(d.State, d.LabelID, d.Query, name, 1)
}

type issueFormData struct {
	repoCtx
	Title string
	Body  string
	Error string
}

type issueData struct {
	repoCtx
	Issue      store.Issue
	BodyHTML   template.HTML
	Comments   []commentView
	Labels     []store.Label // all repo labels (for the assign form)
	CanComment bool
	CanClose   bool
	CanManage  bool
	Assignees  []store.Assignee // D85
	ClosedBy   closedByView     // D87: the commit or pull request whose keyword closed it
	Error      string
	Draft      string
}

// closedByView says what closed an issue: Text + Href when the viewer may read where it came from, Hidden otherwise.
type closedByView struct {
	Text, Href string
	Hidden     bool
}

// closedBy turns Issue.ClosedBy ("<sha>", "#n", "owner/name@<sha>", "owner/name#n") into what this viewer may see.
func (s *Server) closedBy(rc repoCtx, by string) closedByView {
	if by == "" {
		return closedByView{}
	}
	owner, name, rest := rc.Repo.Owner, rc.Repo.Name, by
	if i := strings.IndexAny(by, "@#"); i > 0 {
		path := by[:i]
		o, n, ok := strings.Cut(path, "/")
		if !ok {
			return closedByView{}
		}
		owner, name, rest = o, n, strings.TrimPrefix(by[i:], "@")
		if !s.canSee(rc, owner, name) {
			return closedByView{Hidden: true}
		}
	}
	prefix := ""
	if owner != rc.Repo.Owner || name != rc.Repo.Name {
		prefix = owner + "/" + name
	}
	if strings.HasPrefix(rest, "#") {
		return closedByView{Text: prefix + rest, Href: "/" + owner + "/" + name + "/pr/" + rest[1:]}
	}
	sha := rest
	if len(sha) > 7 {
		sha = sha[:7]
	}
	if prefix != "" {
		prefix += "@"
	}
	return closedByView{Text: prefix + sha, Href: "/" + owner + "/" + name + "/commit/" + rest}
}

type commentView struct {
	store.Comment
	HTML template.HTML
}

type labelsData struct {
	repoCtx
	Labels    []store.Label
	Colours   []string
	CanManage bool
	Error     string
	Name      string
}

// issueCan returns (create, manage) for the viewer on this repo.
func (s *Server) issueCan(r *http.Request, p page, rc repoCtx) (create, manage bool) {
	if p.User == nil || rc.Repo.Archived {
		return false, false
	}
	manage = rc.Access >= store.AccessWrite
	switch {
	case manage:
		create = true
	case rc.Repo.Private:
		create = rc.Access >= store.AccessRead // a read collaborator may still report
	default:
		create, _ = s.Store.SettingBool(r.Context(), "issue.public_create")
	}
	return create, manage
}

// refLinks turns "#12" outside fenced code blocks into a Markdown link to that issue (FR-21).
var refRe = regexp.MustCompile(`(^|[\s(\[])#(\d+)\b`)

func refLinks(md, owner, repo string) string {
	lines := strings.Split(md, "\n")
	fenced := false
	for i, l := range lines {
		if strings.HasPrefix(strings.TrimSpace(l), "```") || strings.HasPrefix(strings.TrimSpace(l), "~~~") {
			fenced = !fenced
			continue
		}
		if fenced {
			continue
		}
		lines[i] = refRe.ReplaceAllString(l, "${1}[#${2}](/"+owner+"/"+repo+"/isu/${2})")
	}
	return strings.Join(lines, "\n")
}

// canSee reports whether the viewer of rc may read owner/name (cross-repository links, D87).
func (s *Server) canSee(rc repoCtx, owner, name string) bool {
	if owner == rc.Repo.Owner && name == rc.Repo.Name {
		return true
	}
	rp, err := s.Store.RepoByPath(context.Background(), owner, name)
	if err != nil {
		return false
	}
	a, err := s.Store.AccessFor(context.Background(), rc.Viewer, rp)
	return err == nil && a >= store.AccessRead
}

func (s *Server) issueHTML(body string, rc repoCtx) template.HTML {
	md := xref.LinkCross(body, func(owner, name string) bool { return s.canSee(rc, owner, name) })
	h, _, err := renderMarkdown([]byte(refLinks(md, rc.Repo.Owner, rc.Repo.Name)), nil)
	if err != nil {
		return template.HTML(template.HTMLEscapeString(body)) // #nosec G203 -- escaped
	}
	return h
}

func (s *Server) issuesPath(rc repoCtx) string {
	return "/" + rc.Repo.Owner + "/" + rc.Repo.Name + "/isu"
}

// bodyLimit reads setting issue.body_max_kb in bytes.
func (s *Server) bodyLimit(r *http.Request) int {
	kb, err := s.Store.SettingInt(r.Context(), "issue.body_max_kb")
	if err != nil || kb < 1 {
		kb = 64
	}
	return int(kb) << 10
}

func (s *Server) uiIssues(w http.ResponseWriter, r *http.Request) {
	pg := s.newPage(w, r, "")
	rc, _, ok := s.openRepo(w, r, pg, "")
	if !ok {
		return
	}
	rc.Tab = "issues"
	q := r.URL.Query()
	d := issuesData{repoCtx: rc, State: "open", Query: strings.TrimSpace(q.Get("q"))}
	if q.Get("keadaan") == "closed" {
		d.State = "closed"
	}
	if utf8.RuneCountInString(d.Query) > 100 {
		d.Query = string([]rune(d.Query)[:100])
	}
	d.LabelID, _ = strconv.ParseInt(q.Get("label"), 10, 64)
	size, err := s.Store.SettingInt(r.Context(), "issue.page_size")
	if err != nil || size < 1 {
		size = 30
	}
	d.Page, _ = strconv.Atoi(q.Get("muka"))
	if d.Page < 1 {
		d.Page = 1
	}
	assignee, aname := s.assigneeFilter(r, "penerima")
	d.Assignee = aname
	if d.Issues, d.Total, err = s.Store.Issues(r.Context(), rc.Repo.ID, d.State, d.LabelID, d.Query, assignee, int(size), (d.Page-1)*int(size)); err != nil {
		s.fail(w, r, err)
		return
	}
	nums := make([]int64, 0, len(d.Issues))
	for _, i := range d.Issues {
		nums = append(nums, i.Number)
	}
	if d.Assigned, err = s.Store.AssigneeNames(r.Context(), rc.Repo.ID, nums); err != nil {
		s.fail(w, r, err)
		return
	}
	d.Pages = (d.Total + int(size) - 1) / int(size)
	if d.Open, d.ClosedN, err = s.Store.IssueCounts(r.Context(), rc.Repo.ID); err != nil {
		s.fail(w, r, err)
		return
	}
	if d.Labels, err = s.Store.Labels(r.Context(), rc.Repo.ID); err != nil {
		s.fail(w, r, err)
		return
	}
	d.CanCreate, d.CanManage = s.issueCan(r, pg, rc)
	d.ReadyLogin = pg.User == nil && !rc.Repo.Archived
	s.takeFlash(w, r, &pg)
	pg.Title = pg.T("ui.issue.title", rc.Repo.Owner+"/"+rc.Repo.Name) + " · " + pg.SiteName
	s.render(w, r, http.StatusOK, "issues.html", pg.with(d))
}

func (s *Server) uiIssueNewForm(w http.ResponseWriter, r *http.Request) {
	pg := s.newPage(w, r, "")
	rc, _, ok := s.openRepo(w, r, pg, "")
	if !ok {
		return
	}
	if pg.User == nil {
		http.Redirect(w, r, "/login?next="+r.URL.Path, http.StatusSeeOther)
		return
	}
	if can, _ := s.issueCan(r, pg, rc); !can {
		s.errPage(w, r, http.StatusForbidden)
		return
	}
	rc.Tab = "issues"
	pg.Title = pg.T("ui.issue.new") + " · " + pg.SiteName
	s.render(w, r, http.StatusOK, "issue_new.html", pg.with(issueFormData{repoCtx: rc}))
}

func (s *Server) uiIssueCreate(w http.ResponseWriter, r *http.Request) {
	pg := s.newPage(w, r, "")
	rc, _, ok := s.openRepo(w, r, pg, "")
	if !ok {
		return
	}
	if pg.User == nil {
		http.Redirect(w, r, "/login?next="+s.issuesPath(rc)+"/baharu", http.StatusSeeOther)
		return
	}
	if can, _ := s.issueCan(r, pg, rc); !can || !s.checkCSRF(r) {
		s.errPage(w, r, http.StatusForbidden)
		return
	}
	rc.Tab = "issues"
	r.Body = http.MaxBytesReader(w, r.Body, int64(s.bodyLimit(r))+4096)
	d := issueFormData{repoCtx: rc, Title: strings.TrimSpace(r.PostFormValue("title")), Body: strings.ReplaceAll(r.PostFormValue("body"), "\r\n", "\n")}
	switch {
	case d.Title == "" || utf8.RuneCountInString(d.Title) > issueTitleMax:
		d.Error = pg.T("ui.issue.bad_title", issueTitleMax)
	case len(d.Body) > s.bodyLimit(r):
		d.Error = pg.T("ui.issue.bad_body", s.bodyLimit(r)>>10)
	}
	if d.Error != "" {
		pg.Title = pg.T("ui.issue.new") + " · " + pg.SiteName
		s.render(w, r, http.StatusBadRequest, "issue_new.html", pg.with(d))
		return
	}
	i, err := s.Store.CreateIssue(r.Context(), rc.Repo.ID, pg.User.ID, d.Title, d.Body)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	_ = s.Store.Audit(r.Context(), pg.User.ID, "issue.open", rc.Repo.Owner+"/"+rc.Repo.Name+"#"+strconv.FormatInt(i.Number, 10), d.Title)
	i.Author = pg.User.Name
	s.emit(r.Context(), rc.Repo.ID, pg.User.ID, "issues", issueEvent(i, "opened"))
	s.setFlash(w, "berjaya", pg.T("ui.issue.opened", i.Number))
	http.Redirect(w, r, s.issuesPath(rc)+"/"+strconv.FormatInt(i.Number, 10), http.StatusSeeOther)
}

// openIssue loads repo + issue #n from the path; false = already answered.
func (s *Server) openIssue(w http.ResponseWriter, r *http.Request, pg page) (repoCtx, store.Issue, bool) {
	rc, _, ok := s.openRepo(w, r, pg, "")
	if !ok {
		return rc, store.Issue{}, false
	}
	n, err := strconv.ParseInt(r.PathValue("n"), 10, 64)
	if err != nil || n < 1 {
		s.errPage(w, r, http.StatusNotFound)
		return rc, store.Issue{}, false
	}
	i, err := s.Store.IssueByNumber(r.Context(), rc.Repo.ID, n)
	if errors.Is(err, store.ErrNotFound) {
		// shared numbering (D53): #n may be a pull request
		if _, perr := s.Store.PullByNumber(r.Context(), rc.Repo.ID, n); perr == nil {
			http.Redirect(w, r, s.pullsPath(rc)+"/"+strconv.FormatInt(n, 10), http.StatusSeeOther)
			return rc, store.Issue{}, false
		}
	}
	if err != nil {
		s.fail(w, r, err)
		return rc, store.Issue{}, false
	}
	rc.Tab = "issues"
	return rc, i, true
}

func (s *Server) renderIssue(w http.ResponseWriter, r *http.Request, pg page, rc repoCtx, i store.Issue, d issueData, code int) {
	d.repoCtx, d.Issue = rc, i
	d.BodyHTML = s.issueHTML(i.Body, rc)
	cs, err := s.Store.Comments(r.Context(), i.ID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	for _, c := range cs {
		d.Comments = append(d.Comments, commentView{Comment: c, HTML: s.issueHTML(c.Body, rc)})
	}
	if d.Labels, err = s.Store.Labels(r.Context(), rc.Repo.ID); err != nil {
		s.fail(w, r, err)
		return
	}
	if d.Assignees, err = s.Store.Assignees(r.Context(), rc.Repo.ID, i.Number); err != nil {
		s.fail(w, r, err)
		return
	}
	if i.Closed() {
		d.ClosedBy = s.closedBy(rc, i.ClosedBy)
	}
	d.CanComment, d.CanManage = s.issueCan(r, pg, rc)
	d.CanClose = d.CanManage || (pg.User != nil && pg.User.ID == i.AuthorID && !rc.Repo.Archived)
	pg.Title = "#" + strconv.FormatInt(i.Number, 10) + " " + i.Title + " · " + rc.Repo.Name + " · " + pg.SiteName
	s.render(w, r, code, "issue.html", pg.with(d))
}

func (s *Server) uiIssue(w http.ResponseWriter, r *http.Request) {
	pg := s.newPage(w, r, "")
	rc, i, ok := s.openIssue(w, r, pg)
	if !ok {
		return
	}
	s.markThreadRead(r, &pg, rc, "issue", i.Number)
	s.takeFlash(w, r, &pg)
	s.renderIssue(w, r, pg, rc, i, issueData{}, http.StatusOK)
}

func (s *Server) uiIssueComment(w http.ResponseWriter, r *http.Request) {
	pg := s.newPage(w, r, "")
	rc, i, ok := s.openIssue(w, r, pg)
	if !ok {
		return
	}
	if pg.User == nil {
		http.Redirect(w, r, "/login?next="+s.issuesPath(rc)+"/"+strconv.FormatInt(i.Number, 10), http.StatusSeeOther)
		return
	}
	if can, _ := s.issueCan(r, pg, rc); !can || !s.checkCSRF(r) {
		s.errPage(w, r, http.StatusForbidden)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, int64(s.bodyLimit(r))+4096)
	body := strings.TrimSpace(strings.ReplaceAll(r.PostFormValue("body"), "\r\n", "\n"))
	if body == "" || len(body) > s.bodyLimit(r) {
		s.renderIssue(w, r, pg, rc, i, issueData{Error: pg.T("ui.issue.bad_body", s.bodyLimit(r)>>10), Draft: body}, http.StatusBadRequest)
		return
	}
	cid, err := s.Store.AddComment(r.Context(), i.ID, pg.User.ID, body)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.emit(r.Context(), rc.Repo.ID, pg.User.ID, "issue_comment", map[string]any{"action": "created", "id": cid.ID, "number": i.Number,
		"is_pull": false, "title": i.Title, "body": body, "user": pg.User.Name})
	s.setFlash(w, "berjaya", pg.T("ui.issue.commented"))
	http.Redirect(w, r, s.issuesPath(rc)+"/"+strconv.FormatInt(i.Number, 10), http.StatusSeeOther)
}

func (s *Server) uiIssueState(w http.ResponseWriter, r *http.Request) {
	pg := s.newPage(w, r, "")
	rc, i, ok := s.openIssue(w, r, pg)
	if !ok {
		return
	}
	_, manage := s.issueCan(r, pg, rc)
	author := pg.User != nil && pg.User.ID == i.AuthorID && !rc.Repo.Archived
	if !(manage || author) || !s.checkCSRF(r) {
		s.errPage(w, r, http.StatusForbidden)
		return
	}
	closed := r.PostFormValue("keadaan") == "closed"
	if err := s.Store.SetIssueState(r.Context(), i.ID, closed); err != nil {
		s.fail(w, r, err)
		return
	}
	target := rc.Repo.Owner + "/" + rc.Repo.Name + "#" + strconv.FormatInt(i.Number, 10)
	if closed {
		_ = s.Store.Audit(r.Context(), pg.User.ID, "issue.close", target, "")
		s.emit(r.Context(), rc.Repo.ID, pg.User.ID, "issues", issueEvent(i, "closed"))
		s.setFlash(w, "berjaya", pg.T("ui.issue.closed_flash", i.Number))
	} else {
		_ = s.Store.Audit(r.Context(), pg.User.ID, "issue.reopen", target, "")
		s.emit(r.Context(), rc.Repo.ID, pg.User.ID, "issues", issueEvent(i, "reopened"))
		s.setFlash(w, "berjaya", pg.T("ui.issue.reopened_flash", i.Number))
	}
	http.Redirect(w, r, s.issuesPath(rc)+"/"+strconv.FormatInt(i.Number, 10), http.StatusSeeOther)
}

func (s *Server) uiIssueEdit(w http.ResponseWriter, r *http.Request) {
	pg := s.newPage(w, r, "")
	rc, i, ok := s.openIssue(w, r, pg)
	if !ok {
		return
	}
	_, manage := s.issueCan(r, pg, rc)
	author := pg.User != nil && pg.User.ID == i.AuthorID && !rc.Repo.Archived
	if !(manage || author) || !s.checkCSRF(r) {
		s.errPage(w, r, http.StatusForbidden)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, int64(s.bodyLimit(r))+4096)
	title := strings.TrimSpace(r.PostFormValue("title"))
	body := strings.ReplaceAll(r.PostFormValue("body"), "\r\n", "\n")
	if title == "" || utf8.RuneCountInString(title) > issueTitleMax {
		s.renderIssue(w, r, pg, rc, i, issueData{Error: pg.T("ui.issue.bad_title", issueTitleMax)}, http.StatusBadRequest)
		return
	}
	if len(body) > s.bodyLimit(r) {
		s.renderIssue(w, r, pg, rc, i, issueData{Error: pg.T("ui.issue.bad_body", s.bodyLimit(r)>>10)}, http.StatusBadRequest)
		return
	}
	if err := s.Store.UpdateIssue(r.Context(), i.ID, title, body); err != nil {
		s.fail(w, r, err)
		return
	}
	_ = s.Store.Audit(r.Context(), pg.User.ID, "issue.edit", rc.Repo.Owner+"/"+rc.Repo.Name+"#"+strconv.FormatInt(i.Number, 10), title)
	i.Title, i.Body = title, body
	s.emit(r.Context(), rc.Repo.ID, pg.User.ID, "issues", issueEvent(i, "edited"))
	s.setFlash(w, "berjaya", pg.T("ui.issue.edited"))
	http.Redirect(w, r, s.issuesPath(rc)+"/"+strconv.FormatInt(i.Number, 10), http.StatusSeeOther)
}

func (s *Server) uiIssueLabels(w http.ResponseWriter, r *http.Request) {
	pg := s.newPage(w, r, "")
	rc, i, ok := s.openIssue(w, r, pg)
	if !ok {
		return
	}
	if _, manage := s.issueCan(r, pg, rc); !manage || !s.checkCSRF(r) {
		s.errPage(w, r, http.StatusForbidden)
		return
	}
	var ids []int64
	for _, v := range r.PostForm["label"] {
		if id, err := strconv.ParseInt(v, 10, 64); err == nil {
			ids = append(ids, id)
		}
	}
	if err := s.Store.SetIssueLabels(r.Context(), rc.Repo.ID, i.ID, ids); err != nil {
		s.fail(w, r, err)
		return
	}
	s.setFlash(w, "berjaya", pg.T("ui.issue.labels_saved"))
	http.Redirect(w, r, s.issuesPath(rc)+"/"+strconv.FormatInt(i.Number, 10), http.StatusSeeOther)
}

func (s *Server) renderLabels(w http.ResponseWriter, r *http.Request, pg page, rc repoCtx, d labelsData, code int) {
	var err error
	d.repoCtx = rc
	if d.Labels, err = s.Store.Labels(r.Context(), rc.Repo.ID); err != nil {
		s.fail(w, r, err)
		return
	}
	d.Colours = store.LabelColours
	_, d.CanManage = s.issueCan(r, pg, rc)
	pg.Title = pg.T("ui.label.title") + " · " + rc.Repo.Name + " · " + pg.SiteName
	s.render(w, r, code, "labels.html", pg.with(d))
}

func (s *Server) uiLabels(w http.ResponseWriter, r *http.Request) {
	pg := s.newPage(w, r, "")
	rc, _, ok := s.openRepo(w, r, pg, "")
	if !ok {
		return
	}
	rc.Tab = "issues"
	s.takeFlash(w, r, &pg)
	s.renderLabels(w, r, pg, rc, labelsData{}, http.StatusOK)
}

func (s *Server) uiLabelCreate(w http.ResponseWriter, r *http.Request) {
	pg := s.newPage(w, r, "")
	rc, _, ok := s.openRepo(w, r, pg, "")
	if !ok {
		return
	}
	rc.Tab = "issues"
	if _, manage := s.issueCan(r, pg, rc); !manage || !s.checkCSRF(r) {
		s.errPage(w, r, http.StatusForbidden)
		return
	}
	name, colour := strings.TrimSpace(r.PostFormValue("name")), r.PostFormValue("colour")
	if name == "" || utf8.RuneCountInString(name) > 40 || strings.ContainsAny(name, "<>\n") || !store.ValidLabelColour(colour) {
		s.renderLabels(w, r, pg, rc, labelsData{Error: pg.T("ui.label.bad"), Name: name}, http.StatusBadRequest)
		return
	}
	_, err := s.Store.CreateLabel(r.Context(), rc.Repo.ID, name, colour)
	switch {
	case errors.Is(err, store.ErrExists):
		s.renderLabels(w, r, pg, rc, labelsData{Error: pg.T("ui.label.exists", name), Name: name}, http.StatusBadRequest)
		return
	case err != nil:
		s.fail(w, r, err)
		return
	}
	s.setFlash(w, "berjaya", pg.T("ui.label.created", name))
	http.Redirect(w, r, "/"+rc.Repo.Owner+"/"+rc.Repo.Name+"/label", http.StatusSeeOther)
}

func (s *Server) uiLabelDelete(w http.ResponseWriter, r *http.Request) {
	pg := s.newPage(w, r, "")
	rc, _, ok := s.openRepo(w, r, pg, "")
	if !ok {
		return
	}
	if _, manage := s.issueCan(r, pg, rc); !manage || !s.checkCSRF(r) {
		s.errPage(w, r, http.StatusForbidden)
		return
	}
	id, _ := strconv.ParseInt(r.PostFormValue("id"), 10, 64)
	if err := s.Store.DeleteLabel(r.Context(), rc.Repo.ID, id); err != nil {
		s.fail(w, r, err)
		return
	}
	s.setFlash(w, "berjaya", pg.T("ui.label.deleted"))
	http.Redirect(w, r, "/"+rc.Repo.Owner+"/"+rc.Repo.Name+"/label", http.StatusSeeOther)
}
