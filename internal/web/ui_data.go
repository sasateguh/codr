package web

import (
	"encoding/json"
	"net/http"
	"net/url"
	"path/filepath"
	"sort"
	"syscall"
	"time"

	"codr.my/codr/internal/store"
)

// The data report (D73): where the data sits, who can read what, the privacy statement from settings, the audit
// trail as CSV and the whole report as JSON; and, for every person, a download of what Codr holds about them.

type dataSummary struct {
	Controller, Location, Contact string
	DataDir                       string
	DBBytes                       int64
	DiskFree                      int64
	Repos, Private, Mirrors       int
	MirrorHosts                   []string
	Users                         int
	Admins                        []string
	CloudHost, CloudBucket        string
	CloudLastOK                   string
	AIHost                        string
	AIRepos                       int
	GeneratedAt                   string
}

type dataPage struct {
	Summary dataSummary
	Matrix  []store.AccessRow
}

func (s *Server) dataReport(r *http.Request) (dataPage, error) {
	ctx := r.Context()
	var d dataPage
	set := func(k string) string { v, _ := s.Store.Setting(ctx, k); return v }
	sm := &d.Summary
	sm.Controller, sm.Location, sm.Contact = set("privacy.controller"), set("privacy.data_location"), set("privacy.contact")
	sm.GeneratedAt = time.Now().UTC().Format(time.RFC3339)
	sm.DataDir = filepath.Dir(s.Disk.Root)
	sm.DBBytes, _ = s.Store.DBBytes(ctx)
	var fs syscall.Statfs_t
	if err := syscall.Statfs(s.Disk.Root, &fs); err == nil {
		sm.DiskFree = int64(fs.Bavail) * int64(fs.Bsize) // #nosec G115 -- block counts fit in int64
	}
	var err error
	if d.Matrix, err = s.Store.AccessMatrix(ctx); err != nil {
		return d, err
	}
	sm.Repos = len(d.Matrix)
	for _, a := range d.Matrix {
		if a.Private {
			sm.Private++
		}
		if a.Mirror {
			sm.Mirrors++
		}
	}
	hosts := map[string]bool{}
	if ms, err := s.Store.Mirrors(ctx); err == nil {
		for _, m := range ms {
			if u, err := url.Parse(m.URL); err == nil {
				h := u.Host
				if u.Scheme == "file" {
					h = "file://" + filepath.Dir(u.Path)
				}
				hosts[h] = true
			}
		}
	}
	for h := range hosts {
		sm.MirrorHosts = append(sm.MirrorHosts, h)
	}
	sort.Strings(sm.MirrorHosts)
	if us, err := s.Store.Users(ctx); err == nil {
		sm.Users = len(us)
		for _, u := range us {
			if u.IsAdmin && !u.Disabled {
				sm.Admins = append(sm.Admins, u.Name)
			}
		}
	}
	if c, err := s.Store.CloudConfig(ctx); err == nil && c.Endpoint != "" {
		if u, err := url.Parse(c.Endpoint); err == nil {
			sm.CloudHost = u.Host
		}
		sm.CloudBucket = c.Bucket
		if runs, err := s.Store.CloudRuns(ctx, 50); err == nil {
			for _, run := range runs {
				if run.Status == "ok" {
					sm.CloudLastOK = run.StartedAt
					break
				}
			}
		}
	}
	if c, err := s.Store.AIConfig(ctx); err == nil && c.Enabled {
		if u, err := url.Parse(c.Endpoint); err == nil {
			sm.AIHost = u.Host
		}
		if on, err := s.Store.AIRepos(ctx); err == nil {
			sm.AIRepos = len(on)
		}
	}
	return d, nil
}

func (s *Server) uiData(w http.ResponseWriter, r *http.Request) {
	p, ok := s.requireAdmin(w, r)
	if !ok {
		return
	}
	d, err := s.dataReport(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	p.Title = p.T("ui.data.title") + " · " + p.SiteName
	p.Data = d
	s.render(w, r, http.StatusOK, "data.html", p)
}

func attachment(w http.ResponseWriter, name, contentType string) {
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	w.Header().Set("Cache-Control", "no-store, no-transform")
}

func (s *Server) uiDataAuditCSV(w http.ResponseWriter, r *http.Request) {
	p, ok := s.requireAdmin(w, r)
	if !ok {
		return
	}
	_ = s.Store.Audit(r.Context(), p.User.ID, "data.audit_export", "audit_log", "csv")
	attachment(w, "codr-audit-"+time.Now().UTC().Format("20060102")+".csv", "text/csv; charset=utf-8")
	if err := s.Store.WriteAuditCSV(r.Context(), w); err != nil {
		s.Log.Error("audit csv", "err", err)
	}
}

func (s *Server) uiDataJSON(w http.ResponseWriter, r *http.Request) {
	p, ok := s.requireAdmin(w, r)
	if !ok {
		return
	}
	d, err := s.dataReport(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	_ = s.Store.Audit(r.Context(), p.User.ID, "data.report_export", "report", "json")
	attachment(w, "codr-data-report-"+time.Now().UTC().Format("20060102")+".json", "application/json; charset=utf-8")
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(d)
}

// uiMyData is the personal data download on /akaun (PDPA right of access).
func (s *Server) uiMyData(w http.ResponseWriter, r *http.Request) {
	p := s.newPage(w, r, "")
	if p.User == nil {
		http.Redirect(w, r, "/login?next=/akaun", http.StatusSeeOther)
		return
	}
	d, err := s.Store.PersonalData(r.Context(), p.User.ID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	d["generated_at"] = time.Now().UTC().Format(time.RFC3339)
	d["note"] = p.T("ui.data.export_note")
	_ = s.Store.Audit(r.Context(), p.User.ID, "data.personal_export", p.User.Name, "json")
	attachment(w, "codr-"+p.User.Name+"-data.json", "application/json; charset=utf-8")
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(d)
}
