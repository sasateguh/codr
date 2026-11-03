package cloud

import (
	"context"
	"log/slog"
	"time"
)

// retryAfter is how soon a failed run is tried again (instead of waiting the whole interval).
const retryAfter = time.Hour

// Due reports whether a run should start now: none yet, the last one older than the interval, or the last
// one failed more than retryAfter ago.
func Due(lastStarted string, lastStatus string, intervalHours int, now time.Time) bool {
	if lastStarted == "" {
		return true
	}
	t, err := time.Parse(time.RFC3339Nano, lastStarted)
	if err != nil {
		return true
	}
	age := now.Sub(t)
	if lastStatus == "running" {
		return false
	}
	if lastStatus == "failed" && age >= retryAfter {
		return true
	}
	return age >= time.Duration(intervalHours)*time.Hour
}

// Schedule checks every tick whether a run is due and runs it in the background loop of `serve`.
func (j *Job) Schedule(ctx context.Context, tick time.Duration, log *slog.Logger) {
	t := time.NewTicker(tick)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		cfg, err := j.Store.CloudConfig(ctx)
		if err != nil || !cfg.Enabled || !Configured(cfg) || j.Running() {
			continue
		}
		runs, err := j.Store.CloudRuns(ctx, 1)
		if err != nil {
			continue
		}
		last, status := "", ""
		if len(runs) > 0 {
			last, status = runs[0].StartedAt, runs[0].Status
		}
		if !Due(last, status, cfg.IntervalHours, time.Now()) {
			continue
		}
		out, err := j.Run(ctx, "schedule")
		if err != nil {
			log.Error("cloud backup", "err", err)
			continue
		}
		log.Info("cloud backup", "object", out.Object, "bytes", out.Bytes, "repos", out.Repos, "pruned", out.Pruned)
	}
}
