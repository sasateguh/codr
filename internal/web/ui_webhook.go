package web

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"codr.my/codr/internal/store"
	"codr.my/codr/internal/webhook"
)

// Webhooks of a repository (D78): admins of the repository add a URL, Codr signs every delivery with a secret it
// generates and shows once, and the page lists each attempt with the receiver's answer. Everyone below admin gets 404,
// as for any page they cannot use: the URLs may point at someone's private systems.

// kindOpt is one event checkbox: the wire name and its label.
type kindOpt struct{ Name, Label string }

func hookKinds(pg page) []kindOpt {
	out := make([]kindOpt, 0, len(webhook.Kinds))
	for _, k := range webhook.Kinds {
		out = append(out, kindOpt{k, pg.T("ui.hook.kind." + k)})
	}
	return out
}

type webhooksData struct {
	repoCtx
	Hooks     []store.Webhook
	Kinds     []kindOpt
	Error     string
	URL       string
	Checked   map[string]bool
	All       bool
	NewSecret string
	NoKey     bool
	Limit     int64
}

type webhookData struct {
	repoCtx
	Hook       store.Webhook
	Kinds      []kindOpt
	Checked    map[string]bool
	All        bool
	Deliveries []store.Delivery
	Error      string
	NewSecret  string
}

// openHooks opens the repository for a webhook page: admins only, 404 otherwise.
func (s *Server) openHooks(w http.ResponseWriter, r *http.Request, pg page) (repoCtx, bool) {
	rc, _, ok := s.openRepo(w, r, pg, "")
	if !ok {
		return rc, false
	}
	if pg.User == nil || rc.Access < store.AccessAdmin {
		s.errPage(w, r, http.StatusNotFound)
		return rc, false
	}
	rc.Tab = "webhooks"
	return rc, true
}

func (s *Server) hooksPath(rc repoCtx) string {
	return "/" + rc.Repo.Owner + "/" + rc.Repo.Name + "/webhook"
}

func (s *Server) builder() webhook.Builder {
	return webhook.Builder{Store: s.Store, HTTPHost: s.UI.HTTPHost, SSHHost: s.UI.SSHHost, Git: s.git}
}

func newHookSecret() (string, error) {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return "whsec_" + hex.EncodeToString(b), nil
}

func (s *Server) renderHooks(w http.ResponseWriter, r *http.Request, pg page, rc repoCtx, d webhooksData, code int) {
	var err error
	d.repoCtx = rc
	if d.Hooks, err = s.Store.Webhooks(r.Context(), rc.Repo.ID); err != nil {
		s.fail(w, r, err)
		return
	}
	d.Kinds = hookKinds(pg)
	d.NoKey = s.Mirror == nil || s.Mirror.Key == nil
	d.Limit, _ = s.Store.SettingInt(r.Context(), "webhook.per_repo")
	if d.Checked == nil {
		d.All = true
	}
	pg.Title = pg.T("ui.hook.title") + " · " + rc.Repo.Name + " · " + pg.SiteName
	s.render(w, r, code, "webhooks.html", pg.with(d))
}

func (s *Server) uiWebhooks(w http.ResponseWriter, r *http.Request) {
	pg := s.newPage(w, r, "")
	rc, ok := s.openHooks(w, r, pg)
	if !ok {
		return
	}
	s.takeFlash(w, r, &pg)
	s.renderHooks(w, r, pg, rc, webhooksData{}, http.StatusOK)
}

// hookForm reads URL + events from a form; nothing ticked = every kind (no separate "all" box to forget, D78).
func hookForm(r *http.Request) (url, events string, checked map[string]bool, all bool) {
	_ = r.ParseForm()
	url = strings.TrimSpace(r.PostFormValue("url"))
	kinds := webhook.ValidKinds(r.PostForm["events"])
	all = len(kinds) == 0
	checked = map[string]bool{}
	for _, k := range kinds {
		checked[k] = true
	}
	if all {
		return url, "*", checked, true
	}
	return url, strings.Join(kinds, ","), checked, false
}

func (s *Server) hookURLError(pg page, err error) string {
	if errors.Is(err, webhook.ErrPrivate) {
		return pg.T("ui.hook.private")
	}
	return pg.T("ui.hook.bad_url")
}

