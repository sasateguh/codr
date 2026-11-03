package store

import "context"

// CloudConfig is the single cloud backup configuration row (D70). Secret and key are sealed values.
type CloudConfig struct {
	Enabled       bool
	Endpoint      string
	Region        string
	Bucket        string
	Prefix        string
	AccessKey     string
	SecretSealed  string
	KeySealed     string
	KeyHint       string
	Keep          int
	IntervalHours int
	UpdatedAt     string
}

// CloudRun is one backup run.
type CloudRun struct {
	ID         int64
	StartedAt  string
	FinishedAt string
	Status     string // running | ok | failed
	Object     string
	Bytes      int64
	Repos      int
	Detail     string
	Trigger    string // schedule | admin | cli
}

// CloudConfig reads the configuration row.
func (s *Store) CloudConfig(ctx context.Context) (CloudConfig, error) {
	var c CloudConfig
	var en int
	err := s.db.QueryRowContext(ctx, `SELECT enabled, endpoint, region, bucket, prefix, access_key, secret_sealed, key_sealed, key_hint, keep, interval_hours, updated_at FROM cloud_backup WHERE id = 1`).
		Scan(&en, &c.Endpoint, &c.Region, &c.Bucket, &c.Prefix, &c.AccessKey, &c.SecretSealed, &c.KeySealed, &c.KeyHint, &c.Keep, &c.IntervalHours, &c.UpdatedAt)
	c.Enabled = en == 1
	return c, err
}

// SetCloudConfig replaces the configuration row.
func (s *Store) SetCloudConfig(ctx context.Context, c CloudConfig) error {
	en := 0
	if c.Enabled {
		en = 1
	}
	_, err := s.db.ExecContext(ctx, `UPDATE cloud_backup SET enabled = ?, endpoint = ?, region = ?, bucket = ?, prefix = ?, access_key = ?, secret_sealed = ?, key_sealed = ?, key_hint = ?, keep = ?, interval_hours = ?, updated_at = ? WHERE id = 1`,
		en, c.Endpoint, c.Region, c.Bucket, c.Prefix, c.AccessKey, c.SecretSealed, c.KeySealed, c.KeyHint, c.Keep, c.IntervalHours, Now())
	return err
}

// StartCloudRun records a run as running and returns its id.
func (s *Store) StartCloudRun(ctx context.Context, trigger string) (int64, error) {
	res, err := s.db.ExecContext(ctx, `INSERT INTO cloud_backup_runs (started_at, status, trigger) VALUES (?, 'running', ?)`, Now(), trigger)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// FinishCloudRun closes a run.
func (s *Store) FinishCloudRun(ctx context.Context, id int64, status, object string, bytes int64, repos int, detail string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE cloud_backup_runs SET finished_at = ?, status = ?, object = ?, bytes = ?, repos = ?, detail = ? WHERE id = ?`,
		Now(), status, object, bytes, repos, detail, id)
	return err
}

// FailStaleCloudRuns marks runs left "running" by a process that stopped (called when serve starts).
func (s *Store) FailStaleCloudRuns(ctx context.Context, detail string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE cloud_backup_runs SET finished_at = ?, status = 'failed', detail = ? WHERE status = 'running'`, Now(), detail)
	return err
}

// CloudRuns lists the newest runs.
func (s *Store) CloudRuns(ctx context.Context, limit int) ([]CloudRun, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, started_at, finished_at, status, object, bytes, repos, detail, trigger FROM cloud_backup_runs ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []CloudRun
	for rows.Next() {
		var r CloudRun
		if err := rows.Scan(&r.ID, &r.StartedAt, &r.FinishedAt, &r.Status, &r.Object, &r.Bytes, &r.Repos, &r.Detail, &r.Trigger); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
