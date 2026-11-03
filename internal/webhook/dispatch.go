package webhook

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"codr.my/codr/internal/secret"
	"codr.my/codr/internal/store"
	"codr.my/codr/internal/version"
)

// Cursor is the outbox reader name of the dispatcher.
const Cursor = "webhook"

// Internal batch sizes: how much one tick reads and sends. Not product values; the limits a person tunes are settings.
const (
	eventBatch    = 200
	deliveryBatch = 50
	responseKeep  = 2048 // bytes of a receiver's answer kept for the delivery page
	pruneEvery    = 6 * time.Hour
	// Tick is how often serve looks for new events when nothing woke it; retry timing comes from webhook.retry_seconds.
	Tick = 5 * time.Second
)

// Dispatcher fans outbox events out to webhooks and delivers them. One runs inside `serve`.
type Dispatcher struct {
	Store   *store.Store
	Key     secret.Key // auth.secret_key: unseals each webhook's signing secret
	Builder Builder
	Log     *slog.Logger
	// Client overrides the HTTP client (tests); nil = a client built from the settings with the address guard.
	Client *http.Client
	// kick wakes the loop early (a ping or redeliver from the web UI)
	kick chan struct{}
}

// Kick asks the loop to run now instead of at the next tick. It never blocks.
func (d *Dispatcher) Kick() {
	if d.kick == nil {
		return
	}
	select {
	case d.kick <- struct{}{}:
	default:
	}
}

// Run ticks until ctx ends.
func (d *Dispatcher) Run(ctx context.Context, tick time.Duration) {
	d.kick = make(chan struct{}, 1)
	t := time.NewTicker(tick)
	defer t.Stop()
	lastPrune := time.Time{}
	for {
		if err := d.Tick(ctx); err != nil && ctx.Err() == nil {
			d.Log.Error("webhook: tick", "err", err)
		}
		if time.Since(lastPrune) > pruneEvery {
			if keep := d.setting(ctx, "webhook.keep_days", 30); keep > 0 {
				if err := d.Store.PruneOutbox(ctx, time.Duration(keep)*24*time.Hour); err != nil {
					d.Log.Error("webhook: prune", "err", err)
				}
			}
			lastPrune = time.Now()
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-d.kick:
		}
	}
}

func (d *Dispatcher) setting(ctx context.Context, key string, def int64) int64 {
	v, err := d.Store.SettingInt(ctx, key)
	if err != nil {
		return def
	}
	return v
}

// PolicyFrom reads the address rules from the settings.
func PolicyFrom(ctx context.Context, st *store.Store) Policy {
	p := Policy{}
	p.AllowPrivate, _ = st.SettingBool(ctx, "webhook.allow_private")
	hosts, _ := st.Setting(ctx, "webhook.allow_hosts")
	p.AllowHosts = ParseHosts(hosts)
	return p
}

// Tick fans out new events and makes every due attempt once.
func (d *Dispatcher) Tick(ctx context.Context) error {
	if err := d.fanOut(ctx); err != nil {
		return err
	}
	return d.deliverDue(ctx)
}

// GUID is a random delivery id (X-Codr-Delivery).
func GUID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func (d *Dispatcher) fanOut(ctx context.Context) error {
	d.Builder.PushCommits = int(d.setting(ctx, "webhook.push_commits", 20))
	for {
		after, err := d.Store.Cursor(ctx, Cursor)
		if err != nil {
			return err
		}
		evs, err := d.Store.EventsAfter(ctx, after, eventBatch)
		if err != nil || len(evs) == 0 {
			return err
		}
		hooks, err := d.Store.ActiveWebhooks(ctx)
		if err != nil {
			return err
		}
		var queue []store.NewDelivery
		for _, e := range evs {
			hs := hooks[e.RepoID]
			if len(hs) == 0 {
				continue
			}
			repo, err := d.Store.RepoByID(ctx, e.RepoID)
			if err != nil {
				continue // repository deleted since
			}
			var body []byte
			for _, h := range hs {
				if e.ID <= h.SinceEvent || !h.Wants(e.Kind) {
					continue
				}
				if body == nil {
					if body, err = d.Builder.Event(ctx, repo, e); err != nil {
						d.Log.Error("webhook: payload", "event", e.ID, "err", err)
						break
					}
				}
				queue = append(queue, store.NewDelivery{WebhookID: h.ID, EventID: e.ID, Kind: e.Kind, GUID: GUID(), Payload: string(body)})
			}
		}
		if err := d.Store.QueueAndAdvance(ctx, Cursor, evs[len(evs)-1].ID, queue); err != nil {
			return err
		}
		if len(evs) < eventBatch {
			return nil
		}
	}
}

