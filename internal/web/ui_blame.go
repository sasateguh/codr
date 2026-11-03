package web

import (
	"html/template"
	"net/http"
	"path"

	"codr.my/codr/internal/gitread"
)

// blameRow pairs one highlighted line with the commit that last touched it.
type blameRow struct {
	N       int
	HTML    template.HTML
	Hash    string
	Short   string
	Author  string
	When    string // ISO date for <time datetime>
	Ago     string
	Summary string
	First   bool
}

type blameData struct {
	repoCtx
	Path     string
	Crumbs   []crumb
	Name     string
	Blob     gitread.Blob
	Rows     []blameRow
	MaxShown string
	TooBig   bool
}

// uiBlame shows who last changed each line (v0.6.2). The same size/binary limits as the
// file page apply; git does the attribution, the page only pairs it with the highlighted text.
func (s *Server) uiBlame(w http.ResponseWriter, r *http.Request) {
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
	d := blameData{repoCtx: rc, Path: cp, Crumbs: crumbs(cp), Name: path.Base(cp), Blob: b, MaxShown: humanSize(maxKB << 10), TooBig: b.Truncated}
	if !b.Binary && !b.Truncated {
		lines, err := g.Blame(r.Context(), rc.Ref, cp)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		code := highlight(d.Name, string(b.Content))
		d.Rows = make([]blameRow, 0, len(lines))
		for i, l := range lines {
			row := blameRow{N: l.N, Hash: l.Hash, Short: l.Short(), Author: l.Author, When: l.When.Format("2006-01-02"), Ago: pg.Ago(l.When), Summary: l.Summary, First: l.First}
			if i < len(code) {
				row.HTML = code[i].HTML
			} else {
				row.HTML = template.HTML(template.HTMLEscapeString(l.Text)) // #nosec G203 -- escaped just above
			}
			d.Rows = append(d.Rows, row)
		}
	}
	pg.Title = pg.T("ui.blame.title", cp) + " · " + rc.Repo.Owner + "/" + rc.Repo.Name + " · " + pg.SiteName
	pg.Data = d
	s.render(w, r, http.StatusOK, "blame.html", pg)
}
