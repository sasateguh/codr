// Package notify turns outbox events into notifications in the app and by email (D79). It is the second reader of
// the event outbox (D78), with its own cursor.
package notify

import (
	"context"
	"encoding/json"
	"log/slog"
	"regexp"
	"strings"
	"time"

	"codr.my/codr/internal/secret"
	"codr.my/codr/internal/store"
)

// Cursor is the outbox reader name of the notifier.
const Cursor = "notify"

// Internal batch sizes and pacing; what a person tunes lives in the settings (notify.*, mail.*).
const (
	eventBatch = 200
	excerptMax = 600 // characters of the latest text kept for the list and the email
	pruneEvery = 6 * time.Hour
	// Tick is how often serve looks for new events and due email when nothing woke it.
	Tick = 5 * time.Second
)

// Notifier fans events out to people and sends the email that is due. One runs inside `serve`.
type Notifier struct {
	Store  *store.Store
	Key    secret.Key // unseals the mail server password
	Mailer *Mailer
	Log    *slog.Logger
	kick   chan struct{}
}

// Kick wakes the loop early. It never blocks.
func (n *Notifier) Kick() {
	if n.kick == nil {
		return
	}
	select {
	case n.kick <- struct{}{}:
	default:
	}
}

// Run ticks until ctx ends.
func (n *Notifier) Run(ctx context.Context, tick time.Duration) {
	n.kick = make(chan struct{}, 1)
	t := time.NewTicker(tick)
	defer t.Stop()
	var lastPrune time.Time
	for {
		if err := n.Tick(ctx); err != nil && ctx.Err() == nil {
			n.Log.Error("notify: tick", "err", err)
		}
		if time.Since(lastPrune) > pruneEvery {
			if days, err := n.Store.SettingInt(ctx, "notify.keep_days"); err == nil && days > 0 {
				_ = n.Store.PruneNotifications(ctx, time.Duration(days)*24*time.Hour)
			}
			lastPrune = time.Now()
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-n.kick:
		}
	}
}

// Tick fans out new events, then sends the email that is due.
func (n *Notifier) Tick(ctx context.Context) error {
	if err := n.fanOut(ctx); err != nil {
		return err
	}
	if n.Mailer != nil {
		return n.Mailer.SendDue(ctx, n.Store, n.Key)
	}
	return nil
}

type eventData struct {
	Action string `json:"action"`
	Number int64  `json:"number"`
	Title  string `json:"title"`
	Body   string `json:"body"`
	IsPull bool   `json:"is_pull"`
	State  string `json:"state"`
	Merged bool   `json:"merged"`
	// AssigneeID is the person an "assigned" event puts on the thread (D85).
	AssigneeID int64 `json:"assignee_id"`
}

// threadOf maps an event kind to the thread it moves; "" = not a thread event (push), or not worth telling anyone
// about (an edit).
func threadOf(kind string, d eventData) string {
	switch kind {
	case "issues":
		if d.Action == "edited" || d.Action == "unassigned" {
			return ""
		}
		return "issue"
	case "issue_comment":
		if d.IsPull {
			return "pull"
		}
		return "issue"
	case "pull_request":
		if d.Action == "edited" || d.Action == "unassigned" {
			return ""
		}
		return "pull"
	case "pull_request_review", "pull_request_review_comment":
		return "pull"
	}
	return ""
}

func (n *Notifier) fanOut(ctx context.Context) error {
	for {
		after, err := n.Store.Cursor(ctx, Cursor)
		if err != nil {
			return err
		}
		evs, err := n.Store.EventsAfter(ctx, after, eventBatch)
		if err != nil || len(evs) == 0 {
			return err
		}
		for _, e := range evs {
			if err := n.event(ctx, e); err != nil {
				return err // the cursor stays: the next tick retries this event
			}
			if err := n.Store.AdvanceCursor(ctx, Cursor, e.ID); err != nil {
				return err
			}
		}
		if len(evs) < eventBatch {
			return nil
		}
	}
}

// action is what happened, as one word the email and the list translate: opened, closed, reopened, merged, commented,
// approved, changes, reviewed.
func action(kind string, d eventData) string {
	switch kind {
	case "issue_comment", "pull_request_review_comment":
		return "commented"
	case "pull_request_review":
		switch d.State {
		case "approve":
			return "approved"
		case "changes":
			return "changes"
		}
		return "reviewed"
	case "pull_request":
		if d.Merged {
			return "merged"
		}
	}
	return d.Action
}

