package web

import (
	"context"

	"codr.my/codr/internal/store"
)

// Outbox events (D78). Every place that changes an issue, a pull request or a branch from the web writes one event
// beside its audit row; the webhook dispatcher (and later notifications) read them. Data is a snapshot of what
// happened, so a later edit does not rewrite an event already sent. Pushes over SSH/HTTPS are written by the hook
// server (internal/hooks), merges here: a merge moves the base branch without passing through a hook.

// emit writes one event and wakes the dispatcher. A failed write is logged, never shown: the change itself succeeded.
func (s *Server) emit(ctx context.Context, repoID, actorID int64, kind string, data map[string]any) {
	if err := s.Store.AddEvent(ctx, repoID, actorID, kind, data); err != nil {
		s.Log.Error("event", "kind", kind, "err", err)
		return
	}
	s.kickReaders()
}

func issueEvent(i store.Issue, action string) map[string]any {
	state := i.State
	switch action {
	case "closed":
		state = "closed"
	case "reopened", "opened":
		state = "open"
	}
	return map[string]any{"action": action, "number": i.Number, "title": i.Title, "body": i.Body, "state": state, "user": i.Author}
}

func pullEvent(p store.Pull, headLabel, action string) map[string]any {
	state, merged := p.State, false
	switch action {
	case "closed":
		state = "closed"
	case "reopened", "opened":
		state = "open"
	case "merged":
		state, merged = "closed", true // GitHub's shape: a merge is "closed" with merged true
	}
	if action == "merged" {
		action = "closed"
	}
	if headLabel == "" {
		headLabel = p.HeadBranch
	}
	return map[string]any{"action": action, "number": p.Number, "title": p.Title, "body": p.Body, "state": state, "merged": merged,
		"merge_commit_sha": p.MergeCommit, "user": p.Author, "head": map[string]any{"ref": p.HeadBranch, "label": headLabel},
		"base": map[string]any{"ref": p.BaseBranch}}
}
