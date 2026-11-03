package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
)

// Notification is one person's row for one issue or pull request thread (D79).
type Notification struct {
	ID         int64
	UserID     int64
	RepoID     int64
	Owner      string
	Repo       string
	Thread     string // "issue" | "pull"
	Number     int64
	Title      string
	Reason     string // author | comment | mention | watching | assigned (D85)
	LastKind   string
	LastAction string
	Actor      string
	Excerpt    string
	UpdatedAt  string
	Read       bool
}

// NotifyRow is what the notifier writes.
type NotifyRow struct {
	UserID, RepoID, Number, ActorID, EventID int64
	Thread, Title, Reason, Kind, Action      string
	Excerpt                                  string
}

// UpsertNotification records that a thread moved for one person: a new row, or the existing one brought back to
// unread with the latest action. A "mention" reason is kept once given (it is the strongest).
func (s *Store) UpsertNotification(ctx context.Context, n NotifyRow) error {
	var actor any
	if n.ActorID > 0 {
		actor = n.ActorID
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO notifications (user_id, repo_id, thread, number, title, reason, last_kind, last_action, actor_id, event_id, excerpt, updated_at, email_due)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 1)
		ON CONFLICT(user_id, repo_id, thread, number) DO UPDATE SET title = excluded.title, last_kind = excluded.last_kind,
			last_action = excluded.last_action, actor_id = excluded.actor_id, event_id = excluded.event_id, excerpt = excluded.excerpt,
			updated_at = excluded.updated_at, read_at = NULL, email_due = 1,
			reason = CASE WHEN notifications.reason = 'mention' THEN 'mention' ELSE excluded.reason END`,
		n.UserID, n.RepoID, n.Thread, n.Number, n.Title, n.Reason, n.Kind, n.Action, actor, n.EventID, n.Excerpt, Now())
	return err
}

const notifCols = `n.id, n.user_id, n.repo_id, o.name, r.name, n.thread, n.number, n.title, n.reason, n.last_kind, n.last_action,
	COALESCE(a.name, ''), n.excerpt, n.updated_at, n.read_at IS NOT NULL`

func scanNotif(sc interface{ Scan(...any) error }) (Notification, error) {
	var n Notification
	err := sc.Scan(&n.ID, &n.UserID, &n.RepoID, &n.Owner, &n.Repo, &n.Thread, &n.Number, &n.Title, &n.Reason, &n.LastKind, &n.LastAction,
		&n.Actor, &n.Excerpt, &n.UpdatedAt, &n.Read)
	return n, err
}

// Notifications lists a person's notifications, unread first then newest. unread = only unread ones. Rows of
// repositories the person can no longer read are left out (access is checked again by the caller before showing).
func (s *Store) Notifications(ctx context.Context, userID int64, unread bool, limit, offset int) ([]Notification, int, error) {
	where := `n.user_id = ?`
	if unread {
		where += ` AND n.read_at IS NULL`
	}
	var total int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM notifications n WHERE `+where, userID).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+notifCols+` FROM notifications n JOIN repos r ON r.id = n.repo_id JOIN users o ON o.id = r.owner_id
		LEFT JOIN users a ON a.id = n.actor_id WHERE `+where+` ORDER BY n.read_at IS NOT NULL, n.updated_at DESC, n.id DESC LIMIT ? OFFSET ?`, userID, limit, offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var out []Notification
	for rows.Next() {
		n, err := scanNotif(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, n)
	}
	return out, total, rows.Err()
}

// NotificationByID finds one of a person's notifications.
func (s *Store) NotificationByID(ctx context.Context, userID, id int64) (Notification, error) {
	n, err := scanNotif(s.db.QueryRowContext(ctx, `SELECT `+notifCols+` FROM notifications n JOIN repos r ON r.id = n.repo_id JOIN users o ON o.id = r.owner_id
		LEFT JOIN users a ON a.id = n.actor_id WHERE n.user_id = ? AND n.id = ?`, userID, id))
	if errors.Is(err, sql.ErrNoRows) {
		return n, ErrNotFound
	}
	return n, err
}