func (n *Notifier) event(ctx context.Context, e store.Event) error {
	var d eventData
	if json.Unmarshal([]byte(e.Data), &d) != nil || d.Number == 0 {
		return nil
	}
	thread := threadOf(e.Kind, d)
	if thread == "" {
		return nil
	}
	repo, err := n.Store.RepoByID(ctx, e.RepoID)
	if err != nil {
		return nil // repository gone
	}
	reasons := map[int64]string{}
	add := func(id int64, why string) {
		if id <= 0 {
			return
		}
		if cur, ok := reasons[id]; !ok || rank[why] > rank[cur] {
			reasons[id] = why
		}
	}
	if d.Action == "assigned" { // only the person put on the thread hears about it (D85)
		add(d.AssigneeID, "assigned")
		return n.deliver(ctx, e, d, repo, thread, reasons, nil)
	}
	// watchers first, so taking part (and above all being mentioned) wins as the stated reason
	watch, err := n.Store.WatchRows(ctx, repo.ID)
	if err != nil {
		return err
	}
	if _, set := watch[repo.OwnerID]; !set {
		watch[repo.OwnerID] = store.WatchAll // the default for the owner of a personal repository
	}
	for id, lvl := range watch {
		if lvl == store.WatchAll {
			add(id, "watching")
		}
	}
	people, err := n.Store.ThreadParticipants(ctx, repo.ID, thread, d.Number)
	if err != nil {
		return err
	}
	author := n.Store.ThreadAuthor(ctx, repo.ID, thread, d.Number)
	for _, id := range people {
		if id == author {
			add(id, "author")
		} else {
			add(id, "comment")
		}
	}
	assigned, err := n.Store.Assignees(ctx, repo.ID, d.Number)
	if err != nil {
		return err
	}
	for _, a := range assigned {
		add(a.UserID, "assigned")
	}
	if names := Mentions(d.Body); len(names) > 0 {
		ids, err := n.Store.ActiveUsersByName(ctx, names)
		if err != nil {
			return err
		}
		for _, id := range ids {
			add(id, "mention")
		}
	}
	return n.deliver(ctx, e, d, repo, thread, reasons, watch)
}

// rank orders the stated reasons: being mentioned beats being assigned beats taking part beats watching.
var rank = map[string]int{"watching": 1, "comment": 2, "author": 3, "assigned": 4, "mention": 5}

// Reasons and Actions are every value a notification row can carry in reason / last_action; the list page and the
// email must have words for each (a test in the web package checks they do).
var (
	Reasons = []string{"watching", "comment", "author", "assigned", "mention"}
	Actions = []string{"opened", "closed", "reopened", "merged", "commented", "approved", "changes", "reviewed", "assigned"}
)

// deliver writes one notification row per person in reasons, skipping the actor, people who ignore the repository,
// organisations, disabled accounts and anyone who cannot read it.
func (n *Notifier) deliver(ctx context.Context, e store.Event, d eventData, repo store.Repo, thread string, reasons map[int64]string, watch map[int64]string) error {
	if watch == nil {
		var err error
		if watch, err = n.Store.WatchRows(ctx, repo.ID); err != nil {
			return err
		}
	}
	excerpt := []rune(strings.TrimSpace(d.Body))
	if d.Action == "assigned" {
		excerpt = nil // the body is not news to the person assigned; the title says what it is
	}
	if len(excerpt) > excerptMax {
		excerpt = append(excerpt[:excerptMax], '…')
	}
	for id, why := range reasons {
		if id == e.ActorID || watch[id] == store.WatchIgnore {
			continue
		}
		u, err := n.Store.UserByID(ctx, id)
		if err != nil || u.Disabled || u.IsOrg {
			continue
		}
		// never tell someone about a repository they cannot read: a mention in a private repository stays private
		if a, err := n.Store.AccessFor(ctx, id, repo); err != nil || a < store.AccessRead {
			continue
		}
		row := store.NotifyRow{UserID: id, RepoID: repo.ID, Number: d.Number, ActorID: e.ActorID, EventID: e.ID, Thread: thread,
			Title: d.Title, Reason: why, Kind: e.Kind, Action: action(e.Kind, d), Excerpt: string(excerpt)}
		if err := n.Store.UpsertNotification(ctx, row); err != nil {
			return err
		}
	}
	return nil
}

var (
	mentionRE = regexp.MustCompile(`(?:^|[^\w@/.-])@([A-Za-z0-9][A-Za-z0-9_-]{0,38})\b`)
	fenceRE   = regexp.MustCompile("(?s)```.*?(```|$)")
	inlineRE  = regexp.MustCompile("`[^`\n]*`")
)

// Mentions returns the @names in a Markdown text, outside code (fenced or inline), lower-cased, without repeats.
// An email address (a@b.com) is not a mention.
func Mentions(text string) []string {
	text = inlineRE.ReplaceAllString(fenceRE.ReplaceAllString(text, " "), " ")
	seen := map[string]bool{}
	var out []string
	for _, m := range mentionRE.FindAllStringSubmatch(text, -1) {
		n := strings.ToLower(m[1])
		if !seen[n] {
			seen[n] = true
			out = append(out, n)
		}
	}
	return out
}
