package web

import (
	"context"
	"net/url"
	"strings"
	"testing"

	"codr.my/codr/internal/auth"
)

// TestOrgDoors: the organisation rules at the web and HTTPS doors (the SSH door has its own test): a stranger
// sees nothing of a private org repo, a member with base read can clone but not push, a team with write can
// push, an organisation cannot sign in, and only owners reach /org/{name} (D74).
func TestOrgDoors(t *testing.T) {
	h := newHarness(t)
	h.seed(t)
	ctx := context.Background()
	login := func(name string) *browser {
		hash, _ := auth.Hash("kata laluan "+name, auth.Params{Time: 1, Memory: 8 * 1024, Threads: 1})
		_ = h.st.SetPasswordHash(ctx, h.users[name].ID, hash)
		b := h.browser()
		b.lang = "en"
		b.get("/login", "en")
		if code, _, _ := b.post("/login", url.Values{"user": {name}, "password": {"kata laluan " + name}}); code != 303 {
			t.Fatalf("login %s", name)
		}
		return b
	}
	alice, bob := login("alice"), login("bob")
	if code, _, loc := alice.post("/org/baharu", url.Values{"name": {"syarikat"}}); code != 303 || loc != "/org/syarikat" {
		t.Fatalf("create org: %d %s", code, loc)
	}
	if code, _, _ := bob.post("/baharu", url.Values{"owner": {"syarikat"}, "name": {"curi"}, "visibility": {"private"}}); code != 403 {
		t.Fatalf("stranger creating a repo in the org: %d", code)
	}
	if code, _, loc := alice.post("/baharu", url.Values{"owner": {"syarikat"}, "name": {"rahsia"}, "visibility": {"private"}}); code != 303 || loc != "/syarikat/rahsia" {
		t.Fatalf("owner creates org repo: %d %s", code, loc)
	}
	// push something with alice's token (she owns the org)
	w := h.work()
	h.commit(w, "a.txt", "satu\n")
	if out, err := h.git(w, "push", "-q", h.url("syarikat/rahsia", "alice"), "main"); err != nil {
		t.Fatalf("owner push: %s", out)
	}
	clone := func(cred string) error {
		_, err := h.git(h.work(), "ls-remote", h.url("syarikat/rahsia", cred))
		return err
	}
	n := 0
	push := func(cred string) error {
		n++
		h.commit(w, "b.txt", cred+itoa(n)+"\n")
		_, err := h.git(w, "push", "-q", h.url("syarikat/rahsia", cred), "main")
		return err
	}
	// a stranger: nothing at either door
	if code, _ := bob.get("/syarikat/rahsia", "en"); code != 404 {
		t.Fatalf("stranger web: %d", code)
	}
	if clone("bob") == nil {
		t.Fatal("stranger cloned a private org repo over HTTPS")
	}
	if code, _ := bob.get("/org/syarikat", "en"); code != 404 {
		t.Fatalf("stranger on the management page: %d", code)
	}
	// member with the default base role (read): clone yes, push no
	if code, _, _ := alice.post("/org/syarikat/urus/ahli", url.Values{"user": {"bob"}, "role": {"member"}}); code != 303 {
		t.Fatalf("add member: %d", code)
	}
	if code, _ := bob.get("/syarikat/rahsia", "en"); code != 200 {
		t.Fatalf("member web: %d", code)
	}
	if err := clone("bob"); err != nil {
		t.Fatalf("member clone: %v", err)
	}
	if push("bob") == nil {
		t.Fatal("member with read pushed")
	}
	if code, _, _ := bob.post("/org/syarikat/urus/asas", url.Values{"role": {"write"}}); code != 404 {
		t.Fatalf("member changed the base role: %d", code)
	}
	// a team with write on this repo: push yes
	if code, _, _ := alice.post("/org/syarikat/urus/pasukan", url.Values{"name": {"pembangun"}}); code != 303 {
		t.Fatalf("create team: %d", code)
	}
	var team, repo int64
	_ = h.st.DB().QueryRow(`SELECT id FROM teams WHERE name='pembangun'`).Scan(&team)
	_ = h.st.DB().QueryRow(`SELECT r.id FROM repos r JOIN users u ON u.id=r.owner_id WHERE u.name='syarikat' AND r.name='rahsia'`).Scan(&repo)
	tid, rid := itoa(int(team)), itoa(int(repo))
	alice.post("/org/syarikat/urus/pasukan-ahli", url.Values{"team": {tid}, "user": {"bob"}, "op": {"add"}})
	if code, _, _ := alice.post("/org/syarikat/urus/pasukan-repo", url.Values{"team": {tid}, "repo": {rid}, "role": {"write"}}); code != 303 {
		t.Fatalf("grant team: %d", code)
	}
	if err := push("bob"); err != nil {
		t.Fatalf("team member with write could not push: %v", err)
	}
	// the org itself can never sign in, even with the right form
	anon := h.browser()
	anon.get("/login", "en")
	if code, body, _ := anon.post("/login", url.Values{"user": {"syarikat"}, "password": {"!"}}); code != 401 || !strings.Contains(body, "Wrong username or password") {
		t.Fatalf("org signed in: %d", code)
	}
	// the last owner stays
	if code, body, _ := alice.post("/org/syarikat/urus/buang", url.Values{"user": {"alice"}}); code != 400 || !strings.Contains(body, "at least one owner") {
		t.Fatalf("last owner removed: %d", code)
	}
	// the org page shows the org, not a person's calendar
	if _, body := bob.get("/syarikat", "en"); !strings.Contains(body, `class="org-kepala"`) || strings.Contains(body, `class="kalendar"`) {
		t.Fatal("org page")
	}
	// taking bob out of the org removes the team access too
	alice.post("/org/syarikat/urus/buang", url.Values{"user": {"bob"}})
	if clone("bob") == nil {
		t.Fatal("removed member still clones")
	}
}
