package web

import (
	"context"
	"strconv"
	"strings"
	"time"

	"codr.my/codr/internal/i18n"
	"codr.my/codr/internal/store"
)

// profileActions are the audit actions that count as a person's activity on their profile (D62).
var profileActions = []string{"git.push", "repo.create", "issue.open", "issue.close", "pr.open", "pr.merge", "pr.review"}

// Calendar geometry (SVG user units): one cell per day, one column per week, Sunday on top.
const (
	calCell   = 11
	calStep   = 14
	calTop    = 18
	calMaxRow = 20000 // audit rows read for one profile year
	calRecent = 8
)

type calDay struct {
	X, Y, Level, N int
	Date           string
}

type calMonth struct {
	X     int
	Label string
}

type activity struct {
	Actor  string
	Day    string // set on the first event of each day in a feed: the day heading
	At     time.Time
	Key    string // locale key of the sentence (profile: no subject)
	Feed   string // locale key with the actor as subject (dashboard)
	Repo   string
	Link   string
	Branch string
	Num    string // issue or pull request number, when the event is about one
	Title  string // its current title, read only after the visibility check
}

type profileData struct {
	Name   string
	Joined time.Time
	Days   []calDay
	Months []calMonth
	Total  int
	W, H   int
	Recent []activity
}

// profile builds a person's year of activity as the VIEWER may see it: an event on a repository the viewer
// cannot read is not counted, not shown and not hinted at (D62).
func (s *Server) profile(ctx context.Context, viewerID int64, u store.User, lang string, now time.Time) *profileData {
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	start := today.AddDate(0, 0, -364)
	start = start.AddDate(0, 0, -int(start.Weekday()))
	d := &profileData{Name: u.Name}
	if t, err := time.Parse(time.RFC3339Nano, u.CreatedAt); err == nil {
		d.Joined = t
	}
	rows, err := s.Store.ActivityOf(ctx, u.ID, start.Format(time.RFC3339), profileActions, calMaxRow)
	if err != nil {
		rows = nil
	}
	visible := s.visibleTo(ctx, viewerID)
	title := s.threadTitles(ctx)
	perDay := map[string]int{}
	for _, r := range rows {
		path, _, _ := strings.Cut(r.Target, "#")
		if !visible(path) {
			continue
		}
		at, err := time.Parse(time.RFC3339Nano, r.At)
		if err != nil {
			continue
		}
		perDay[at.UTC().Format("2006-01-02")]++
		d.Total++
		if len(d.Recent) < calRecent {
			if a, ok := toActivity(r, at); ok {
				a.Title = title(a)
				d.Recent = append(d.Recent, a)
			}
		}
	}
	max := 0
	for _, n := range perDay {
		if n > max {
			max = n
		}
	}
	lastMonthX := -100
	for day, i := start, 0; !day.After(today); day, i = day.AddDate(0, 0, 1), i+1 {
		key := day.Format("2006-01-02")
		n := perDay[key]
		lv := 0
		if n > 0 {
			lv = (4*n + max - 1) / max
		}
		x := (i / 7) * calStep
		d.Days = append(d.Days, calDay{X: x, Y: calTop + int(day.Weekday())*calStep, Level: lv, N: n, Date: key})
		if day.Day() == 1 && x-lastMonthX >= 3*calStep {
			d.Months = append(d.Months, calMonth{X: x, Label: monthShort(lang, day.Month())})
			lastMonthX = x
		}
	}
	d.W = (len(d.Days)+6)/7*calStep - (calStep - calCell)
	d.H = calTop + 7*calStep - (calStep - calCell)
	return d
}

func monthShort(lang string, m time.Month) string {
	return i18n.T(lang, "ui.month."+strconv.Itoa(int(m)))
}

