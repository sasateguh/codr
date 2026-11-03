package web

import (
	"net/http"
	"strings"
	"unicode/utf8"

	"codr.my/codr/internal/gitread"
)

// searchFile groups content hits under one path.
type searchFile struct {
	Path string
	Hits []gitread.GrepHit
}

type searchData struct {
	repoCtx
	Query    string
	TooShort bool
	Files    []string // paths whose name contains the query
	Content  []searchFile
	NHits    int
	Capped   bool
	MaxHits  int64
	MoreFile bool
}

const searchMinRunes, searchMaxRunes = 2, 100

// uiSearch searches one repo at one ref: file names (substring, case-insensitive, matched here)
// and file contents (git grep, fixed string). Limits are settings ui.search_max_*.
func (s *Server) uiSearch(w http.ResponseWriter, r *http.Request) {
	pg := s.newPage(w, r, "")
	ref := r.URL.Query().Get("ref")
	rc, g, ok := s.openRepo(w, r, pg, ref)
	if !ok {
		return
	}
	rc.Tab = "search"
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if utf8.RuneCountInString(q) > searchMaxRunes {
		q = string([]rune(q)[:searchMaxRunes])
	}
	d := searchData{repoCtx: rc, Query: q}
	d.MaxHits, _ = s.Store.SettingInt(r.Context(), "ui.search_max_hits")
	maxFiles, _ := s.Store.SettingInt(r.Context(), "ui.search_max_files")
	switch {
	case q == "":
	case utf8.RuneCountInString(q) < searchMinRunes:
		d.TooShort = true
	case rc.Has:
		files, err := g.Files(r.Context(), rc.Ref)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		lq := strings.ToLower(q)
		for _, f := range files {
			if strings.Contains(strings.ToLower(f), lq) {
				if int64(len(d.Files)) >= maxFiles {
					d.MoreFile = true
					break
				}
				d.Files = append(d.Files, f)
			}
		}
		hits, capped, err := g.Grep(r.Context(), rc.Ref, q, 5, int(d.MaxHits))
		if err != nil {
			s.fail(w, r, err)
			return
		}
		d.Capped, d.NHits = capped, len(hits)
		for _, h := range hits {
			if n := len(d.Content); n > 0 && d.Content[n-1].Path == h.Path {
				d.Content[n-1].Hits = append(d.Content[n-1].Hits, h)
			} else {
				d.Content = append(d.Content, searchFile{Path: h.Path, Hits: []gitread.GrepHit{h}})
			}
		}
	}
	pg.Title = pg.T("ui.search.title", rc.Repo.Name) + " · " + pg.SiteName
	pg.Data = d
	s.render(w, r, http.StatusOK, "search.html", pg)
}
