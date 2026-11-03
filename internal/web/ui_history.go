package web

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"codr.my/codr/internal/gitread"
	"codr.my/codr/internal/store"
)

type dayGroup struct {
	Day     time.Time
	Commits []gitread.Commit
}

type logData struct {
	repoCtx
	Path    string
	Groups  []dayGroup
	Page    int
	HasMore bool
}

func (s *Server) uiLog(w http.ResponseWriter, r *http.Request) {
	pg := s.newPage(w, r, "")
	rc, g, ok := s.openRepo(w, r, pg, r.PathValue("ref"))
	if !ok {
		return
	}
	cp, good := gitread.CleanPath(strings.TrimSuffix(r.PathValue("path"), "/"))
	if !good {
		s.errPage(w, r, http.StatusNotFound)
		return
	}
	size, err := s.Store.SettingInt(r.Context(), "ui.log_page_size")
	if err != nil || size < 1 {
		size = 40
	}
	n, _ := strconv.Atoi(r.URL.Query().Get("muka"))
	if n < 1 {
		n = 1
	}
	rc.Tab = "commits"
	d := logData{repoCtx: rc, Path: cp, Page: n}
	if rc.Has {
		cs, err := g.Log(r.Context(), rc.Ref, cp, int(size)+1, (n-1)*int(size))
		if err != nil {
			s.fail(w, r, err)
			return
		}
		if len(cs) > int(size) {
			d.HasMore = true
			cs = cs[:size]
		}
		for _, c := range cs {
			day := c.When.UTC().Truncate(24 * time.Hour)
			if len(d.Groups) == 0 || !d.Groups[len(d.Groups)-1].Day.Equal(day) {
				d.Groups = append(d.Groups, dayGroup{Day: day})
			}
			d.Groups[len(d.Groups)-1].Commits = append(d.Groups[len(d.Groups)-1].Commits, c)
		}
	}
	pg.Title = pg.T("ui.log.title", rc.Ref) + " · " + rc.Repo.Owner + "/" + rc.Repo.Name + " · " + pg.SiteName
	pg.Data = d
	s.render(w, r, http.StatusOK, "log.html", pg)
}

type commitData struct {
	repoCtx
	Commit gitread.Commit
	Diff   gitread.Diff
	Max    int64
	Fold   int64                // ui.diff_fold_lines
	Split  bool                 // side-by-side view (?papar=belah)
	Checks []store.CommitStatus // commit statuses from CI (D81)
}

func (s *Server) uiCommit(w http.ResponseWriter, r *http.Request) {
	pg := s.newPage(w, r, "")
	hash := r.PathValue("hash")
	rc, g, ok := s.openRepo(w, r, pg, hash)
	if !ok {
		return
	}
	c, err := g.Commit(r.Context(), hash)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	max, _ := s.Store.SettingInt(r.Context(), "ui.diff_max_lines")
	diff, err := g.Diff(r.Context(), c.Hash, int(max))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	rc.Tab = "commits"
	rc.Ref = c.Hash
	pg.Title = pg.T("ui.commit.title", c.Short()) + " · " + rc.Repo.Owner + "/" + rc.Repo.Name + " · " + pg.SiteName
	fold, _ := s.Store.SettingInt(r.Context(), "ui.diff_fold_lines")
	checks, _ := s.Store.Statuses(r.Context(), rc.Repo.ID, c.Hash)
	pg.Data = commitData{repoCtx: rc, Commit: c, Diff: diff, Max: max, Fold: fold, Split: r.URL.Query().Get("papar") == "belah", Checks: checks}
	s.render(w, r, http.StatusOK, "commit.html", pg)
}

// refsData adds the "open a pull request" affordance per branch (write access, D53).
type refsData struct {
	repoCtx
	CanPull bool
}

func (s *Server) uiRefs(w http.ResponseWriter, r *http.Request) {
	pg := s.newPage(w, r, "")
	rc, _, ok := s.openRepo(w, r, pg, "")
	if !ok {
		return
	}
	rc.Tab = "refs"
	pg.Title = pg.T("ui.repo.tab_refs") + " · " + rc.Repo.Owner + "/" + rc.Repo.Name + " · " + pg.SiteName
	canPull, _ := s.pullCan(pg, rc)
	pg.Data = refsData{repoCtx: rc, CanPull: canPull}
	s.render(w, r, http.StatusOK, "refs.html", pg)
}
