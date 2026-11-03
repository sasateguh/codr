package web

import (
	"net/http"
	"strconv"
	"strings"

	"codr.my/codr/internal/store"
)

// /akaun/aktiviti (D86, FR-25): a person's own audit trail as a page, the same rows the admin audit log holds but only
// those they did. The export /akaun/data.json carries the same rows in full.

// activityAreas are the audit action prefixes a person can filter by; each has a ui.activity.area.<name> label. An
// action outside the list is shown under "other".
var activityAreas = []string{"git", "issue", "pr", "assign", "repo", "wiki", "user", "key", "token", "account", "org", "collab",
	"protect", "webhook", "mirror", "github", "data", "backup", "cloud", "mail", "ai", "oidc", "setting"}

type activityRow struct {
	store.AuditRow
	Area string
}

type activityData struct {
	Rows  []activityRow
	Areas []string
	Area  string
	Page  int
	Pages int
	Total int
}

// Link is the page with one area filter and page number.
func (d activityData) Link(area string, page int) string {
	q := []string{}
	if area != "" {
		q = append(q, "jenis="+area)
	}
	if page > 1 {
		q = append(q, "muka="+strconv.Itoa(page))
	}
	if len(q) == 0 {
		return "/akaun/aktiviti"
	}
	return "/akaun/aktiviti?" + strings.Join(q, "&")
}

func areaOf(action string) string {
	prefix, _, _ := strings.Cut(action, ".")
	for _, a := range activityAreas {
		if a == prefix {
			return a
		}
	}
	return "other"
}

func (s *Server) uiActivity(w http.ResponseWriter, r *http.Request) {
	p, ok := s.requireUser(w, r)
	if !ok {
		return
	}
	d := activityData{}
	have, err := s.Store.AuditPrefixes(r.Context(), p.User.ID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	for _, a := range activityAreas { // only the kinds this person has rows of: a filter that lists nothing is noise
		if have[a] {
			d.Areas = append(d.Areas, a)
		}
	}
	if a := r.URL.Query().Get("jenis"); a != "" {
		for _, x := range activityAreas {
			if x == a {
				d.Area = a
			}
		}
	}
	size := int(s.settingInt(r.Context(), "account.activity_page_size", 50))
	d.Page, _ = strconv.Atoi(r.URL.Query().Get("muka"))
	if d.Page < 1 {
		d.Page = 1
	}
	rows, total, err := s.Store.AuditOf(r.Context(), p.User.ID, d.Area, size, (d.Page-1)*size)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	for _, row := range rows {
		d.Rows = append(d.Rows, activityRow{AuditRow: row, Area: areaOf(row.Action)})
	}
	d.Total, d.Pages = total, (total+size-1)/size
	p.Title = p.T("ui.activity.title") + " · " + p.SiteName
	s.render(w, r, http.StatusOK, "activity.html", p.with(d))
}
