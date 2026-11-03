package xref

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"codr.my/codr/internal/gitread"
	"codr.my/codr/internal/store"
)

// Cursor is the outbox reader name of the closer.
const Cursor = "xref"

// Tick is how often serve looks for new events when nothing woke the closer.
const Tick = 5 * time.Second

const eventBatch = 200

// Closer is the third reader of the event outbox (D87). It closes issues named by closing keywords in:
//   - commits that a push brings onto a repository's default branch (the commits new to that branch, at most
//     issue.close_scan_commits of them), and
//   - the body and title of a pull request when it is merged into the default branch.
//
// The person acting is the pusher or the person merging; an issue in another repository closes only when that person
// has write access there, and nothing is ever closed in a mirror (its issues belong to the source).
type Closer struct {
	Store *store.Store
	Git   func(repoID int64) gitread.Repo
	Log   *slog.Logger
	// OnEvent is called after the closer wrote outbox events (serve wakes the webhook and notification readers).
	OnEvent func()
	kick    chan struct{}
}

// Kick wakes the loop early. It never blocks.
func (c *Closer) Kick() {
	if c.kick == nil {
		return
	}
	select {
	case c.kick <- struct{}{}:
	default:
	}
}

// Run ticks until ctx ends.
func (c *Closer) Run(ctx context.Context, tick time.Duration) {
	c.kick = make(chan struct{}, 1)
	t := time.NewTicker(tick)
	defer t.Stop()
	for {
		if err := c.Tick(ctx); err != nil && ctx.Err() == nil {
			c.Log.Error("xref: tick", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-c.kick:
		}
	}
}

// Tick reads the events after the cursor.
func (c *Closer) Tick(ctx context.Context) error {
	wrote := false
	defer func() {
		if wrote && c.OnEvent != nil {
			c.OnEvent()
		}
	}()
	for {
		after, err := c.Store.Cursor(ctx, Cursor)
		if err != nil {
			return err
		}
		evs, err := c.Store.EventsAfter(ctx, after, eventBatch)
		if err != nil || len(evs) == 0 {
			return err
		}
		for _, e := range evs {
			n, err := c.event(ctx, e)
			if err != nil {
				return err // the cursor stays: the next tick retries this event
			}
			wrote = wrote || n > 0
			if err := c.Store.AdvanceCursor(ctx, Cursor, e.ID); err != nil {
				return err
			}
		}
		if len(evs) < eventBatch {
			return nil
		}
	}
}

type pushData struct {
	Ref    string `json:"ref"`
	Before string `json:"before"`
	After  string `json:"after"`
}

type pullData struct {
	Action string `json:"action"`
	Number int64  `json:"number"`
	Title  string `json:"title"`
	Body   string `json:"body"`
	Merged bool   `json:"merged"`
	Base   struct {
		Ref string `json:"ref"`
	} `json:"base"`
}

func isZero(id string) bool { return strings.Trim(id, "0") == "" }

// event handles one outbox event and returns how many issues it closed.
func (c *Closer) event(ctx context.Context, e store.Event) (int, error) {
	if e.Kind != "push" && e.Kind != "pull_request" {
		return 0, nil
	}
	repo, err := c.Store.RepoByID(ctx, e.RepoID)
	if errors.Is(err, store.ErrNotFound) {
		return 0, nil
	} else if err != nil {
		return 0, err
	}
	if repo.Mirror {
		return 0, nil
	}
	kw, _ := c.Store.Setting(ctx, "issue.close_keywords")
	words := Keywords(kw)
	if len(words) == 0 {
		return 0, nil
	}
	closed := 0
	switch e.Kind {
	case "push":
		var d pushData
		// a new or deleted default branch closes nothing: pushing an old history must not sweep old issues shut
		if json.Unmarshal([]byte(e.Data), &d) != nil || d.Ref != "refs/heads/"+repo.DefaultBranch || isZero(d.After) || isZero(d.Before) {
			return 0, nil
		}
		max, err := c.Store.SettingInt(ctx, "issue.close_scan_commits")
		if err != nil || max < 1 {
			max = 250
		}
		commits, err := c.Git(repo.ID).LogRange(ctx, d.Before, d.After, int(max))
		if err != nil {
			c.Log.Warn("xref: read commits", "repo", repo.Owner+"/"+repo.Name, "err", err)
			return 0, nil // a force push to history git no longer has: nothing to read, nothing to retry
		}
		for i := len(commits) - 1; i >= 0; i-- { // oldest first, like the history
			cm := commits[i]
			for _, r := range Closing(cm.Subject+"\n"+cm.Body, words) {
				n, err := c.close(ctx, repo, e.ActorID, r, cm.Hash, "commit "+cm.Short())
				if err != nil {
					return closed, err
				}
				closed += n
			}
		}
	case "pull_request":
		var d pullData
		if json.Unmarshal([]byte(e.Data), &d) != nil || !d.Merged || d.Base.Ref != repo.DefaultBranch {
			return 0, nil
		}
		for _, r := range Closing(d.Title+"\n"+d.Body, words) {
			n, err := c.close(ctx, repo, e.ActorID, r, "#"+strconv.FormatInt(d.Number, 10), "#"+strconv.FormatInt(d.Number, 10))
			if err != nil {
				return closed, err
			}
			closed += n
		}
	}
	return closed, nil
}

// close shuts one referenced issue when it exists, is an issue (not a pull request), is open, and the actor may write
// to its repository.
func (c *Closer) close(ctx context.Context, from store.Repo, actorID int64, r Ref, by, why string) (int, error) {
	target := from
	if r.Owner != "" {
		t, err := c.Store.RepoByPath(ctx, r.Owner, r.Repo)
		if errors.Is(err, store.ErrNotFound) {
			return 0, nil
		} else if err != nil {
			return 0, err
		}
		target = t
	}
	if target.Mirror || target.Archived {
		return 0, nil
	}
	if a, err := c.Store.AccessFor(ctx, actorID, target); err != nil || a < store.AccessWrite {
		return 0, err
	}
	i, err := c.Store.IssueByNumber(ctx, target.ID, r.Number)
	if errors.Is(err, store.ErrNotFound) {
		return 0, nil
	} else if err != nil {
		return 0, err
	}
	if target.ID != from.ID && !strings.HasPrefix(by, "#") {
		by = from.Owner + "/" + from.Name + "@" + by // a commit in another repository: keep where it lives
	} else if target.ID != from.ID {
		by = from.Owner + "/" + from.Name + by
	}
	changed, err := c.Store.CloseIssueByRef(ctx, i.ID, by)
	if err != nil || !changed {
		return 0, err
	}
	ref := target.Owner + "/" + target.Name + "#" + strconv.FormatInt(i.Number, 10)
	_ = c.Store.Audit(ctx, actorID, "issue.close", ref, why)
	data := map[string]any{"action": "closed", "number": i.Number, "title": i.Title, "body": i.Body, "state": "closed", "user": i.Author, "closed_by": by}
	if err := c.Store.AddEvent(ctx, target.ID, actorID, "issues", data); err != nil {
		return 0, err
	}
	return 1, nil
}