// UnreadCount is the number on the bell.
func (s *Store) UnreadCount(ctx context.Context, userID int64) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM notifications WHERE user_id = ? AND read_at IS NULL`, userID).Scan(&n)
	return n, err
}

// MarkRead marks one notification read (id > 0) or all of a person's (id = 0). Reading also cancels a pending email.
func (s *Store) MarkRead(ctx context.Context, userID, id int64) error {
	q, args := `UPDATE notifications SET read_at = ?, email_due = 0 WHERE user_id = ? AND read_at IS NULL`, []any{Now(), userID}
	if id > 0 {
		q += ` AND id = ?`
		args = append(args, id)
	}
	_, err := s.db.ExecContext(ctx, q, args...)
	return err
}

// MarkThreadRead marks a person's notification for one thread read (they opened the page).
func (s *Store) MarkThreadRead(ctx context.Context, userID, repoID int64, thread string, number int64) error {
	_, err := s.db.ExecContext(ctx, `UPDATE notifications SET read_at = ?, email_due = 0 WHERE user_id = ? AND repo_id = ? AND thread = ? AND number = ? AND read_at IS NULL`,
		Now(), userID, repoID, thread, number)
	return err
}

// Watch levels.
const (
	WatchAll           = "all"
	WatchParticipating = "participating"
	WatchIgnore        = "ignore"
)

// WatchLevel is a person's level for a repository, with the default applied: the owner of a personal repository
// watches everything, everyone else hears about threads they take part in.
func (s *Store) WatchLevel(ctx context.Context, userID int64, repo Repo) (string, error) {
	var lvl string
	err := s.db.QueryRowContext(ctx, `SELECT level FROM repo_watch WHERE user_id = ? AND repo_id = ?`, userID, repo.ID).Scan(&lvl)
	if errors.Is(err, sql.ErrNoRows) {
		if repo.OwnerID == userID {
			return WatchAll, nil
		}
		return WatchParticipating, nil
	}
	return lvl, err
}

// SetWatch stores a person's level for a repository.
func (s *Store) SetWatch(ctx context.Context, userID, repoID int64, level string) error {
	if level != WatchAll && level != WatchParticipating && level != WatchIgnore {
		return errors.New("store: bad watch level")
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO repo_watch (user_id, repo_id, level, updated_at) VALUES (?, ?, ?, ?)
		ON CONFLICT(user_id, repo_id) DO UPDATE SET level = excluded.level, updated_at = excluded.updated_at`, userID, repoID, level, Now())
	return err
}

// WatchRows returns the explicit levels for a repository (user id → level).
func (s *Store) WatchRows(ctx context.Context, repoID int64) (map[int64]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT user_id, level FROM repo_watch WHERE repo_id = ?`, repoID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]string{}
	for rows.Next() {
		var id int64
		var l string
		if err := rows.Scan(&id, &l); err != nil {
			return nil, err
		}
		out[id] = l
	}
	return out, rows.Err()
}

// ThreadParticipants lists everyone who wrote in a thread: its author, commenters, reviewers and line commenters.
func (s *Store) ThreadParticipants(ctx context.Context, repoID int64, thread string, number int64) ([]int64, error) {
	var q string
	if thread == "pull" {
		q = `SELECT p.author_id FROM pulls p WHERE p.repo_id = ?1 AND p.number = ?2
			UNION SELECT c.author_id FROM pull_comments c JOIN pulls p ON p.id = c.pull_id WHERE p.repo_id = ?1 AND p.number = ?2
			UNION SELECT v.reviewer_id FROM pull_reviews v JOIN pulls p ON p.id = v.pull_id WHERE p.repo_id = ?1 AND p.number = ?2
			UNION SELECT l.author_id FROM pull_line_comments l JOIN pulls p ON p.id = l.pull_id WHERE p.repo_id = ?1 AND p.number = ?2`
	} else {
		q = `SELECT i.author_id FROM issues i WHERE i.repo_id = ?1 AND i.number = ?2
			UNION SELECT c.author_id FROM issue_comments c JOIN issues i ON i.id = c.issue_id WHERE i.repo_id = ?1 AND i.number = ?2`
	}
	rows, err := s.db.QueryContext(ctx, q, repoID, number)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// ThreadAuthor is the author of an issue or pull request (0 when imported or gone).
func (s *Store) ThreadAuthor(ctx context.Context, repoID int64, thread string, number int64) int64 {
	table := "issues"
	if thread == "pull" {
		table = "pulls"
	}
	var id int64
	_ = s.db.QueryRowContext(ctx, `SELECT author_id FROM `+table+` WHERE repo_id = ? AND number = ?`, repoID, number).Scan(&id) // #nosec G202 -- table is one of two constants
	return id
}

// ActiveUsersByName maps the given names to account ids (people only: no organisations, no disabled accounts).
func (s *Store) ActiveUsersByName(ctx context.Context, names []string) (map[string]int64, error) {
	out := map[string]int64{}
	if len(names) == 0 {
		return out, nil
	}
	args := make([]any, len(names))
	for i, n := range names {
		args[i] = strings.ToLower(n)
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id, name FROM users WHERE kind = 'user' AND disabled_at IS NULL AND lower(name) IN (?`+strings.Repeat(",?", len(names)-1)+`)`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var n string
		if err := rows.Scan(&id, &n); err != nil {
			return nil, err
		}
		out[strings.ToLower(n)] = id
	}
	return out, rows.Err()
}

// MailItem is a notification waiting for an email, with what the mail needs.
type MailItem struct {
	Notification
	Email     string
	Lang      string
	EmailedAt string
}

