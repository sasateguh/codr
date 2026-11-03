package web

import (
	"html/template"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"codr.my/codr/internal/gitread"
	"codr.my/codr/internal/store"
)

// Line comments on a pull request diff (D65, FR-22). No JavaScript: a line number is a link that asks
// the server to render a form under that line; existing comments sit under the line they belong to.

// noteView is a line comment ready for a template.
type noteView struct {
	store.LineComment
	HTML     template.HTML
	Outdated bool // written against an older head
}

// lineAsk is the line whose comment form is open (?ulas_fail=&ulas_sisi=&ulas_baris=).
type lineAsk struct {
	Path string
	Side string
	Line int
}

func noteKey(path, side string, line int) string {
	return path + "\x00" + side + "\x00" + strconv.Itoa(line)
}

// notesAt is the template lookup for the comments under one diff line.
func notesAt(m map[string][]noteView, path, side string, line int) []noteView {
	if line <= 0 {
		return nil
	}
	return m[noteKey(path, side, line)]
}

// askHref builds the link that opens the comment form under path:line.
func askHref(path, side string, line int) string {
	q := url.Values{"ulas_fail": {path}, "ulas_sisi": {side}, "ulas_baris": {strconv.Itoa(line)}}
	return "?" + q.Encode() + "#borang-ulas"
}

// attachNotes loads the line comments of p: on the files page they are placed under their line, and the
// ones whose line is no longer in the diff are kept apart; the conversation lists them all.
func (s *Server) attachNotes(r *http.Request, g gitread.Repo, rc repoCtx, p store.Pull, d *pullData) error {
	cs, err := s.Store.LineComments(r.Context(), p.ID)
	if err != nil {
		return err
	}
	head := ""
	if !d.HeadGone && d.HeadRef != "" {
		head, _ = g.Resolve(r.Context(), d.HeadRef)
	}
	present := map[string]bool{}
	for _, f := range d.Diff.Files {
		for _, l := range f.Lines {
			switch l.Kind {
			case "del":
				present[noteKey(f.Path, "old", l.Old)] = true
			case "add", "ctx":
				present[noteKey(f.Path, "new", l.New)] = true
			}
		}
	}
	d.Notes = map[string][]noteView{}
	for _, c := range cs {
		n := noteView{LineComment: c, HTML: s.issueHTML(c.Body, rc), Outdated: head != "" && c.Commit != head}
		d.AllNotes = append(d.AllNotes, n)
		if k := noteKey(c.Path, c.Side, c.Line); present[k] {
			d.Notes[k] = append(d.Notes[k], n)
		} else {
			d.Outdated = append(d.Outdated, n)
		}
	}
	q := r.URL.Query()
	if line, err := strconv.Atoi(q.Get("ulas_baris")); err == nil && line > 0 && d.CanComment {
		if side := q.Get("ulas_sisi"); side == "old" || side == "new" {
			if path := q.Get("ulas_fail"); present[noteKey(path, side, line)] {
				d.Ask = lineAsk{Path: path, Side: side, Line: line}
			}
		}
	}
	return nil
}

// uiPullLineComment stores a comment on one line of the current diff.
func (s *Server) uiPullLineComment(w http.ResponseWriter, r *http.Request) {
	pg := s.newPage(w, r, "")
	rc, g, p, ok := s.openPull(w, r, pg)
	if !ok {
		return
	}
	files := s.pullPath(rc, p) + "/fail"
	if pg.User == nil {
		http.Redirect(w, r, "/login?next="+files, http.StatusSeeOther)
		return
	}
	if can, _ := s.issueCan(r, pg, rc); !can || !p.Open() || !s.checkCSRF(r) {
		s.errPage(w, r, http.StatusForbidden)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, int64(s.bodyLimit(r))+8192)
	path, side := r.PostFormValue("path"), r.PostFormValue("side")
	line, err := strconv.Atoi(r.PostFormValue("line"))
	body := strings.TrimSpace(strings.ReplaceAll(r.PostFormValue("body"), "\r\n", "\n"))
	if err != nil || line < 1 || (side != "old" && side != "new") || body == "" || len(body) > s.bodyLimit(r) {
		http.Redirect(w, r, files+askHref(path, side, line), http.StatusSeeOther)
		return
	}
	// the line must be in the diff the reviewer is looking at now
	max, _ := s.Store.SettingInt(r.Context(), "pull.diff_max_lines")
	headRef, ok := s.pullHead(r.Context(), rc, g, p)
	if !ok {
		s.errPage(w, r, http.StatusConflict)
		return
	}
	diff, err := g.DiffRange(r.Context(), p.BaseBranch, headRef, int(max))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	found := false
	for _, f := range diff.Files {
		for _, l := range f.Lines {
			if f.Path == path && ((side == "old" && l.Kind == "del" && l.Old == line) || (side == "new" && l.Kind != "del" && l.Kind != "hunk" && l.New == line)) {
				found = true
			}
		}
	}
	if !found {
		s.errPage(w, r, http.StatusBadRequest)
		return
	}
	head, err := g.Resolve(r.Context(), headRef)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	c, err := s.Store.AddLineComment(r.Context(), p.ID, pg.User.ID, path, side, line, head, body)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.emit(r.Context(), rc.Repo.ID, pg.User.ID, "pull_request_review_comment", map[string]any{"action": "created", "id": c.ID,
		"number": p.Number, "title": p.Title, "path": path, "side": side, "line": line, "commit_id": head, "body": body, "user": pg.User.Name})
	s.setFlash(w, "berjaya", pg.T("ui.issue.commented"))
	http.Redirect(w, r, files+"#ulasan-"+strconv.FormatInt(c.ID, 10), http.StatusSeeOther)
}