// visibleTo is THE check for activity shown to a viewer (profile D62, dashboard feed D68): a cached "may the
// viewer read this repository" by owner/name. Events on anything else are dropped before they are counted.
func (s *Server) visibleTo(ctx context.Context, viewerID int64) func(path string) bool {
	canSee := map[string]bool{}
	return func(path string) bool {
		if v, ok := canSee[path]; ok {
			return v
		}
		owner, name, _ := strings.Cut(path, "/")
		ok := false
		if repo, err := s.Store.RepoByPath(ctx, owner, name); err == nil {
			if a, err := s.Store.AccessFor(ctx, viewerID, repo); err == nil && a >= store.AccessRead {
				ok = true
			}
		}
		canSee[path] = ok
		return ok
	}
}

// threadTitles returns a cached lookup of the current title of the issue or pull request an activity line is
// about. It is called only for lines that already passed visibleTo, so a title never leaks past that check.
func (s *Server) threadTitles(ctx context.Context) func(a activity) string {
	seen := map[string]string{}
	return func(a activity) string {
		if a.Num == "" {
			return ""
		}
		pull := strings.Contains(a.Link, "/pr/")
		key := a.Link
		if t, ok := seen[key]; ok {
			return t
		}
		t := ""
		owner, name, _ := strings.Cut(a.Repo, "/")
		n, err := strconv.ParseInt(a.Num, 10, 64)
		if repo, rerr := s.Store.RepoByPath(ctx, owner, name); err == nil && rerr == nil {
			if pull {
				if p, err := s.Store.PullByNumber(ctx, repo.ID, n); err == nil {
					t = p.Title
				}
			} else if i, err := s.Store.IssueByNumber(ctx, repo.ID, n); err == nil {
				t = i.Title
			}
		}
		seen[key] = t
		return t
	}
}

// toActivity turns one audit row into a linked activity line.
func toActivity(r store.AuditRow, at time.Time) (activity, bool) {
	path, num, _ := strings.Cut(r.Target, "#")
	if path == "" {
		return activity{}, false
	}
	act := strings.ReplaceAll(r.Action, ".", "_")
	a := activity{Actor: r.Actor, At: at, Key: "ui.profile.act." + act, Feed: "ui.feed.act." + act, Repo: path, Link: "/" + path}
	switch {
	case strings.HasPrefix(r.Action, "issue.") && num != "":
		a.Link += "/isu/" + num
		a.Num = num
	case strings.HasPrefix(r.Action, "pr.") && num != "":
		a.Link += "/pr/" + num
		a.Num = num
	case r.Action == "git.push":
		if _, ref, ok := strings.Cut(r.Detail, "refs/heads/"); ok {
			a.Branch, _, _ = strings.Cut(ref, " ")
		}
	}
	return a, true
}

// feedRows bounds the audit rows read for one dashboard; feedShown is how many lines it shows.
const (
	feedRows  = 2000
	feedShown = 30
)

// feed is the dashboard's activity: the newest events by anyone on repositories the viewer may read (D68).
func (s *Server) feed(ctx context.Context, viewerID int64, lang string, now time.Time) []activity {
	rows, err := s.Store.ActivityRecent(ctx, profileActions, feedRows)
	if err != nil {
		return nil
	}
	visible := s.visibleTo(ctx, viewerID)
	title := s.threadTitles(ctx)
	var out []activity
	last := ""
	for _, r := range rows {
		if len(out) >= feedShown {
			break
		}
		path, _, _ := strings.Cut(r.Target, "#")
		if r.Actor == "" || !visible(path) { // CLI actions have no person behind them; the feed is about people
			continue
		}
		at, err := time.Parse(time.RFC3339Nano, r.At)
		if err != nil {
			continue
		}
		a, ok := toActivity(r, at)
		if !ok {
			continue
		}
		a.Title = title(a)
		if day := at.UTC().Format("2006-01-02"); day != last {
			last = day
			switch day {
			case now.UTC().Format("2006-01-02"):
				a.Day = i18n.T(lang, "ui.feed.today")
			case now.UTC().AddDate(0, 0, -1).Format("2006-01-02"):
				a.Day = i18n.T(lang, "ui.feed.yesterday")
			default:
				a.Day = day
			}
		}
		out = append(out, a)
	}
	return out
}