func (s *Server) uiWebhookCreate(w http.ResponseWriter, r *http.Request) {
	pg := s.newPage(w, r, "")
	rc, ok := s.openHooks(w, r, pg)
	if !ok {
		return
	}
	if !s.checkCSRF(r) {
		s.errPage(w, r, http.StatusForbidden)
		return
	}
	url, events, checked, all := hookForm(r)
	bad := func(msg string) {
		s.renderHooks(w, r, pg, rc, webhooksData{Error: msg, URL: url, Checked: checked, All: all}, http.StatusBadRequest)
	}
	if s.Mirror == nil || s.Mirror.Key == nil {
		bad(pg.T("ui.hook.no_key"))
		return
	}
	if _, err := webhook.PolicyFrom(r.Context(), s.Store).CheckURL(url); err != nil {
		bad(s.hookURLError(pg, err))
		return
	}
	existing, err := s.Store.Webhooks(r.Context(), rc.Repo.ID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if limit, _ := s.Store.SettingInt(r.Context(), "webhook.per_repo"); limit > 0 && int64(len(existing)) >= limit {
		bad(pg.T("ui.hook.too_many", limit))
		return
	}
	plain, err := newHookSecret()
	if err != nil {
		s.fail(w, r, err)
		return
	}
	sealed, err := s.Mirror.Key.Seal(plain)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	id, err := s.Store.CreateWebhook(r.Context(), store.Webhook{RepoID: rc.Repo.ID, URL: url, SecretSealed: sealed, Events: events, Active: true, CreatedBy: pg.User.ID})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	_ = s.Store.Audit(r.Context(), pg.User.ID, "webhook.add", rc.Repo.Owner+"/"+rc.Repo.Name, url)
	if h, err := s.Store.WebhookByID(r.Context(), rc.Repo.ID, id); err == nil {
		s.queuePing(r, rc, h, pg.User.ID)
	}
	s.renderHook(w, r, pg, rc, id, webhookData{NewSecret: plain}, http.StatusOK)
}

// queuePing queues a ping delivery: sent on creation and from the Test button.
func (s *Server) queuePing(r *http.Request, rc repoCtx, h store.Webhook, actorID int64) {
	body, err := s.builder().Ping(r.Context(), rc.Repo, h, actorID)
	if err == nil {
		err = s.Store.QueueDelivery(r.Context(), store.NewDelivery{WebhookID: h.ID, Kind: "ping", GUID: webhook.GUID(), Payload: string(body)})
	}
	if err != nil {
		s.Log.Error("webhook: ping", "err", err)
		return
	}
	if s.Webhooks != nil {
		s.Webhooks.Kick()
	}
}

func hookID(r *http.Request) int64 {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	return id
}

func (s *Server) renderHook(w http.ResponseWriter, r *http.Request, pg page, rc repoCtx, id int64, d webhookData, code int) {
	h, err := s.Store.WebhookByID(r.Context(), rc.Repo.ID, id)
	if errors.Is(err, store.ErrNotFound) {
		s.errPage(w, r, http.StatusNotFound)
		return
	} else if err != nil {
		s.fail(w, r, err)
		return
	}
	d.repoCtx, d.Hook, d.Kinds = rc, h, hookKinds(pg)
	if d.Checked == nil {
		d.Checked = map[string]bool{}
		for _, k := range h.EventList() {
			d.Checked[k] = true
		}
		d.All = h.Events == "*"
	}
	if d.Deliveries, err = s.Store.Deliveries(r.Context(), h.ID, s.hookPageSize(r)); err != nil {
		s.fail(w, r, err)
		return
	}
	pg.Title = pg.T("ui.hook.title") + " · " + rc.Repo.Name + " · " + pg.SiteName
	s.render(w, r, code, "webhook.html", pg.with(d))
}

func (s *Server) uiWebhook(w http.ResponseWriter, r *http.Request) {
	pg := s.newPage(w, r, "")
	rc, ok := s.openHooks(w, r, pg)
	if !ok {
		return
	}
	s.takeFlash(w, r, &pg)
	s.renderHook(w, r, pg, rc, hookID(r), webhookData{}, http.StatusOK)
}

// uiWebhookAction handles the buttons of one webhook: save, test, new secret, redeliver, delete.
func (s *Server) uiWebhookAction(w http.ResponseWriter, r *http.Request) {
	pg := s.newPage(w, r, "")
	rc, ok := s.openHooks(w, r, pg)
	if !ok {
		return
	}
	if !s.checkCSRF(r) {
		s.errPage(w, r, http.StatusForbidden)
		return
	}
	ctx, id := r.Context(), hookID(r)
	h, err := s.Store.WebhookByID(ctx, rc.Repo.ID, id)
	if errors.Is(err, store.ErrNotFound) {
		s.errPage(w, r, http.StatusNotFound)
		return
	} else if err != nil {
		s.fail(w, r, err)
		return
	}
	target := rc.Repo.Owner + "/" + rc.Repo.Name
	back := s.hooksPath(rc) + "/" + strconv.FormatInt(id, 10)
	switch r.PathValue("action") {
	case "simpan":
		url, events, checked, all := hookForm(r)
		if _, err := webhook.PolicyFrom(ctx, s.Store).CheckURL(url); err != nil {
			s.renderHook(w, r, pg, rc, id, webhookData{Error: s.hookURLError(pg, err), Checked: checked, All: all}, http.StatusBadRequest)
			return
		}
		if err := s.Store.UpdateWebhook(ctx, rc.Repo.ID, id, url, events, r.PostFormValue("active") == "1"); err != nil {
			s.fail(w, r, err)
			return
		}
		_ = s.Store.Audit(ctx, pg.User.ID, "webhook.edit", target, url)
		s.setFlash(w, "berjaya", pg.T("ui.hook.saved"))
	case "uji":
		s.queuePing(r, rc, h, pg.User.ID)
		s.setFlash(w, "berjaya", pg.T("ui.hook.ping_queued"))
	case "rahsia":
		if s.Mirror == nil || s.Mirror.Key == nil {
			s.renderHook(w, r, pg, rc, id, webhookData{Error: pg.T("ui.hook.no_key")}, http.StatusBadRequest)
			return
		}
		plain, err := newHookSecret()
		if err == nil {
			var sealed string
			if sealed, err = s.Mirror.Key.Seal(plain); err == nil {
				err = s.Store.SetWebhookSecret(ctx, rc.Repo.ID, id, sealed)
			}
		}
		if err != nil {
			s.fail(w, r, err)
			return
		}
		_ = s.Store.Audit(ctx, pg.User.ID, "webhook.secret", target, h.URL)
		s.renderHook(w, r, pg, rc, id, webhookData{NewSecret: plain}, http.StatusOK)
		return
	case "hantar":
		did, _ := strconv.ParseInt(r.PostFormValue("delivery"), 10, 64)
		dl, err := s.Store.DeliveryByID(ctx, id, did)
		if errors.Is(err, store.ErrNotFound) {
			s.errPage(w, r, http.StatusNotFound)
			return
		} else if err != nil {
			s.fail(w, r, err)
			return
		}
		if err := s.Store.QueueDelivery(ctx, store.NewDelivery{WebhookID: id, EventID: dl.EventID, Kind: dl.Kind, GUID: webhook.GUID(), Payload: dl.Payload}); err != nil {
			s.fail(w, r, err)
			return
		}
		if s.Webhooks != nil {
			s.Webhooks.Kick()
		}
		s.setFlash(w, "berjaya", pg.T("ui.hook.redelivered"))
	case "padam":
		if err := s.Store.DeleteWebhook(ctx, rc.Repo.ID, id); err != nil {
			s.fail(w, r, err)
			return
		}
		_ = s.Store.Audit(ctx, pg.User.ID, "webhook.remove", target, h.URL)
		s.setFlash(w, "berjaya", pg.T("ui.hook.removed"))
		back = s.hooksPath(rc)
	default:
		s.errPage(w, r, http.StatusNotFound)
		return
	}
	http.Redirect(w, r, back, http.StatusSeeOther)
}

func (s *Server) hookPageSize(r *http.Request) int {
	size, err := s.Store.SettingInt(r.Context(), "ui.page_size")
	if err != nil || size < 1 {
		return 50
	}
	return int(size)
}
