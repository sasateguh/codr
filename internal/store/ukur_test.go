package store

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"testing"
	"time"
)

// TestUkurSQLite (D93, Fasa 3e) measures this store on a COPY of a real database: concurrent reads, concurrent writes
// and both together, with latency percentiles. It runs only when CODR_UKUR_DB names a database file (never in the gate);
// the file is copied first, so the original is never written.
//
//	CODR_UKUR_DB=/path/to/backup/codr.db go test ./internal/store -run TestUkurSQLite -v -count=1
func TestUkurSQLite(t *testing.T) {
	src := os.Getenv("CODR_UKUR_DB")
	if src == "" {
		t.Skip("CODR_UKUR_DB not set: measurement only")
	}
	dst := filepath.Join(t.TempDir(), "ukur.db")
	b, err := os.ReadFile(src) // #nosec G304 -- the operator names the file
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dst, b, 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := Open(dst)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	if _, _, err := s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	repos, err := s.Repos(ctx, "")
	if err != nil || len(repos) == 0 {
		t.Fatalf("repos: %d %v", len(repos), err)
	}
	r := repos[0]
	owner, err := s.UserByID(ctx, r.OwnerID)
	if err != nil {
		t.Fatal(err)
	}
	iss, err := s.CreateIssue(ctx, r.ID, owner.ID, "ukur", "ukur")
	if err != nil {
		t.Fatal(err)
	}
	info, _ := os.Stat(dst)
	t.Logf("DB %s: %.1f MB, %d repos", filepath.Base(src), float64(info.Size())/1e6, len(repos))
	read := func() error {
		if _, err := s.RepoByPath(ctx, r.Owner, r.Name); err != nil {
			return err
		}
		if _, err := s.AccessFor(ctx, owner.ID, r); err != nil {
			return err
		}
		_, _, err := s.Issues(ctx, r.ID, "open", 0, "", 0, 30, 0)
		return err
	}
	write := func() error { _, err := s.AddComment(ctx, iss.ID, owner.ID, "ukur komen"); return err }
	run := func(name string, readers, writers int, d time.Duration) {
		var mu sync.Mutex
		var rl, wl []time.Duration
		errs := 0
		stop := time.Now().Add(d)
		var wg sync.WaitGroup
		work := func(f func() error, into *[]time.Duration) {
			defer wg.Done()
			for time.Now().Before(stop) {
				t0 := time.Now()
				err := f()
				el := time.Since(t0)
				mu.Lock()
				if err != nil {
					errs++
				} else {
					*into = append(*into, el)
				}
				mu.Unlock()
			}
		}
		for i := 0; i < readers; i++ {
			wg.Add(1)
			go work(read, &rl)
		}
		for i := 0; i < writers; i++ {
			wg.Add(1)
			go work(write, &wl)
		}
		wg.Wait()
		pct := func(l []time.Duration, p float64) string {
			if len(l) == 0 {
				return "-"
			}
			sort.Slice(l, func(a, b int) bool { return l[a] < l[b] })
			return l[int(float64(len(l)-1)*p)].Round(10 * time.Microsecond).String()
		}
		t.Logf("%-22s pembaca %2d penulis %2d | baca %6.0f/s p50 %-8s p95 %-8s p99 %-8s | tulis %5.0f/s p50 %-8s p95 %-8s p99 %-8s | ralat %d",
			name, readers, writers, float64(len(rl))/d.Seconds(), pct(rl, .5), pct(rl, .95), pct(rl, .99),
			float64(len(wl))/d.Seconds(), pct(wl, .5), pct(wl, .95), pct(wl, .99), errs)
	}
	d := 4 * time.Second
	run("baca sahaja", 16, 0, d)
	run("tulis sahaja", 0, 8, d)
	run("campuran", 16, 4, d)
	run("campuran berat", 64, 16, d)
}
