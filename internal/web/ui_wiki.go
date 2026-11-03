package web

import (
	"errors"
	"html/template"
	"net/http"
	"regexp"
	"strings"
	"unicode/utf8"

	"codr.my/codr/internal/store"
	"codr.my/codr/internal/wiki"
)

// Wiki (D83): pages of Markdown in a git repository beside the code (<id>.wiki.git). Reading follows the repository;
// writing needs write access (setting wiki.public_edit lets any signed-in person edit the wiki of a public repository).
// [[Page title]] links to another page. Every save is a commit; the history of a page is its git log.

type wikiData struct {
	repoCtx
	Pages    []wiki.Page
	Page     string // name
	Title    string
	HTML     template.HTML
	Removed  bool
	Body     string // editor
	Base     string // commit the editor started from
	Rev      string // ?v= when an old revision is shown
	History  []wiki.Rev
	CanEdit  bool
	New      bool
	Error    string
	Message  string
	Missing  bool // the page does not exist (yet)
	CloneURL string
}

func (s *Server) wikiOf(rc repoCtx) wiki.Wiki {
	return wiki.Wiki{Path: s.Disk.WikiPath(rc.Repo.ID), Git: s.Git.Binary, Timeout: s.UI.GitTimeout}
}

func (s *Server) wikiCanEdit(r *http.Request, pg page, rc repoCtx) bool {
	if pg.User == nil || rc.Repo.Archived || rc.Repo.Mirror {
		return false
	}
	if rc.Access >= store.AccessWrite {
		return true
	}
	open, _ := s.Store.SettingBool(r.Context(), "wiki.public_edit")
	return open && !rc.Repo.Private
}

func (s *Server) wikiPath(rc repoCtx, name string) string {
	return "/" + rc.Repo.Owner + "/" + rc.Repo.Name + "/wiki/" + name
}

var wikiLinkRE = regexp.MustCompile(`\[\[([^\[\]\n|]{1,100})(?:\|([^\[\]\n]{1,100}))?\]\]`)

// wikiLinks turns [[Title]] and [[Title|text]] into Markdown links to pages, outside fenced code blocks.
func wikiLinks(md, base string) string {
	lines := strings.Split(md, "\n")
	fence := false
	for i, l := range lines {
		if strings.HasPrefix(strings.TrimSpace(l), "```") {
			fence = !fence
			continue
		}
		if fence {
			continue
		}
		lines[i] = wikiLinkRE.ReplaceAllStringFunc(l, func(m string) string {
			sm := wikiLinkRE.FindStringSubmatch(m)
			target, text := wiki.Slug(sm[1]), strings.TrimSpace(sm[1])
			if sm[2] != "" {
				text = strings.TrimSpace(sm[2])
			}
			if target == "" {
				return m
			}
			return "[" + strings.NewReplacer("[", "", "]", "").Replace(text) + "](" + base + target + ")"
		})
	}
	return strings.Join(lines, "\n")
}

func (s *Server) renderWiki(w http.ResponseWriter, r *http.Request, pg page, rc repoCtx, d wikiData, tmpl string, code int) {
	var err error
	d.repoCtx = rc
	rc.Tab = "wiki"
	d.repoCtx.Tab = "wiki"
	if d.Pages, err = s.wikiOf(rc).Pages(r.Context()); err != nil {
		s.fail(w, r, err)
		return
	}
	d.CanEdit = s.wikiCanEdit(r, pg, rc)
	d.CloneURL = s.UI.HTTPHost + "/" + rc.Repo.Owner + "/" + rc.Repo.Name + ".wiki.git"
	title := pg.T("ui.wiki.title")
	if d.Title != "" {
		title = d.Title + " · " + title
	}
	pg.Title = title + " · " + rc.Repo.Name + " · " + pg.SiteName
	s.render(w, r, code, tmpl, pg.with(d))
}

func (s *Server) wikiShow(w http.ResponseWriter, r *http.Request, pg page, rc repoCtx, name string) {
	rev := r.URL.Query().Get("v")
	b, _, err := s.wikiOf(rc).Read(r.Context(), name, rev)
	d := wikiData{Page: name, Title: wiki.TitleOf(name), Rev: rev}
	switch {
	case errors.Is(err, wiki.ErrNotFound) || errors.Is(err, wiki.ErrBadName):
		d.Missing = true
		s.renderWiki(w, r, pg, rc, d, "wiki.html", http.StatusNotFound)
		return
	case err != nil:
		s.fail(w, r, err)
		return
	}
	md := wikiLinks(string(b), s.wikiPath(rc, ""))
	h, removed, err := renderMarkdown([]byte(md), &mdBase{Owner: rc.Repo.Owner, Repo: rc.Repo.Name, Ref: rc.Branch})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	d.HTML, d.Removed = h, removed
	s.renderWiki(w, r, pg, rc, d, "wiki.html", http.StatusOK)
}

