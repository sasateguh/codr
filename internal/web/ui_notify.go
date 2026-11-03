package web

import (
	"errors"
	"net/http"
	"net/mail"
	"strconv"
	"strings"

	"codr.my/codr/internal/store"
)

// Notifications (D79): the bell in the header, the list at /pemberitahuan, the watch switch of a repository and the
// email settings of an account. The notifier in serve writes the rows; these pages only read and mark them.

type notifyData struct {
	Items  []store.Notification
	Total  int
	All    bool // read ones too
	Unread int
	Phrase map[string]string // action → message key
	Why    map[string]string // reason → message key
}

var notifyPhrase = map[string]string{"opened": "ui.notify.did.opened", "closed": "ui.notify.did.closed", "reopened": "ui.notify.did.reopened",
	"merged": "ui.notify.did.merged", "commented": "ui.notify.did.commented", "approved": "ui.notify.did.approved",
	"changes": "ui.notify.did.changes", "reviewed": "ui.notify.did.reviewed", "assigned": "ui.notify.did.assigned"}

var notifyWhy = map[string]string{"author": "ui.notify.why.author", "comment": "ui.notify.why.comment", "mention": "ui.notify.why.mention",
	"watching": "ui.notify.why.watching", "assigned": "ui.notify.why.assigned"}

func (s *Server) uiNotifications(w http.ResponseWriter, r *http.Request) {
	pg := s.newPage(w, r, "")
	if pg.User == nil {
		http.Redirect(w, r, "/login?next=/pemberitahuan", http.StatusSeeOther)
		return
	}
	ctx := r.Context()
	all := r.URL.Query().Get("semua") == "1"
	size, err := s.Store.SettingInt(ctx, "notify.page_size")
	if err != nil || size < 1 {
		size = 50
	}
	items, total, err := s.Store.Notifications(ctx, pg.User.ID, !all, int(size), 0)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	// a row stays, but a repository the person can no longer read is not shown (access is the truth, not the row)
	kept := items[:0]
	for _, n := range items {
		if repo, err := s.Store.RepoByID(ctx, n.RepoID); err == nil {
			if a, err := s.Store.AccessFor(ctx, pg.User.ID, repo); err == nil && a >= store.AccessRead {
				kept = append(kept, n)
			}
		}
	}
	s.takeFlash(w, r, &pg)
	pg.Title = pg.T("ui.notify.title") + " · " + pg.SiteName
	s.render(w, r, http.StatusOK, "notifications.html", pg.with(notifyData{Items: kept, Total: total, All: all, Unread: pg.Unread, Phrase: notifyPhrase, Why: notifyWhy}))
}

// uiNotificationOpen marks one notification read and goes to its thread.
func (s *Server) uiNotificationOpen(w http.ResponseWriter, r *http.Request) {
	pg := s.newPage(w, r, "")
	if pg.User == nil {
		http.Redirect(w, r, "/login?next=/pemberitahuan", http.StatusSeeOther)
		return
	}
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	n, err := s.Store.NotificationByID(r.Context(), pg.User.ID, id)
	if errors.Is(err, store.ErrNotFound) {
		s.errPage(w, r, http.StatusNotFound)
		return
	} else if err != nil {
		s.fail(w, r, err)
		return
	}
	_ = s.Store.MarkRead(r.Context(), pg.User.ID, n.ID)
	path := "/isu/"
	if n.Thread == "pull" {
		path = "/pr/"
	}
	http.Redirect(w, r, "/"+n.Owner+"/"+n.Repo+path+strconv.FormatInt(n.Number, 10), http.StatusSeeOther)
}

// uiNotificationsRead marks one (id) or all notifications read.
func (s *Server) uiNotificationsRead(w http.ResponseWriter, r *http.Request) {
	pg := s.newPage(w, r, "")
	if pg.User == nil || !s.checkCSRF(r) {
		s.errPage(w, r, http.StatusForbidden)
		return
	}
	id, _ := strconv.ParseInt(r.PostFormValue("id"), 10, 64)
	if err := s.Store.MarkRead(r.Context(), pg.User.ID, id); err != nil {
		s.fail(w, r, err)
		return
	}
	if id == 0 {
		s.setFlash(w, "berjaya", pg.T("ui.notify.all_read"))
	}
	http.Redirect(w, r, "/pemberitahuan", http.StatusSeeOther)
}

// markThreadRead is called by the issue and pull request pages: opening the thread reads its notification.
func (s *Server) markThreadRead(r *http.Request, pg *page, rc repoCtx, thread string, number int64) {
	if pg.User != nil && pg.Unread > 0 {
		_ = s.Store.MarkThreadRead(r.Context(), pg.User.ID, rc.Repo.ID, thread, number)
		pg.Unread, _ = s.Store.UnreadCount(r.Context(), pg.User.ID)
	}
}

// uiWatch sets how much of a repository a person hears about.
func (s *Server) uiWatch(w http.ResponseWriter, r *http.Request) {
	pg := s.newPage(w, r, "")
	rc, _, ok := s.openRepo(w, r, pg, "")
	if !ok {
		return
	}
	if pg.User == nil || !s.checkCSRF(r) {
		s.errPage(w, r, http.StatusForbidden)
		return
	}
	level := r.PostFormValue("level")
	if err := s.Store.SetWatch(r.Context(), pg.User.ID, rc.Repo.ID, level); err != nil {
		s.errPage(w, r, http.StatusBadRequest)
		return
	}
	s.setFlash(w, "berjaya", pg.T("ui.watch.saved."+level))
	http.Redirect(w, r, "/"+rc.Repo.Owner+"/"+rc.Repo.Name, http.StatusSeeOther)
}

// uiAccountEmail stores a person's email address and whether they want notification email.
func (s *Server) uiAccountEmail(w http.ResponseWriter, r *http.Request) {
	pg := s.newPage(w, r, "")
	if pg.User == nil || !s.checkCSRF(r) {
		s.errPage(w, r, http.StatusForbidden)
		return
	}
	email := strings.TrimSpace(r.PostFormValue("email"))
	if email != "" {
		a, err := mail.ParseAddress(email)
		if err != nil || a.Address != email || len(email) > 254 {
			s.setFlash(w, "ralat", pg.T("ui.account.email_bad"))
			http.Redirect(w, r, "/akaun#emel", http.StatusSeeOther)
			return
		}
	}
	if err := s.Store.SetEmailSettings(r.Context(), pg.User.ID, email, r.PostFormValue("notify") == "1"); err != nil {
		s.fail(w, r, err)
		return
	}
	_ = s.Store.Audit(r.Context(), pg.User.ID, "account.email", pg.User.Name, "")
	s.setFlash(w, "berjaya", pg.T("ui.account.email_saved"))
	http.Redirect(w, r, "/akaun#emel", http.StatusSeeOther)
}

// kickReaders wakes every outbox reader after an event was written.
func (s *Server) kickReaders() {
	if s.Webhooks != nil {
		s.Webhooks.Kick()
	}
	if s.Notifier != nil {
		s.Notifier.Kick()
	}
	if s.Closer != nil {
		s.Closer.Kick()
	}
}
