package store

import (
	"context"
	"testing"
)

// TestPulls: shared #n with issues, one open pull per branch pair, state transitions, comments,
// reviews where the latest verdict per reviewer stands.
func TestPulls(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	u, _ := s.CreateUser(ctx, "alice", "", "x", false, "ms-MY")
	v, _ := s.CreateUser(ctx, "bob", "", "x", false, "en-US")
	r, _ := s.CreateRepo(ctx, u.ID, "p", "", true, "main")
	i1, _ := s.CreateIssue(ctx, r.ID, u.ID, "Isu", "")
	p1, err := s.CreatePull(ctx, r.ID, u.ID, "Ciri", "badan", "ciri", "main")
	if err != nil || i1.Number != 1 || p1.Number != 2 || p1.State != "open" || p1.Author != "alice" {
		t.Fatalf("shared numbering: issue %d pull %d %v", i1.Number, p1.Number, err)
	}
	if i2, _ := s.CreateIssue(ctx, r.ID, u.ID, "Isu 2", ""); i2.Number != 3 {
		t.Fatalf("issue after pull: %d", i2.Number)
	}
	if got, err := s.OpenPullFor(ctx, r.ID, "ciri", "main"); err != nil || got.ID != p1.ID {
		t.Fatalf("open pull for pair: %v", err)
	}
	if _, err := s.OpenPullFor(ctx, r.ID, "lain", "main"); err != ErrNotFound {
		t.Fatalf("missing pair: %v", err)
	}
	if _, err := s.PullByNumber(ctx, r.ID, 1); err != ErrNotFound { // #1 is an issue
		t.Fatalf("issue number is not a pull: %v", err)
	}
	if _, err := s.AddPullComment(ctx, p1.ID, v.ID, "ok?"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddReview(ctx, p1.ID, v.ID, "changes", "ubah"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddReview(ctx, p1.ID, v.ID, "lulus", ""); err == nil {
		t.Fatal("bad verdict accepted")
	}
	if got, _ := s.PullByID(ctx, p1.ID); got.Comments != 1 || got.Approvals != 0 {
		t.Fatalf("counts after changes-requested: %+v", got)
	}
	if _, err := s.AddReview(ctx, p1.ID, v.ID, "approve", "ok"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddReview(ctx, p1.ID, u.ID, "comment", "nota"); err != nil {
		t.Fatal(err)
	}
	got, _ := s.PullByID(ctx, p1.ID)
	vd, _ := s.Verdicts(ctx, p1.ID)
	if got.Approvals != 1 || vd["bob"] != "approve" || vd["alice"] != "" {
		t.Fatalf("latest verdict per reviewer stands: %+v %v", got, vd)
	}
	list, total, _ := s.Pulls(ctx, r.ID, "open", "ciri", 0, 10, 0)
	if total != 1 || len(list) != 1 || list[0].Comments != 1 {
		t.Fatalf("list open: %d %+v", total, list)
	}
	if err := s.SetPullState(ctx, p1.ID, true); err != nil {
		t.Fatal(err)
	}
	if err := s.SetPullMerged(ctx, p1.ID, u.ID, "abc"); err != ErrNotFound {
		t.Fatalf("merging a closed pull: %v", err)
	}
	if err := s.SetPullState(ctx, p1.ID, false); err != nil {
		t.Fatal(err)
	}
	if err := s.SetPullMerged(ctx, p1.ID, u.ID, "0123456789012345678901234567890123456789"); err != nil {
		t.Fatal(err)
	}
	got, _ = s.PullByID(ctx, p1.ID)
	if !got.Merged() || got.MergedBy != "alice" || got.ClosedAt == "" {
		t.Fatalf("merged: %+v", got)
	}
	if err := s.SetPullState(ctx, p1.ID, false); err != ErrNotFound {
		t.Fatalf("reopening a merged pull: %v", err)
	}
	if o, c, _ := s.PullCounts(ctx, r.ID); o != 0 || c != 1 {
		t.Fatalf("counts: %d %d", o, c)
	}
	if _, total, _ := s.Pulls(ctx, r.ID, "closed", "", 0, 10, 0); total != 1 {
		t.Fatalf("closed list: %d", total)
	}
}