func (s *Server) uiWikiHome(w http.ResponseWriter, r *http.Request) {
	pg := s.newPage(w, r, "")
	rc, _, ok := s.openRepo(w, r, pg, "")
	if !ok {
		return
	}
	s.takeFlash(w, r, &pg)
	if head, _ := s.wikiOf(rc).Head(r.Context()); head == "" {
		s.renderWiki(w, r, pg, rc, wikiData{Missing: true}, "wiki.html", http.StatusOK) // an empty wiki: the invitation
		return
	}
	s.wikiShow(w, r, pg, rc, wiki.Home)
}

func (s *Server) uiWikiPage(w http.ResponseWriter, r *http.Request) {
	pg := s.newPage(w, r, "")
	rc, _, ok := s.openRepo(w, r, pg, "")
	if !ok {
		return
	}
	s.takeFlash(w, r, &pg)
	s.wikiShow(w, r, pg, rc, r.PathValue("page"))
}

func (s *Server) uiWikiList(w http.ResponseWriter, r *http.Request) {
	pg := s.newPage(w, r, "")
	rc, _, ok := s.openRepo(w, r, pg, "")
	if !ok {
		return
	}
	s.renderWiki(w, r, pg, rc, wikiData{Title: pg.T("ui.wiki.all")}, "wiki_list.html", http.StatusOK)
}

func (s *Server) uiWikiHistory(w http.ResponseWriter, r *http.Request) {
	pg := s.newPage(w, r, "")
	rc, _, ok := s.openRepo(w, r, pg, "")
	if !ok {
		return
	}
	name := r.PathValue("page")
	h, err := s.wikiOf(rc).History(r.Context(), name, 100)
	if err != nil || len(h) == 0 {
		s.errPage(w, r, http.StatusNotFound)
		return
	}
	s.renderWiki(w, r, pg, rc, wikiData{Page: name, Title: wiki.TitleOf(name), History: h}, "wiki_history.html", http.StatusOK)
}

// uiWikiEditForm opens the editor: an existing page (its text and the commit it was read at) or a new one.
func (s *Server) uiWikiEditForm(w http.ResponseWriter, r *http.Request) {
	pg := s.newPage(w, r, "")
	rc, _, ok := s.openRepo(w, r, pg, "")
	if !ok {
		return
	}
	if pg.User == nil {
		http.Redirect(w, r, "/login?next="+r.URL.Path, http.StatusSeeOther)
		return
	}
	if !s.wikiCanEdit(r, pg, rc) {
		s.errPage(w, r, http.StatusForbidden)
		return
	}
	name := r.PathValue("page")
	d := wikiData{New: name == ""}
	if name == "" {
		d.Title = r.URL.Query().Get("tajuk") // a [[link]] to a missing page offers to create it
		if head, _ := s.wikiOf(rc).Head(r.Context()); head == "" && d.Title == "" {
			d.Title = wiki.Home
		}
	} else {
		b, at, err := s.wikiOf(rc).Read(r.Context(), name, "")
		if err != nil {
			http.Redirect(w, r, s.wikiPath(rc, "_baharu")+"?tajuk="+wiki.TitleOf(name), http.StatusSeeOther)
			return
		}
		d.Page, d.Title, d.Body, d.Base = name, wiki.TitleOf(name), string(b), at
	}
	s.renderWiki(w, r, pg, rc, d, "wiki_edit.html", http.StatusOK)
}

func cleanLine(v string, n int) string {
	v = strings.TrimSpace(strings.NewReplacer("\r", " ", "\n", " ").Replace(v))
	if utf8.RuneCountInString(v) > n {
		v = string([]rune(v)[:n])
	}
	return v
}

