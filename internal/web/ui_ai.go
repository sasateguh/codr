package web

import (
	"context"
	"html/template"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"codr.my/codr/internal/ai"
	"codr.my/codr/internal/gitread"
	"codr.my/codr/internal/i18n"
	"codr.my/codr/internal/store"
)

// The AI assistant on pull requests (D71). Off by default: it needs an endpoint, the switch on /admin/ai, and the
// repository opted in. Only people who may merge can ask; everyone who can read the pull request sees the
// answers, labelled with the model and the head they read. The key is sealed and never rendered back.

type aiNoteView struct {
	store.AINote
	HTML  template.HTML
	Stale bool // the head moved since the model read it
}

type aiAdminData struct {
	Config store.AIConfig
	HasKey bool
	Repos  []store.Repo
	On     map[int64]bool
	NoKey  bool
	Error  string
}

// aiState fills what the pull request page needs from the assistant.
func (s *Server) aiState(r *http.Request, rc repoCtx, d *pullData, head string) {
	ctx := r.Context()
	notes, err := s.Store.AINotes(ctx, d.Pull.ID)
	if err == nil {
		for _, n := range notes {
			d.AINotes = append(d.AINotes, aiNoteView{AINote: n, HTML: s.issueHTML(n.Body, rc), Stale: head != "" && n.HeadSHA != head})
		}
	}
	cfg, err := s.Store.AIConfig(ctx)
	if err != nil || !cfg.Enabled || cfg.Endpoint == "" || cfg.Model == "" {
		return
	}
	if on, err := s.Store.AIRepoEnabled(ctx, rc.Repo.ID); err == nil && on {
		if u, err := url.Parse(cfg.Endpoint); err == nil {
			d.AIHost = u.Host
		}
		d.AIReady = d.CanMerge || d.CanReview || (d.CanEdit && rc.Access >= store.AccessWrite)
	}
}

// diffText rebuilds a unified diff from the parsed one, cut at max bytes.
func diffText(d gitread.Diff, max int) (string, bool) {
	var b strings.Builder
	for _, f := range d.Files {
		old := f.Path
		if f.OldPath != "" {
			old = f.OldPath
		}
		b.WriteString("--- a/" + old + "\n+++ b/" + f.Path + "\n")
		if f.Binary {
			b.WriteString("(binary file)\n")
		}
		for _, l := range f.Lines {
			switch l.Kind {
			case "hunk":
				b.WriteString(l.Text + "\n")
			case "add":
				b.WriteString("+" + l.Text + "\n")
			case "del":
				b.WriteString("-" + l.Text + "\n")
			default:
				b.WriteString(" " + l.Text + "\n")
			}
		}
		if b.Len() > max {
			return b.String()[:max], true
		}
	}
	return b.String(), false
}

