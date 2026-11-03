package web

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"testing"

	"codr.my/codr/internal/store"
)

type apiResp struct {
	Code int
	Body []byte
	Head http.Header
}

func (r apiResp) obj(t *testing.T) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(r.Body, &m); err != nil {
		t.Fatalf("not a JSON object: %s", r.Body)
	}
	return m
}

func (r apiResp) list(t *testing.T) []map[string]any {
	t.Helper()
	var l []map[string]any
	if err := json.Unmarshal(r.Body, &l); err != nil {
		t.Fatalf("not a JSON list: %s", r.Body)
	}
	return l
}

// api calls the API; auth "" = anonymous, a name = that person's token, "raw:<x>" = that header value.
func (h *harness) api(method, path, auth string, body any) apiResp {
	h.t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, h.srv.URL+path, rd)
	switch {
	case strings.HasPrefix(auth, "raw:"):
		req.Header.Set("Authorization", strings.TrimPrefix(auth, "raw:"))
	case auth != "":
		req.Header.Set("Authorization", "Bearer "+h.tokens[auth])
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		h.t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return apiResp{resp.StatusCode, b, resp.Header}
}

// The OpenAPI document lists exactly the registered routes: a route added without documentation, or documentation of
// a route that does not exist, fails here.
func TestOpenAPIMatchesRoutes(t *testing.T) {
	var spec struct {
		OpenAPI string                    `json:"openapi"`
		Paths   map[string]map[string]any `json:"paths"`
	}
	if err := json.Unmarshal(openAPI, &spec); err != nil || !strings.HasPrefix(spec.OpenAPI, "3.1") {
		t.Fatalf("openapi.json: %v", err)
	}
	var doc, code []string
	for p, ops := range spec.Paths {
		for m := range ops {
			doc = append(doc, strings.ToUpper(m)+" "+p)
		}
	}
	for _, rt := range apiRoutes {
		code = append(code, rt.Method+" "+strings.ReplaceAll(rt.Path, "{path...}", "{path}"))
	}
	sort.Strings(doc)
	sort.Strings(code)
	if strings.Join(doc, "\n") != strings.Join(code, "\n") {
		t.Fatalf("spec and routes differ:\nspec:\n%s\n\nroutes:\n%s", strings.Join(doc, "\n"), strings.Join(code, "\n"))
	}
}

