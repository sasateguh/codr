package web

import (
	"context"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"codr.my/codr/internal/cloud"
	"codr.my/codr/internal/store"
)

// /admin/sandaran: backups to the owner's own bucket (D70). Secrets are write-only on this page: the bucket
// secret and the recovery key are sealed under auth.secret_key and never rendered back; the recovery key is
// shown exactly once, in the response that created it.

type cloudData struct {
	Config    store.CloudConfig
	Runs      []store.CloudRun
	Running   bool
	HasSecret bool   // a bucket secret is stored
	NoKey     bool   // config.toml has no auth.secret_key: nothing can be sealed
	NewKey    string // shown once
	Error     string
	Ready     bool // every value a run needs is present
}

var (
	bucketRE = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{1,61}[a-z0-9]$`)
	prefixRE = regexp.MustCompile(`^[A-Za-z0-9/_-]{0,64}$`)
	regionRE = regexp.MustCompile(`^[a-z0-9-]{2,32}$`)
)

const cloudRunLimit = 2 * time.Hour

func (s *Server) cloudPage(w http.ResponseWriter, r *http.Request, p page, d cloudData, code int) {
	cfg, err := s.Store.CloudConfig(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if d.Config.Endpoint == "" && d.Error == "" {
		d.Config = cfg
	}
	d.Config.SecretSealed, d.Config.KeySealed = "", "" // never into a template
	d.HasSecret = cfg.SecretSealed != ""
	d.Config.KeyHint = cfg.KeyHint
	d.Ready = cloud.Configured(cfg)
	d.NoKey = s.Cloud == nil || s.Cloud.Secret == nil
	if s.Cloud != nil {
		d.Running = s.Cloud.Running()
	}
	if d.Runs, err = s.Store.CloudRuns(r.Context(), 10); err != nil {
		s.fail(w, r, err)
		return
	}
	p.Title = p.T("ui.cloud.title") + " · " + p.SiteName
	p.Data = d
	s.render(w, r, code, "sandaran.html", p)
}

func (s *Server) uiCloud(w http.ResponseWriter, r *http.Request) {
	p, ok := s.requireAdmin(w, r)
	if !ok {
		return
	}
	s.takeFlash(w, r, &p)
	s.cloudPage(w, r, p, cloudData{}, http.StatusOK)
}

// cloudForm checks an admin POST: admin, CSRF, a key to seal with.
func (s *Server) cloudForm(w http.ResponseWriter, r *http.Request) (page, bool) {
	p, ok := s.requireAdmin(w, r)
	if !ok {
		return p, false
	}
	if !s.checkCSRF(r) || s.Cloud == nil || s.Cloud.Secret == nil {
		s.errPage(w, r, http.StatusForbidden)
		return p, false
	}
	return p, true
}

func validEndpoint(v string) bool {
	u, err := url.Parse(v)
	if err != nil || u.Host == "" || u.User != nil || (u.Path != "" && u.Path != "/") || u.RawQuery != "" {
		return false
	}
	if u.Scheme == "https" {
		return true
	}
	h := u.Hostname()
	return u.Scheme == "http" && (h == "127.0.0.1" || h == "localhost" || h == "::1") // a local test bucket only
}

func (s *Server) uiCloudSave(w http.ResponseWriter, r *http.Request) {
	p, ok := s.cloudForm(w, r)
	if !ok {
		return
	}
	cur, err := s.Store.CloudConfig(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	f := func(k string) string { return strings.TrimSpace(r.PostFormValue(k)) }
	c := cur
	c.Endpoint, c.Region, c.Bucket, c.Prefix, c.AccessKey = strings.TrimRight(f("endpoint"), "/"), f("region"), f("bucket"), strings.Trim(f("prefix"), "/"), f("access_key")
	c.Enabled = r.PostFormValue("enabled") == "1"
	keep, err1 := strconv.Atoi(f("keep"))
	hours, err2 := strconv.Atoi(f("interval_hours"))
	c.Keep, c.IntervalHours = keep, hours
	bad := ""
	switch {
	case c.Endpoint != "" && !validEndpoint(c.Endpoint):
		bad = "ui.cloud.bad_endpoint"
	case c.Bucket != "" && !bucketRE.MatchString(c.Bucket):
		bad = "ui.cloud.bad_bucket"
	case !prefixRE.MatchString(c.Prefix):
		bad = "ui.cloud.bad_prefix"
	case !regionRE.MatchString(c.Region):
		bad = "ui.cloud.bad_region"
	case err1 != nil || keep < 1 || keep > 1000 || err2 != nil || hours < 1 || hours > 720:
		bad = "ui.cloud.bad_numbers"
	case len(c.AccessKey) > 256 || len(f("secret")) > 512:
		bad = "ui.cloud.bad_numbers"
	}
	if sec := f("secret"); bad == "" && sec != "" {
		if c.SecretSealed, err = s.Cloud.Secret.Seal(sec); err != nil {
			s.fail(w, r, err)
			return
		}
	}
	if bad == "" && c.Enabled && !cloud.Configured(c) {
		bad = "ui.cloud.not_ready"
	}
	if bad != "" {
		c.SecretSealed, c.KeySealed = "", ""
		s.cloudPage(w, r, p, cloudData{Config: c, Error: p.T(bad)}, http.StatusBadRequest)
		return
	}
	if err := s.Store.SetCloudConfig(r.Context(), c); err != nil {
		s.fail(w, r, err)
		return
	}
	_ = s.Store.Audit(r.Context(), p.User.ID, "cloud.config", c.Endpoint+"/"+c.Bucket, "enabled="+strconv.FormatBool(c.Enabled))
	s.setFlash(w, "berjaya", p.T("ui.cloud.saved"))
	http.Redirect(w, r, "/admin/sandaran", http.StatusSeeOther)
}

func (s *Server) uiCloudKey(w http.ResponseWriter, r *http.Request) {
	p, ok := s.cloudForm(w, r)
	if !ok {
		return
	}
	c, err := s.Store.CloudConfig(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	k, err := cloud.NewRecoveryKey()
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if c.KeySealed, err = s.Cloud.Secret.Seal(k); err != nil {
		s.fail(w, r, err)
		return
	}
	c.KeyHint = cloud.KeyHint(k)
	if err := s.Store.SetCloudConfig(r.Context(), c); err != nil {
		s.fail(w, r, err)
		return
	}
	_ = s.Store.Audit(r.Context(), p.User.ID, "cloud.key", c.KeyHint, "new recovery key")
	w.Header().Set("Cache-Control", "no-store, no-transform")
	s.cloudPage(w, r, p, cloudData{NewKey: groupKey(k)}, http.StatusOK)
}

// groupKey writes a 64-hex key in groups of eight, easier to copy onto paper.
func groupKey(k string) string {
	var parts []string
	for i := 0; i+8 <= len(k); i += 8 {
		parts = append(parts, k[i:i+8])
	}
	return strings.Join(parts, " ")
}

func (s *Server) uiCloudTest(w http.ResponseWriter, r *http.Request) {
	p, ok := s.cloudForm(w, r)
	if !ok {
		return
	}
	c, err := s.Store.CloudConfig(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	cl, err := s.Cloud.Client(c)
	if err == nil {
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()
		var objs []cloud.Object
		if objs, err = cl.List(ctx, cloud.ObjectPrefix(c)); err == nil {
			s.setFlash(w, "berjaya", p.N("ui.cloud.test_ok", len(objs)))
			http.Redirect(w, r, "/admin/sandaran", http.StatusSeeOther)
			return
		}
	}
	s.setFlash(w, "ralat", p.T("ui.cloud.test_failed", err.Error()))
	http.Redirect(w, r, "/admin/sandaran", http.StatusSeeOther)
}

func (s *Server) uiCloudRun(w http.ResponseWriter, r *http.Request) {
	p, ok := s.cloudForm(w, r)
	if !ok {
		return
	}
	if s.Cloud.Running() {
		s.setFlash(w, "ralat", p.T("ui.cloud.busy"))
		http.Redirect(w, r, "/admin/sandaran", http.StatusSeeOther)
		return
	}
	user := p.User.ID
	go func() { // the request returns now; the run is recorded in cloud_backup_runs
		ctx, cancel := context.WithTimeout(context.Background(), cloudRunLimit)
		defer cancel()
		if out, err := s.Cloud.Run(ctx, "admin"); err != nil {
			s.Log.Error("cloud backup", "err", err, "by", user)
		} else {
			s.Log.Info("cloud backup", "object", out.Object, "bytes", out.Bytes, "by", user)
		}
	}()
	s.setFlash(w, "berjaya", p.T("ui.cloud.started"))
	http.Redirect(w, r, "/admin/sandaran", http.StatusSeeOther)
}
