package store

import (
	"context"
	"testing"
	"time"
)

// TestOrgAccess: every person × repository × base role, AccessFor and ReposVisible agree, and the rules hold:
// owner = admin, member = base role, team = its role on its repos, outsider = public only (D74).
func TestOrgAccess(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	mk := func(n string) User { u, _ := s.CreateUser(ctx, n, "", "h", false, "en-US"); return u }
	owner, member, teamer, outsider, other := mk("pemilik"), mk("ahli"), mk("pasukan"), mk("luar"), mk("orglain")
	org, err := s.CreateOrg(ctx, "syarikat", owner.ID)
	if err != nil {
		t.Fatal(err)
	}
	other2, _ := s.CreateOrg(ctx, "lain", other.ID)
	secret, _ := s.CreateRepo(ctx, org.ID, "rahsia", "", true, "main")
	tool, _ := s.CreateRepo(ctx, org.ID, "alat", "", true, "main")
	open, _ := s.CreateRepo(ctx, org.ID, "terbuka", "", false, "main")
	foreign, _ := s.CreateRepo(ctx, other2.ID, "asing", "", true, "main")
	_ = s.SetOrgMember(ctx, org.ID, member.ID, "member")
	_ = s.SetOrgMember(ctx, org.ID, teamer.ID, "member")
	team, _ := s.CreateTeam(ctx, org.ID, "pembangun")
	if err := s.SetTeamMember(ctx, org.ID, team, teamer.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.SetTeamRepo(ctx, org.ID, team, tool.ID, "write"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetTeamRepo(ctx, org.ID, team, foreign.ID, "admin"); err == nil {
		t.Fatal("a team reached another organisation's repository")
	}
	if err := s.SetTeamMember(ctx, org.ID, team, outsider.ID); err == nil {
		t.Fatal("someone outside the org joined a team")
	}
	repos := []Repo{secret, tool, open, foreign}
	people := []User{owner, member, teamer, outsider}
	want := map[string]map[string][]Access{ // base role → person → [rahsia, alat, terbuka, asing]
		"read":  {"pemilik": {AccessAdmin, AccessAdmin, AccessAdmin, AccessNone}, "ahli": {AccessRead, AccessRead, AccessRead, AccessNone}, "pasukan": {AccessRead, AccessWrite, AccessRead, AccessNone}, "luar": {AccessNone, AccessNone, AccessRead, AccessNone}},
		"none":  {"pemilik": {AccessAdmin, AccessAdmin, AccessAdmin, AccessNone}, "ahli": {AccessNone, AccessNone, AccessRead, AccessNone}, "pasukan": {AccessNone, AccessWrite, AccessRead, AccessNone}, "luar": {AccessNone, AccessNone, AccessRead, AccessNone}},
		"write": {"pemilik": {AccessAdmin, AccessAdmin, AccessAdmin, AccessNone}, "ahli": {AccessWrite, AccessWrite, AccessWrite, AccessNone}, "pasukan": {AccessWrite, AccessWrite, AccessWrite, AccessNone}, "luar": {AccessNone, AccessNone, AccessRead, AccessNone}},
	}
	for base, byPerson := range want {
		if err := s.SetOrgBaseRole(ctx, org.ID, base); err != nil {
			t.Fatal(err)
		}
		for _, p := range people {
			vis, _, err := s.ReposVisible(ctx, p.ID, "", "", "name", 100, 0)
			if err != nil {
				t.Fatal(err)
			}
			seen := map[int64]bool{}
			for _, r := range vis {
				seen[r.ID] = true
			}
			for i, r := range repos {
				got, err := s.AccessFor(ctx, p.ID, r)
				if err != nil {
					t.Fatal(err)
				}
				if got != byPerson[p.Name][i] {
					t.Errorf("base %s: %s on %s = %v, want %v", base, p.Name, r.Name, got, byPerson[p.Name][i])
				}
				if seen[r.ID] != (got >= AccessRead) {
					t.Errorf("base %s: %s on %s: list says %v, AccessFor says %v", base, p.Name, r.Name, seen[r.ID], got)
				}
			}
		}
	}
	// an organisation can never act as a person
	if _, err := s.AddSSHKey(ctx, org.ID, "k", "fp", "ssh-ed25519 AAAA"); err != ErrIsOrg {
		t.Fatalf("org key: %v", err)
	}
	if _, _, err := s.CreateToken(ctx, org.ID, "t", []string{"repo:read"}, time.Hour); err != ErrIsOrg {
		t.Fatalf("org token: %v", err)
	}
	if err := s.SetOrgMember(ctx, org.ID, other2.ID, "member"); err != ErrIsOrg {
		t.Fatalf("an org as a member: %v", err)
	}
	// the last owner cannot leave or be demoted
	if err := s.RemoveOrgMember(ctx, org.ID, owner.ID); err != ErrLastOwner {
		t.Fatalf("last owner removed: %v", err)
	}
	if err := s.SetOrgMember(ctx, org.ID, owner.ID, "member"); err != ErrLastOwner {
		t.Fatalf("last owner demoted: %v", err)
	}
	// leaving the org also leaves its teams
	if err := s.RemoveOrgMember(ctx, org.ID, teamer.ID); err != nil {
		t.Fatal(err)
	}
	if a, _ := s.AccessFor(ctx, teamer.ID, tool); a != AccessNone {
		t.Fatalf("former member kept team access: %v", a)
	}
	if _, err := s.OrgByName(ctx, "pemilik"); err != ErrNotFound {
		t.Fatal("a person loaded as an organisation")
	}
}