// uiPullAI asks the model for a summary or a review of the current diff and stores the answer.
func (s *Server) uiPullAI(w http.ResponseWriter, r *http.Request) {
	pg := s.newPage(w, r, "")
	rc, g, p, ok := s.openPull(w, r, pg)
	if !ok {
		return
	}
	if pg.User == nil {
		http.Redirect(w, r, "/login?next="+s.pullPath(rc, p), http.StatusSeeOther)
		return
	}
	kind := r.PostFormValue("kind")
	_, manage := s.pullCan(pg, rc)
	cfg, err := s.Store.AIConfig(r.Context())
	on, _ := s.Store.AIRepoEnabled(r.Context(), rc.Repo.ID)
	if err != nil || !manage || !s.checkCSRF(r) || !cfg.Enabled || !on || (kind != "summary" && kind != "review") || !p.Open() {
		s.errPage(w, r, http.StatusForbidden)
		return
	}
	key := ""
	if cfg.KeySealed != "" {
		if s.Mirror == nil || s.Mirror.Key == nil {
			s.errPage(w, r, http.StatusForbidden)
			return
		}
		if key, err = s.Mirror.Key.Open(cfg.KeySealed); err != nil {
			s.fail(w, r, err)
			return
		}
	}
	max, _ := s.Store.SettingInt(r.Context(), "pull.diff_max_lines")
	headRef, okHead := s.pullHead(r.Context(), rc, g, p)
	if !okHead {
		s.errPage(w, r, http.StatusConflict)
		return
	}
	diff, err := g.DiffRange(r.Context(), p.BaseBranch, headRef, int(max))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	head, err := g.Resolve(r.Context(), headRef)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	text, cut := diffText(diff, cfg.MaxDiffKB<<10)
	prompt, _ := s.Store.Setting(r.Context(), "ai.prompt_"+kind)
	system := prompt + "\n" + i18n.T(pg.Lang, "ai.answer_in")
	user := "Title: " + p.Title + "\n\nDescription:\n" + p.Body + "\n\nDiff (" + p.BaseBranch + "..." + s.headLabel(r.Context(), p) + "):\n" + text
	if cut {
		user += "\n[the diff was cut at " + strconv.Itoa(cfg.MaxDiffKB) + " KB]"
	}
	answer, err := ai.Complete(r.Context(), nil, ai.Request{Endpoint: cfg.Endpoint, Key: key, Model: cfg.Model, System: system, User: user,
		MaxTokens: 1200, Timeout: time.Duration(cfg.TimeoutSeconds) * time.Second})
	if err != nil {
		s.Log.Warn("ai", "repo", rc.Repo.Owner+"/"+rc.Repo.Name, "pull", p.Number, "err", err)
		s.setFlash(w, "ralat", pg.T("ui.ai.failed", err.Error()))
		http.Redirect(w, r, s.pullPath(rc, p), http.StatusSeeOther)
		return
	}
	id, err := s.Store.AddAINote(r.Context(), store.AINote{PullID: p.ID, Kind: kind, Lang: pg.Lang, Model: cfg.Model, HeadSHA: head, Body: answer, BytesSent: len(system) + len(user)}, pg.User.ID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	_ = s.Store.Audit(context.WithoutCancel(r.Context()), pg.User.ID, "ai."+kind, rc.Repo.Owner+"/"+rc.Repo.Name+"#"+strconv.FormatInt(p.Number, 10),
		cfg.Model+" "+strconv.Itoa(len(system)+len(user))+" bytes")
	http.Redirect(w, r, s.pullPath(rc, p)+"#ai-"+strconv.FormatInt(id, 10), http.StatusSeeOther)
}

func (s *Server) aiAdminPage(w http.ResponseWriter, r *http.Request, p page, d aiAdminData, code int) {
	ctx := r.Context()
	cfg, err := s.Store.AIConfig(ctx)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if d.Error == "" {
		d.Config = cfg
	}
	d.HasKey = cfg.KeySealed != ""
	d.Config.KeySealed = ""
	d.NoKey = s.Mirror == nil || s.Mirror.Key == nil
	if d.Repos, err = s.Store.Repos(ctx, ""); err != nil {
		s.fail(w, r, err)
		return
	}
	if d.On, err = s.Store.AIRepos(ctx); err != nil {
		s.fail(w, r, err)
		return
	}
	p.Title = p.T("ui.ai.title") + " · " + p.SiteName
	p.Data = d
	s.render(w, r, code, "ai.html", p)
}

func (s *Server) uiAIAdmin(w http.ResponseWriter, r *http.Request) {
	p, ok := s.requireAdmin(w, r)
	if !ok {
		return
	}
	s.takeFlash(w, r, &p)
	s.aiAdminPage(w, r, p, aiAdminData{}, http.StatusOK)
}

func (s *Server) uiAIAdminSave(w http.ResponseWriter, r *http.Request) {
	p, ok := s.requireAdmin(w, r)
	if !ok {
		return
	}
	if !s.checkCSRF(r) || s.Mirror == nil || s.Mirror.Key == nil {
		s.errPage(w, r, http.StatusForbidden)
		return
	}
	ctx := r.Context()
	c, err := s.Store.AIConfig(ctx)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	f := func(k string) string { return strings.TrimSpace(r.PostFormValue(k)) }
	c.Endpoint, c.Model, c.Enabled = strings.TrimRight(f("endpoint"), "/"), f("model"), r.PostFormValue("enabled") == "1"
	kb, e1 := strconv.Atoi(f("max_diff_kb"))
	secs, e2 := strconv.Atoi(f("timeout_seconds"))
	c.MaxDiffKB, c.TimeoutSeconds = kb, secs
	bad := ""
	switch {
	case c.Endpoint != "" && !ai.ValidEndpoint(c.Endpoint):
		bad = "ui.ai.bad_endpoint"
	case len(c.Model) > 120 || strings.ContainsAny(c.Model, " \n\t"):
		bad = "ui.ai.bad_model"
	case e1 != nil || kb < 4 || kb > 2048 || e2 != nil || secs < 5 || secs > 600:
		bad = "ui.ai.bad_numbers"
	case c.Enabled && (c.Endpoint == "" || c.Model == ""):
		bad = "ui.ai.not_ready"
	}
	if k := f("key"); bad == "" && k != "" {
		if c.KeySealed, err = s.Mirror.Key.Seal(k); err != nil {
			s.fail(w, r, err)
			return
		}
	}
	if r.PostFormValue("clear_key") == "1" {
		c.KeySealed = ""
	}
	if bad != "" {
		c.KeySealed = ""
		s.aiAdminPage(w, r, p, aiAdminData{Config: c, Error: p.T(bad)}, http.StatusBadRequest)
		return
	}
	if err := s.Store.SetAIConfig(ctx, c); err != nil {
		s.fail(w, r, err)
		return
	}
	var ids []int64
	for _, v := range r.PostForm["repo"] {
		if id, err := strconv.ParseInt(v, 10, 64); err == nil {
			if _, err := s.Store.RepoByID(ctx, id); err == nil {
				ids = append(ids, id)
			}
		}
	}
	if err := s.Store.SetAIRepos(ctx, ids, p.User.ID); err != nil {
		s.fail(w, r, err)
		return
	}
	_ = s.Store.Audit(ctx, p.User.ID, "ai.config", c.Endpoint, "enabled="+strconv.FormatBool(c.Enabled)+" model="+c.Model+" repos="+strconv.Itoa(len(ids)))
	s.setFlash(w, "berjaya", p.T("ui.ai.saved"))
	http.Redirect(w, r, "/admin/ai", http.StatusSeeOther)
}