// MailDue lists notifications marked for email whose person wants email and has an address, oldest first.
func (s *Store) MailDue(ctx context.Context, limit int) ([]MailItem, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+notifCols+`, COALESCE(u.email, ''), u.lang, COALESCE(n.emailed_at, '')
		FROM notifications n JOIN repos r ON r.id = n.repo_id JOIN users o ON o.id = r.owner_id LEFT JOIN users a ON a.id = n.actor_id
		JOIN users u ON u.id = n.user_id
		WHERE n.email_due = 1 ORDER BY n.updated_at LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []MailItem
	for rows.Next() {
		var m MailItem
		err := rows.Scan(&m.ID, &m.UserID, &m.RepoID, &m.Owner, &m.Repo, &m.Thread, &m.Number, &m.Title, &m.Reason, &m.LastKind, &m.LastAction,
			&m.Actor, &m.Excerpt, &m.UpdatedAt, &m.Read, &m.Email, &m.Lang, &m.EmailedAt)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// MailDone clears the email flag; sent = true also records when (the thread cooldown counts from it).
func (s *Store) MailDone(ctx context.Context, id int64, sent bool) error {
	if sent {
		_, err := s.db.ExecContext(ctx, `UPDATE notifications SET email_due = 0, emailed_at = ? WHERE id = ?`, Now(), id)
		return err
	}
	_, err := s.db.ExecContext(ctx, `UPDATE notifications SET email_due = 0 WHERE id = ?`, id)
	return err
}

// ClearMailDue drops every pending email (mail is off: switching it on later must not send a backlog).
func (s *Store) ClearMailDue(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `UPDATE notifications SET email_due = 0 WHERE email_due = 1`)
	return err
}

// PruneNotifications deletes read notifications older than keep.
func (s *Store) PruneNotifications(ctx context.Context, keep time.Duration) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM notifications WHERE read_at IS NOT NULL AND updated_at < ?`, time.Now().UTC().Add(-keep).Format("2006-01-02T15:04:05.000Z"))
	return err
}

// AdvanceCursor moves a named outbox reader.
func (s *Store) AdvanceCursor(ctx context.Context, name string, lastID int64) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO event_cursors (name, last_id) VALUES (?, ?) ON CONFLICT(name) DO UPDATE SET last_id = excluded.last_id`, name, lastID)
	return err
}

// EmailNotify reports whether a person wants notification email.
func (s *Store) EmailNotify(ctx context.Context, userID int64) (bool, error) {
	var on int
	err := s.db.QueryRowContext(ctx, `SELECT email_notify FROM users WHERE id = ?`, userID).Scan(&on)
	return on == 1, err
}

// SetEmailSettings stores a person's address ("" = none) and whether they want notification email.
func (s *Store) SetEmailSettings(ctx context.Context, userID int64, email string, notify bool) error {
	var e any
	if email != "" {
		e = email
	}
	return oneRow(s.db.ExecContext(ctx, `UPDATE users SET email = ?, email_notify = ? WHERE id = ? AND kind = 'user'`, e, boolInt(notify), userID))
}

// MailConfig is the outgoing mail server (D79). PasswordSealed is sealed under auth.secret_key.
type MailConfig struct {
	Enabled        bool
	Host           string
	Port           int
	Security       string // "tls" (implicit, 465) | "starttls" (587)
	Username       string
	PasswordSealed string
	From           string
	LastOKAt       string
	LastError      string
	UpdatedAt      string
}

// MailConfig reads the single row.
func (s *Store) MailConfig(ctx context.Context) (MailConfig, error) {
	var c MailConfig
	var on int
	err := s.db.QueryRowContext(ctx, `SELECT enabled, host, port, security, username, password_sealed, from_addr, COALESCE(last_ok_at, ''), last_error, updated_at FROM mail_config WHERE id = 1`).
		Scan(&on, &c.Host, &c.Port, &c.Security, &c.Username, &c.PasswordSealed, &c.From, &c.LastOKAt, &c.LastError, &c.UpdatedAt)
	c.Enabled = on == 1
	return c, err
}

// SaveMailConfig writes the row (the result columns are left alone).
func (s *Store) SaveMailConfig(ctx context.Context, c MailConfig) error {
	_, err := s.db.ExecContext(ctx, `UPDATE mail_config SET enabled = ?, host = ?, port = ?, security = ?, username = ?, password_sealed = ?, from_addr = ?, updated_at = ? WHERE id = 1`,
		boolInt(c.Enabled), c.Host, c.Port, c.Security, c.Username, c.PasswordSealed, c.From, Now())
	return err
}

// MailResult records the outcome of the last send ("" = it worked).
func (s *Store) MailResult(ctx context.Context, errText string) error {
	if errText == "" {
		_, err := s.db.ExecContext(ctx, `UPDATE mail_config SET last_ok_at = ?, last_error = '' WHERE id = 1`, Now())
		return err
	}
	_, err := s.db.ExecContext(ctx, `UPDATE mail_config SET last_error = ? WHERE id = 1`, errText)
	return err
}