func TestAPI(t *testing.T) {
	h := newHarness(t)
	pub, priv := h.seed(t)
	ctx := context.Background()

	// anonymous: public only; private answers 404, not 403
	if r := h.api("GET", "/api/v1/user", "", nil); r.Code != 401 {
		t.Fatalf("anonymous /user: %d", r.Code)
	}
	r := h.api("GET", "/api/v1/repos", "", nil)
	if l := r.list(t); r.Code != 200 || len(l) != 1 || l[0]["full_name"] != "alice/awam" || !strings.Contains(r.Head.Get("Cache-Control"), "no-store") {
		t.Fatalf("anonymous repos: %d %s", r.Code, r.Body)
	}
	if r := h.api("GET", "/api/v1/repos/alice/rahsia", "", nil); r.Code != 404 {
		t.Fatalf("private for anonymous: %d", r.Code)
	}
	// bad credentials: 401, and a password is never a token
	if r := h.api("GET", "/api/v1/repos", "raw:Bearer codr_nope", nil); r.Code != 401 {
		t.Fatalf("bad token: %d", r.Code)
	}
	if r := h.api("GET", "/api/v1/repos", "raw:Basic "+base64.StdEncoding.EncodeToString([]byte("alice:kata laluan alice")), nil); r.Code != 401 {
		t.Fatalf("password accepted: %d", r.Code)
	}

	// alice (repo:write token) sees both; the permission is reported
	if r := h.api("GET", "/api/v1/repos/alice/rahsia", "alice", nil); r.Code != 200 || r.obj(t)["permission"] != "write" { // admin access, capped by a repo:write token
		t.Fatalf("owner private: %d %s", r.Code, r.Body)
	}
	if m := h.api("GET", "/api/v1/user", "alice", nil).obj(t); m["login"] != "alice" || m["two_factor"] != false {
		t.Fatalf("user: %v", m)
	}
	// a read-only token: reads, cannot write, and its repository permission is capped at read
	scopes, _ := store.ParseScopes("repo:read")
	ro, _, _ := h.st.CreateToken(ctx, h.users["alice"].ID, "ro", scopes, 3600e9)
	if r := h.api("GET", "/api/v1/repos/alice/rahsia", "raw:Bearer "+ro, nil); r.obj(t)["permission"] != "read" {
		t.Fatalf("read token permission: %s", r.Body)
	}
	if r := h.api("POST", "/api/v1/repos/alice/awam/issues", "raw:Bearer "+ro, map[string]string{"title": "x"}); r.Code != 403 {
		t.Fatalf("write with a read token: %d", r.Code)
	}

	// git reads
	if l := h.api("GET", "/api/v1/repos/alice/awam/branches", "", nil).list(t); len(l) != 1 || l[0]["name"] != "main" {
		t.Fatalf("branches: %v", l)
	}
	if l := h.api("GET", "/api/v1/repos/alice/awam/tags", "", nil).list(t); len(l) != 1 || l[0]["name"] != "v1" {
		t.Fatalf("tags: %v", l)
	}
	cs := h.api("GET", "/api/v1/repos/alice/awam/commits?per_page=1", "", nil).list(t)
	if len(cs) != 1 || len(cs[0]["sha"].(string)) != 40 {
		t.Fatalf("commits: %v", cs)
	}
	head := cs[0]["sha"].(string)
	if l := h.api("GET", "/api/v1/repos/alice/awam/contents/", "", nil).list(t); len(l) < 3 {
		t.Fatalf("root listing: %v", l)
	}
	f := h.api("GET", "/api/v1/repos/alice/awam/contents/src/main.go", "", nil).obj(t)
	if raw, _ := base64.StdEncoding.DecodeString(f["content"].(string)); !strings.Contains(string(raw), "println(\"hai\")") {
		t.Fatalf("file: %v", f)
	}
	if r := h.api("GET", "/api/v1/repos/alice/awam/contents/tiada.txt", "", nil); r.Code != 404 {
		t.Fatal("missing path")
	}

	// issues: create (event + audit), list with a total, close, comment
	r = h.api("POST", "/api/v1/repos/alice/awam/issues", "alice", map[string]string{"title": "Dari API", "body": "Badan @bob"})
	if r.Code != 201 || r.obj(t)["number"].(float64) != 1 {
		t.Fatalf("create issue: %d %s", r.Code, r.Body)
	}
	if r := h.api("POST", "/api/v1/repos/alice/awam/issues", "alice", map[string]string{"title": "dua\nbaris"}); r.Code != 422 {
		t.Fatalf("two-line title: %d", r.Code)
	}
	if r := h.api("POST", "/api/v1/repos/alice/awam/issues", "alice", "bukan objek"); r.Code != 400 {
		t.Fatalf("bad JSON: %d", r.Code)
	}
	r = h.api("GET", "/api/v1/repos/alice/awam/issues", "", nil)
	if l := r.list(t); len(l) != 1 || r.Head.Get("X-Total-Count") != "1" {
		t.Fatalf("issue list: %s", r.Body)
	}
	evs, _ := h.st.EventsAfter(ctx, 0, 100)
	if len(evs) == 0 || evs[len(evs)-1].Kind != "issues" {
		t.Fatal("API issue wrote no event")
	}
	// bob may not close alice's issue; alice may
	if r := h.api("PATCH", "/api/v1/repos/alice/awam/issues/1", "bob", map[string]string{"state": "closed"}); r.Code != 403 {
		t.Fatalf("stranger closed: %d", r.Code)
	}
	if m := h.api("PATCH", "/api/v1/repos/alice/awam/issues/1", "alice", map[string]string{"state": "closed"}).obj(t); m["state"] != "closed" {
		t.Fatalf("close: %v", m)
	}
	if r := h.api("POST", "/api/v1/repos/alice/awam/issues/1/comments", "alice", map[string]string{"body": "Selesai."}); r.Code != 201 {
		t.Fatalf("comment: %d %s", r.Code, r.Body)
	}
	if l := h.api("GET", "/api/v1/repos/alice/awam/issues/1/comments", "", nil).list(t); len(l) != 1 || l[0]["body"] != "Selesai." {
		t.Fatalf("comments: %v", l)
	}
	// bob cannot see alice's private repository through any route
	for _, p := range []string{"/api/v1/repos/alice/rahsia", "/api/v1/repos/alice/rahsia/issues", "/api/v1/repos/alice/rahsia/contents/README.md", "/api/v1/repos/alice/rahsia/commits"} {
		if r := h.api("GET", p, "bob", nil); r.Code != 404 {
			t.Fatalf("%s for bob: %d", p, r.Code)
		}
	}
	_ = priv

	// the session cookie is not an API credential: a signed-in browser without a token is anonymous
	b := h.login("alice", "kata laluan alice")
	req, _ := http.NewRequest("POST", h.srv.URL+"/api/v1/repos/alice/awam/issues", strings.NewReader(`{"title":"csrf"}`))
	for _, c := range b.c.Jar.Cookies(mustURL(h.srv.URL)) {
		req.AddCookie(c)
	}
	if resp, err := http.DefaultClient.Do(req); err != nil || resp.StatusCode != 401 {
		t.Fatalf("cookie write: %v %d", err, resp.StatusCode)
	}

	// commit statuses: a CI runner reports; the commit page and the webhook event carry it
	if r := h.api("POST", "/api/v1/repos/alice/awam/statuses/"+head, "bob", map[string]string{"state": "success"}); r.Code != 403 {
		t.Fatalf("status without write access: %d", r.Code)
	}
	for _, bad := range []map[string]string{{"state": "ok"}, {"state": "success", "target_url": "javascript:alert(1)"}, {"state": "success", "context": "<b>"}} {
		if r := h.api("POST", "/api/v1/repos/alice/awam/statuses/"+head, "alice", bad); r.Code != 422 {
			t.Fatalf("bad status %v: %d", bad, r.Code)
		}
	}
	if r := h.api("POST", "/api/v1/repos/alice/awam/statuses/"+strings.Repeat("a", 40), "alice", map[string]string{"state": "success"}); r.Code != 404 {
		t.Fatalf("status on an unknown commit: %d", r.Code)
	}
	h.api("POST", "/api/v1/repos/alice/awam/statuses/"+head, "alice", map[string]string{"state": "pending", "context": "ci/ujian"})
	r = h.api("POST", "/api/v1/repos/alice/awam/statuses/"+head, "alice", map[string]string{"state": "success", "context": "ci/ujian", "description": "42 ujian lulus", "target_url": "https://ci.contoh.invalid/1"})
	if r.Code != 201 || r.obj(t)["state"] != "success" {
		t.Fatalf("status: %d %s", r.Code, r.Body)
	}
	h.api("POST", "/api/v1/repos/alice/awam/statuses/"+head, "alice", map[string]string{"state": "failure", "context": "lint"})
	m := h.api("GET", "/api/v1/repos/alice/awam/commits/main/statuses", "", nil).obj(t)
	if m["state"] != "failure" || len(m["statuses"].([]any)) != 2 {
		t.Fatalf("combined: %v", m)
	}
	if _, page := h.browser().get("/alice/awam/commit/"+head, "en"); !strings.Contains(page, "ci/ujian") || !strings.Contains(page, "42 ujian lulus") || !strings.Contains(page, `href="https://ci.contoh.invalid/1" rel="nofollow noopener noreferrer"`) {
		t.Fatal("statuses not on the commit page")
	}
	evs, _ = h.st.EventsAfter(ctx, 0, 100)
	if evs[len(evs)-1].Kind != "status" || evs[len(evs)-1].RepoID != pub.ID {
		t.Fatal("status event")
	}
	if r := h.api("GET", "/api/v1/openapi.json", "", nil); r.Code != 200 || !json.Valid(r.Body) {
		t.Fatal("openapi.json")
	}
	_ = url.Values{}
}