func (d *Dispatcher) client(ctx context.Context) *http.Client {
	if d.Client != nil {
		return d.Client
	}
	timeout := time.Duration(d.setting(ctx, "webhook.timeout_seconds", 10)) * time.Second
	base := &net.Dialer{Timeout: timeout}
	tr := &http.Transport{
		Proxy:                 nil, // never through an environment proxy: the guard must see the real destination
		DialContext:           PolicyFrom(ctx, d.Store).dialer(base),
		TLSHandshakeTimeout:   timeout,
		ResponseHeaderTimeout: timeout,
		MaxIdleConns:          4,
		IdleConnTimeout:       30 * time.Second,
	}
	return &http.Client{Transport: tr, Timeout: timeout,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

func (d *Dispatcher) deliverDue(ctx context.Context) error {
	due, err := d.Store.DueDeliveries(ctx, store.Now(), deliveryBatch)
	if err != nil || len(due) == 0 {
		return err
	}
	cl := d.client(ctx)
	maxAttempts := int(d.setting(ctx, "webhook.max_attempts", 5))
	retry := time.Duration(d.setting(ctx, "webhook.retry_seconds", 60)) * time.Second
	hooks := map[int64]store.Webhook{}
	for _, dl := range due {
		if ctx.Err() != nil {
			return nil
		}
		h, ok := hooks[dl.WebhookID]
		if !ok {
			if h, err = d.Store.WebhookAny(ctx, dl.WebhookID); err != nil {
				return err
			}
			hooks[dl.WebhookID] = h
		}
		d.attempt(ctx, cl, h, &dl, maxAttempts, retry)
		if err := d.Store.RecordAttempt(ctx, dl); err != nil {
			return err
		}
	}
	return nil
}

// attempt sends one delivery and fills in its outcome: ok, pending with the next try, or failed for good.
func (d *Dispatcher) attempt(ctx context.Context, cl *http.Client, h store.Webhook, dl *store.Delivery, maxAttempts int, retry time.Duration) {
	dl.Attempts++
	dl.StatusCode, dl.Error, dl.Response = 0, "", ""
	start := time.Now()
	err := d.send(ctx, cl, h, dl)
	dl.DurationMS = time.Since(start).Milliseconds()
	switch {
	case err == nil && dl.StatusCode >= 200 && dl.StatusCode < 300:
		dl.State = "ok"
	case !h.Active && dl.Kind != "ping":
		dl.State = "failed"
		dl.Error = "webhook switched off"
	case dl.Attempts >= maxAttempts || errors.Is(err, ErrPrivate) || errors.Is(err, errNoSecret):
		dl.State = "failed" // a refused address or a missing key does not get better by waiting
	default:
		dl.State = "pending"
		dl.NextAt = time.Now().UTC().Add(retry * time.Duration(1<<(dl.Attempts-1))).Format("2006-01-02T15:04:05.000Z")
	}
	if err != nil {
		dl.Error = cleanErr(err)
	} else if dl.State != "ok" && dl.Error == "" {
		dl.Error = http.StatusText(dl.StatusCode)
		if dl.StatusCode >= 300 && dl.StatusCode < 400 {
			dl.Error = "redirect not followed"
		}
	}
}

var errNoSecret = errors.New("webhook: signing secret cannot be opened (auth.secret_key changed?)")

func (d *Dispatcher) send(ctx context.Context, cl *http.Client, h store.Webhook, dl *store.Delivery) error {
	if !h.Active && dl.Kind != "ping" {
		return nil
	}
	sec, err := d.Key.Open(h.SecretSealed)
	if err != nil {
		return errNoSecret
	}
	if _, err := PolicyFrom(ctx, d.Store).CheckURL(h.URL); err != nil {
		return err
	}
	body := []byte(dl.Payload)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, h.URL, bytes.NewReader(body))
	if err != nil {
		return ErrBadURL
	}
	sig := Sign(sec, body)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "Codr-Hookshot/"+version.Tag)
	req.Header.Set("X-Codr-Event", dl.Kind)
	req.Header.Set("X-Codr-Delivery", dl.GUID)
	req.Header.Set("X-Codr-Hook-ID", strconv.FormatInt(h.ID, 10))
	req.Header.Set("X-Codr-Signature-256", sig)
	req.Header.Set("X-Hub-Signature-256", sig) // the header most receivers already verify
	resp, err := cl.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	dl.StatusCode = resp.StatusCode
	b, _ := io.ReadAll(io.LimitReader(resp.Body, responseKeep))
	dl.Response = strings.ToValidUTF8(string(b), "?")
	return nil
}

func cleanErr(err error) string {
	if errors.Is(err, ErrPrivate) {
		return ErrPrivate.Error()
	}
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return "timeout"
	}
	s := err.Error()
	if len(s) > 300 {
		s = s[:300]
	}
	return s
}
