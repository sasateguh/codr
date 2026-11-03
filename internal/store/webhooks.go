package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

// Event is one row of the outbox (D78): something happened in a repository that a webhook or a notification may care
// about. Data is a JSON snapshot taken when it happened, so later edits do not rewrite what was sent.
type Event struct {
	ID      int64
	At      string
	RepoID  int64
	ActorID int64
	Kind    string
	Data    string
}

// AddEvent appends one event to the outbox. data is marshalled to JSON.
func (s *Store) AddEvent(ctx context.Context, repoID, actorID int64, kind string, data any) error {
	b, err := json.Marshal(data)
	if err != nil {
		return err
	}
	var actor any
	if actorID > 0 {
		actor = actorID
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO events (at, repo_id, actor_id, kind, data) VALUES (?, ?, ?, ?, ?)`, Now(), repoID, actor, kind, string(b))
	return err
}

// EventsAfter returns up to limit events with id > after, oldest first.
func (s *Store) EventsAfter(ctx context.Context, after int64, limit int) ([]Event, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, at, repo_id, COALESCE(actor_id, 0), kind, data FROM events WHERE id > ? ORDER BY id LIMIT ?`, after, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Event
	for rows.Next() {
		var e Event
		if err := rows.Scan(&e.ID, &e.At, &e.RepoID, &e.ActorID, &e.Kind, &e.Data); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// LastEventID is the newest event id (0 when the outbox is empty).
func (s *Store) LastEventID(ctx context.Context) (int64, error) {
	var id int64
	err := s.db.QueryRowContext(ctx, `SELECT COALESCE(MAX(id), 0) FROM events`).Scan(&id)
	return id, err
}

// Cursor returns how far the named outbox reader has got.
func (s *Store) Cursor(ctx context.Context, name string) (int64, error) {
	var id int64
	err := s.db.QueryRowContext(ctx, `SELECT last_id FROM event_cursors WHERE name = ?`, name).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return id, err
}

// Webhook is one outgoing webhook of a repository. Events is "*" or a comma list of event kinds.
type Webhook struct {
	ID           int64
	RepoID       int64
	URL          string
	SecretSealed string
	Events       string
	Active       bool
	SinceEvent   int64
	CreatedBy    int64
	CreatedAt    string
	UpdatedAt    string
	// the newest delivery, for the list page
	LastState string
	LastCode  int
	LastAt    string
}

// Wants reports whether the webhook subscribes to an event kind ("ping" always goes through).
func (w Webhook) Wants(kind string) bool {
	if kind == "ping" || w.Events == "*" {
		return true
	}
	for _, k := range strings.Split(w.Events, ",") {
		if k == kind {
			return true
		}
	}
	return false
}

// EventList is Events split for a form ("*" = nil).
func (w Webhook) EventList() []string {
	if w.Events == "*" || w.Events == "" {
		return nil
	}
	return strings.Split(w.Events, ",")
}

const hookCols = `w.id, w.repo_id, w.url, w.secret_sealed, w.events, w.active, w.since_event, COALESCE(w.created_by, 0), w.created_at, w.updated_at,
	COALESCE((SELECT state FROM webhook_deliveries d WHERE d.webhook_id = w.id ORDER BY d.id DESC LIMIT 1), ''),
	COALESCE((SELECT status_code FROM webhook_deliveries d WHERE d.webhook_id = w.id ORDER BY d.id DESC LIMIT 1), 0),
	COALESCE((SELECT COALESCE(done_at, created_at) FROM webhook_deliveries d WHERE d.webhook_id = w.id ORDER BY d.id DESC LIMIT 1), '')`

func scanHook(sc interface{ Scan(...any) error }) (Webhook, error) {
	var w Webhook
	var active int
	err := sc.Scan(&w.ID, &w.RepoID, &w.URL, &w.SecretSealed, &w.Events, &active, &w.SinceEvent, &w.CreatedBy, &w.CreatedAt, &w.UpdatedAt,
		&w.LastState, &w.LastCode, &w.LastAt)
	w.Active = active == 1
	return w, err
}

// Webhooks lists a repository's webhooks, oldest first.
func (s *Store) Webhooks(ctx context.Context, repoID int64) ([]Webhook, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+hookCols+` FROM webhooks w WHERE w.repo_id = ? ORDER BY w.id`, repoID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Webhook
	for rows.Next() {
		w, err := scanHook(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, w)
	}
	return out, rows.Err()
}

// WebhookByID finds one webhook of a repository (ErrNotFound when it belongs to another one).
func (s *Store) WebhookByID(ctx context.Context, repoID, id int64) (Webhook, error) {
	w, err := scanHook(s.db.QueryRowContext(ctx, `SELECT `+hookCols+` FROM webhooks w WHERE w.repo_id = ? AND w.id = ?`, repoID, id))
	if errors.Is(err, sql.ErrNoRows) {
		return w, ErrNotFound
	}
	return w, err
}

// WebhookAny finds a webhook by id alone (the dispatcher, which already holds the delivery).
func (s *Store) WebhookAny(ctx context.Context, id int64) (Webhook, error) {
	w, err := scanHook(s.db.QueryRowContext(ctx, `SELECT `+hookCols+` FROM webhooks w WHERE w.id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return w, ErrNotFound
	}
	return w, err
}

// CreateWebhook adds a webhook that hears events from now on (since_event = the newest event).
func (s *Store) CreateWebhook(ctx context.Context, w Webhook) (int64, error) {
	now := Now()
	res, err := s.db.ExecContext(ctx, `INSERT INTO webhooks (repo_id, url, secret_sealed, events, active, since_event, created_by, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, (SELECT COALESCE(MAX(id), 0) FROM events), ?, ?, ?)`, w.RepoID, w.URL, w.SecretSealed, w.Events, boolInt(w.Active), w.CreatedBy, now, now)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// UpdateWebhook changes the URL, the events and the active switch.
func (s *Store) UpdateWebhook(ctx context.Context, repoID, id int64, url, events string, active bool) error {
	return oneRow(s.db.ExecContext(ctx, `UPDATE webhooks SET url = ?, events = ?, active = ?, updated_at = ? WHERE repo_id = ? AND id = ?`,
		url, events, boolInt(active), Now(), repoID, id))
}

// SetWebhookSecret replaces the sealed signing secret.
func (s *Store) SetWebhookSecret(ctx context.Context, repoID, id int64, sealed string) error {
	return oneRow(s.db.ExecContext(ctx, `UPDATE webhooks SET secret_sealed = ?, updated_at = ? WHERE repo_id = ? AND id = ?`, sealed, Now(), repoID, id))
}

// DeleteWebhook removes a webhook and its deliveries.
func (s *Store) DeleteWebhook(ctx context.Context, repoID, id int64) error {
	return oneRow(s.db.ExecContext(ctx, `DELETE FROM webhooks WHERE repo_id = ? AND id = ?`, repoID, id))
}

// ActiveWebhooks lists every active webhook, keyed by repository id.
func (s *Store) ActiveWebhooks(ctx context.Context) (map[int64][]Webhook, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+hookCols+` FROM webhooks w WHERE w.active = 1 ORDER BY w.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64][]Webhook{}
	for rows.Next() {
		w, err := scanHook(rows)
		if err != nil {
			return nil, err
		}
		out[w.RepoID] = append(out[w.RepoID], w)
	}
	return out, rows.Err()
}

// Delivery is one webhook delivery (all its attempts).
type Delivery struct {
	ID         int64
	WebhookID  int64
	EventID    int64
	Kind       string
	GUID       string
	State      string
	Attempts   int
	StatusCode int
	Error      string
	DurationMS int64
	Response   string
	Payload    string
	CreatedAt  string
	NextAt     string
	DoneAt     string
}

const deliveryCols = `id, webhook_id, COALESCE(event_id, 0), kind, guid, state, attempts, status_code, error, duration_ms, response, payload, created_at, next_at, COALESCE(done_at, '')`

func scanDelivery(sc interface{ Scan(...any) error }) (Delivery, error) {
	var d Delivery
	err := sc.Scan(&d.ID, &d.WebhookID, &d.EventID, &d.Kind, &d.GUID, &d.State, &d.Attempts, &d.StatusCode, &d.Error, &d.DurationMS,
		&d.Response, &d.Payload, &d.CreatedAt, &d.NextAt, &d.DoneAt)
	return d, err
}

// NewDelivery is a delivery to queue.
type NewDelivery struct {
	WebhookID int64
	EventID   int64
	Kind      string
	GUID      string
	Payload   string
}

func (s *Store) queue(ctx context.Context, ex interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}, d NewDelivery, now string) error {
	var ev any
	if d.EventID > 0 {
		ev = d.EventID
	}
	_, err := ex.ExecContext(ctx, `INSERT INTO webhook_deliveries (webhook_id, event_id, kind, guid, payload, created_at, next_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		d.WebhookID, ev, d.Kind, d.GUID, d.Payload, now, now)
	return err
}

// QueueDelivery queues one delivery now (ping, redeliver).
func (s *Store) QueueDelivery(ctx context.Context, d NewDelivery) error {
	return s.queue(ctx, s.db, d, Now())
}

// QueueAndAdvance queues deliveries and moves the named cursor to lastID in one transaction: an event is fanned out
// once, even when serve stops half-way.
func (s *Store) QueueAndAdvance(ctx context.Context, cursor string, lastID int64, ds []NewDelivery) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	now := Now()
	for _, d := range ds {
		if err := s.queue(ctx, tx, d, now); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO event_cursors (name, last_id) VALUES (?, ?) ON CONFLICT(name) DO UPDATE SET last_id = excluded.last_id`, cursor, lastID); err != nil {
		return err
	}
	return tx.Commit()
}

// DueDeliveries returns pending deliveries whose next attempt is due.
func (s *Store) DueDeliveries(ctx context.Context, now string, limit int) ([]Delivery, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+deliveryCols+` FROM webhook_deliveries WHERE state = 'pending' AND next_at <= ? ORDER BY next_at, id LIMIT ?`, now, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Delivery
	for rows.Next() {
		d, err := scanDelivery(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// RecordAttempt stores the outcome of one attempt. state "pending" with nextAt = try again later.
func (s *Store) RecordAttempt(ctx context.Context, d Delivery) error {
	var done any
	if d.State != "pending" {
		done = Now()
	}
	_, err := s.db.ExecContext(ctx, `UPDATE webhook_deliveries SET state = ?, attempts = ?, status_code = ?, error = ?, duration_ms = ?, response = ?, next_at = ?, done_at = ? WHERE id = ?`,
		d.State, d.Attempts, d.StatusCode, d.Error, d.DurationMS, d.Response, d.NextAt, done, d.ID)
	return err
}

// Deliveries lists a webhook's newest deliveries.
func (s *Store) Deliveries(ctx context.Context, webhookID int64, limit int) ([]Delivery, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+deliveryCols+` FROM webhook_deliveries WHERE webhook_id = ? ORDER BY id DESC LIMIT ?`, webhookID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Delivery
	for rows.Next() {
		d, err := scanDelivery(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// DeliveryByID finds one delivery of a webhook.
func (s *Store) DeliveryByID(ctx context.Context, webhookID, id int64) (Delivery, error) {
	d, err := scanDelivery(s.db.QueryRowContext(ctx, `SELECT `+deliveryCols+` FROM webhook_deliveries WHERE webhook_id = ? AND id = ?`, webhookID, id))
	if errors.Is(err, sql.ErrNoRows) {
		return d, ErrNotFound
	}
	return d, err
}

// WebhookHealth counts active webhooks and deliveries that failed for good since the given time (doctor).
func (s *Store) WebhookHealth(ctx context.Context, since string) (active, failed int, err error) {
	if err = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM webhooks WHERE active = 1`).Scan(&active); err != nil {
		return
	}
	err = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM webhook_deliveries WHERE state = 'failed' AND done_at >= ?`, since).Scan(&failed)
	return
}

// PruneOutbox drops finished deliveries and events older than keep, keeping every event a cursor has not passed yet.
func (s *Store) PruneOutbox(ctx context.Context, keep time.Duration) error {
	before := time.Now().UTC().Add(-keep).Format("2006-01-02T15:04:05.000Z")
	if _, err := s.db.ExecContext(ctx, `DELETE FROM webhook_deliveries WHERE state != 'pending' AND created_at < ?`, before); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `DELETE FROM events WHERE at < ? AND id <= (SELECT COALESCE(MIN(last_id), 0) FROM event_cursors)
		AND id NOT IN (SELECT event_id FROM webhook_deliveries WHERE event_id IS NOT NULL)`, before)
	return err
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func oneRow(res sql.Result, err error) error {
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}