// uiWikiSave writes a page (new, edited or renamed).
func (s *Server) uiWikiSave(w http.ResponseWriter, r *http.Request) {
	pg := s.newPage(w, r, "")
	rc, _, ok := s.openRepo(w, r, pg, "")
	if !ok {
		return
	}
	if pg.User == nil || !s.wikiCanEdit(r, pg, rc) || !s.checkCSRF(r) {
		s.errPage(w, r, http.StatusForbidden)
		return
	}
	ctx := r.Context()
	limit := s.bodyLimit(r)
	r.Body = http.MaxBytesReader(w, r.Body, int64(limit)+8192)
	old := r.PathValue("page")
	title := cleanLine(r.PostFormValue("title"), 100)
	body := strings.ReplaceAll(r.PostFormValue("body"), "\r\n", "\n")
	msg := cleanLine(r.PostFormValue("message"), 200)
	base := r.PostFormValue("base")
	name := wiki.Slug(title)
	d := wikiData{Page: old, Title: title, Body: body, Base: base, Message: msg, New: old == ""}
	bad := func(key string, args ...any) {
		d.Error = pg.T(key, args...)
		s.renderWiki(w, r, pg, rc, d, "wiki_edit.html", http.StatusBadRequest)
	}
	if name == "" {
		bad("ui.wiki.bad_title")
		return
	}
	if len(body) > limit {
		bad("ui.issue.bad_body", limit>>10)
		return
	}
	action := "edited"
	if old == "" {
		action = "created"
	}
	if msg == "" {
		msg = pg.T("ui.wiki.default_message."+action, wiki.TitleOf(name))
	}
	email := pg.User.Email
	if email == "" {
		email = pg.User.Name + "@" + strings.TrimPrefix(strings.TrimPrefix(s.UI.HTTPHost, "https://"), "http://")
	}
	rename := ""
	if old != "" && old != name {
		rename = old
	}
	commit, err := s.wikiOf(rc).Save(ctx, name, rename, []byte(body), wiki.Author{Name: pg.User.Name, Email: email}, msg, base)
	switch {
	case errors.Is(err, wiki.ErrConflict):
		bad("ui.wiki.conflict")
		return
	case errors.Is(err, wiki.ErrBadName):
		bad("ui.wiki.bad_title")
		return
	case err != nil:
		s.fail(w, r, err)
		return
	}
	_ = s.Store.Audit(ctx, pg.User.ID, "wiki."+action, rc.Repo.Owner+"/"+rc.Repo.Name+"/wiki/"+name, msg)
	s.emit(ctx, rc.Repo.ID, pg.User.ID, "wiki", map[string]any{"action": action, "page": name, "title": wiki.TitleOf(name), "sha": commit, "renamed_from": rename})
	s.setFlash(w, "berjaya", pg.T("ui.wiki.saved"))
	http.Redirect(w, r, s.wikiPath(rc, name), http.StatusSeeOther)
}

func (s *Server) uiWikiDelete(w http.ResponseWriter, r *http.Request) {
	pg := s.newPage(w, r, "")
	rc, _, ok := s.openRepo(w, r, pg, "")
	if !ok {
		return
	}
	if pg.User == nil || !s.wikiCanEdit(r, pg, rc) || !s.checkCSRF(r) {
		s.errPage(w, r, http.StatusForbidden)
		return
	}
	ctx, name := r.Context(), r.PathValue("page")
	email := pg.User.Email
	if email == "" {
		email = pg.User.Name + "@" + strings.TrimPrefix(strings.TrimPrefix(s.UI.HTTPHost, "https://"), "http://")
	}
	commit, err := s.wikiOf(rc).Delete(ctx, name, wiki.Author{Name: pg.User.Name, Email: email}, pg.T("ui.wiki.default_message.deleted", wiki.TitleOf(name)), r.PostFormValue("base"))
	switch {
	case errors.Is(err, wiki.ErrNotFound), errors.Is(err, wiki.ErrBadName):
		s.errPage(w, r, http.StatusNotFound)
		return
	case errors.Is(err, wiki.ErrConflict):
		s.setFlash(w, "ralat", pg.T("ui.wiki.conflict"))
		http.Redirect(w, r, s.wikiPath(rc, name), http.StatusSeeOther)
		return
	case err != nil:
		s.fail(w, r, err)
		return
	}
	_ = s.Store.Audit(ctx, pg.User.ID, "wiki.deleted", rc.Repo.Owner+"/"+rc.Repo.Name+"/wiki/"+name, "")
	s.emit(ctx, rc.Repo.ID, pg.User.ID, "wiki", map[string]any{"action": "deleted", "page": name, "title": wiki.TitleOf(name), "sha": commit})
	s.setFlash(w, "berjaya", pg.T("ui.wiki.deleted", wiki.TitleOf(name)))
	http.Redirect(w, r, s.wikiPath(rc, "_semua"), http.StatusSeeOther)
}
