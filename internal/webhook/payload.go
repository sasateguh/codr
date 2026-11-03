package webhook

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"time"

	"codr.my/codr/internal/gitread"
	"codr.my/codr/internal/store"
)

// Kinds are the event kinds a webhook can subscribe to, in form order. Field names follow GitHub's where the meaning
// is the same (full_name, html_url, ref/before/after, number), so existing receivers need little change.
var Kinds = []string{"push", "issues", "issue_comment", "pull_request", "pull_request_review", "pull_request_review_comment", "status", "wiki"}

// ValidKinds keeps the known kinds of a form submission, in canonical order; empty means "everything".
func ValidKinds(in []string) []string {
	var out []string
	for _, k := range Kinds {
		for _, v := range in {
			if v == k {
				out = append(out, k)
				break
			}
		}
	}
	return out
}

const zeroID = "0000000000000000000000000000000000000000"

type repoJSON struct {
	ID            int64  `json:"id"`
	Name          string `json:"name"`
	FullName      string `json:"full_name"`
	Owner         string `json:"owner"`
	Private       bool   `json:"private"`
	HTMLURL       string `json:"html_url"`
	CloneURL      string `json:"clone_url"`
	SSHURL        string `json:"ssh_url"`
	DefaultBranch string `json:"default_branch"`
}

type userJSON struct {
	ID      int64  `json:"id"`
	Login   string `json:"login"`
	HTMLURL string `json:"html_url"`
}

type commitJSON struct {
	ID        string `json:"id"`
	Message   string `json:"message"`
	Timestamp string `json:"timestamp"`
	URL       string `json:"url"`
	Author    struct {
		Name  string `json:"name"`
		Email string `json:"email"`
	} `json:"author"`
}

// Builder turns an outbox event into the JSON body a webhook receives.
type Builder struct {
	Store    *store.Store
	HTTPHost string // forge base URL, e.g. https://git.codr.my
	SSHHost  string
	Git      func(repoID int64) gitread.Repo
	// PushCommits caps the commits listed in a push payload (setting webhook.push_commits).
	PushCommits int
}

func (b Builder) repo(r store.Repo) repoJSON {
	full := r.Owner + "/" + r.Name
	return repoJSON{ID: r.ID, Name: r.Name, FullName: full, Owner: r.Owner, Private: r.Private, HTMLURL: b.HTTPHost + "/" + full,
		CloneURL: b.HTTPHost + "/" + full + ".git", SSHURL: "ssh://git@" + b.SSHHost + "/" + full + ".git", DefaultBranch: r.DefaultBranch}
}

func (b Builder) user(ctx context.Context, id int64) *userJSON {
	if id <= 0 {
		return nil
	}
	u, err := b.Store.UserByID(ctx, id)
	if err != nil {
		return nil
	}
	return &userJSON{ID: u.ID, Login: u.Name, HTMLURL: b.HTTPHost + "/" + u.Name}
}

// Ping is the body sent when a webhook is created or tested.
func (b Builder) Ping(ctx context.Context, r store.Repo, w store.Webhook, actorID int64) ([]byte, error) {
	return json.Marshal(map[string]any{"event": "ping", "zen": "Codr webhook works.", "hook_id": w.ID, "hook": map[string]any{
		"id": w.ID, "url": w.URL, "events": eventsOrAll(w), "active": w.Active}, "repository": b.repo(r), "sender": b.user(ctx, actorID)})
}

func eventsOrAll(w store.Webhook) []string {
	if l := w.EventList(); l != nil {
		return l
	}
	return []string{"*"}
}

// Event builds the body for one outbox event. The event's own data (a snapshot taken when it happened) is placed
// under the key its kind names; push events get their commit list here, from the repository on disk.
func (b Builder) Event(ctx context.Context, r store.Repo, e store.Event) ([]byte, error) {
	var data map[string]any
	if err := json.Unmarshal([]byte(e.Data), &data); err != nil {
		return nil, err
	}
	body := map[string]any{"event": e.Kind, "delivered_at": time.Now().UTC().Format(time.RFC3339), "repository": b.repo(r), "sender": b.user(ctx, e.ActorID)}
	if a, ok := data["action"]; ok {
		body["action"] = a
		delete(data, "action")
	}
	full := b.HTTPHost + "/" + r.Owner + "/" + r.Name
	switch e.Kind {
	case "push":
		b.push(ctx, r, full, data, body)
	case "issues":
		data["html_url"] = full + "/isu/" + num(data["number"])
		body["issue"] = data
	case "issue_comment":
		if pull, _ := data["is_pull"].(bool); pull {
			data["html_url"] = full + "/pr/" + num(data["number"])
		} else {
			data["html_url"] = full + "/isu/" + num(data["number"])
		}
		body["comment"] = data
	case "pull_request", "pull_request_review", "pull_request_review_comment":
		data["html_url"] = full + "/pr/" + num(data["number"])
		key := map[string]string{"pull_request": "pull_request", "pull_request_review": "review", "pull_request_review_comment": "comment"}[e.Kind]
		body[key] = data
	case "wiki": // like GitHub's gollum event: a list of pages
		page, _ := data["page"].(string)
		data["page_name"], data["html_url"] = page, full+"/wiki/"+page
		body["pages"] = []any{data}
	case "status": // like GitHub's: the status fields at the top level
		for k, v := range data {
			body[k] = v
		}
		if sha, _ := data["sha"].(string); sha != "" {
			body["commit_url"] = full + "/commit/" + sha
		}
	default:
		body["data"] = data
	}
	return json.Marshal(body)
}

func (b Builder) push(ctx context.Context, r store.Repo, full string, data, body map[string]any) {
	ref, _ := data["ref"].(string)
	before, _ := data["before"].(string)
	after, _ := data["after"].(string)
	body["ref"], body["before"], body["after"] = ref, before, after
	body["created"], body["deleted"] = before == zeroID, after == zeroID
	body["pusher"] = body["sender"]
	commits := []commitJSON{}
	if after != zeroID && b.Git != nil && b.PushCommits > 0 {
		g := b.Git(r.ID)
		var cs []gitread.Commit
		var err error
		if before == zeroID {
			cs, err = g.Log(ctx, after, "", 1, 0) // a new branch: its tip only; the rest is history already in the repository
		} else {
			cs, err = g.LogRange(ctx, before, after, b.PushCommits)
		}
		if err == nil {
			for i := len(cs) - 1; i >= 0; i-- { // oldest first, as receivers expect
				c := cs[i]
				cj := commitJSON{ID: c.Hash, Message: strings.TrimSpace(c.Subject + "\n\n" + c.Body), Timestamp: c.When.UTC().Format(time.RFC3339), URL: full + "/commit/" + c.Hash}
				cj.Author.Name, cj.Author.Email = c.Author, c.Email
				commits = append(commits, cj)
			}
		}
	}
	body["commits"] = commits
	if len(commits) > 0 {
		body["head_commit"] = commits[len(commits)-1]
	} else {
		body["head_commit"] = nil
	}
	if after != zeroID {
		body["compare"] = full + "/commits/" + after
	}
}

func num(v any) string {
	switch n := v.(type) {
	case float64:
		return strconv.FormatInt(int64(n), 10)
	case string:
		return n
	}
	return ""
}
