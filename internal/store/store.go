// Package store is the only package that speaks SQL. It owns the SQLite file
// (D3: modernc.org/sqlite, WAL, one file) and the append-only migrations.
package store

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	_ "modernc.org/sqlite" // registers the "sqlite" driver
)

//go:embed migrations/*.sql
var migrationFS embed.FS

// ErrNotFound is returned when a row does not exist.
var ErrNotFound = errors.New("not found")

// ErrIsOrg means an organisation was asked to do what only a person can (sign in, hold a key or a token).
var ErrIsOrg = errors.New("store: an organisation cannot do that")

// ErrLastOwner means the change would leave an organisation without an owner.
var ErrLastOwner = errors.New("store: an organisation needs at least one owner")

// Store wraps the database handle.
type Store struct {
	db *sql.DB
}

// Open opens (creating if needed) the SQLite file at path in WAL mode with foreign keys on.
func Open(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	dsn := "file:" + path + "?_pragma=journal_mode(WAL)&_pragma=foreign_keys(ON)&_pragma=busy_timeout(5000)&_pragma=synchronous(NORMAL)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1) // SQLite: one writer; serialising in-process avoids SQLITE_BUSY storms
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, err
	}
	return &Store{db: db}, nil
}

// Close closes the database.
func (s *Store) Close() error { return s.db.Close() }

// DB exposes the handle for tests only.
func (s *Store) DB() *sql.DB { return s.db }

// timeFormat is how every timestamp is stored (UTC, RFC 3339 with millis).
const timeFormat = "2006-01-02T15:04:05.000Z"

// Now is the current time in timeFormat, the form used in every table.
func Now() string { return time.Now().UTC().Format(timeFormat) }

// migration is one embedded SQL file.
type migration struct {
	version int
	name    string
	sql     string
}

func migrations() ([]migration, error) {
	entries, err := fs.ReadDir(migrationFS, "migrations")
	if err != nil {
		return nil, err
	}
	var out []migration
	for _, e := range entries {
		n := e.Name()
		if !strings.HasSuffix(n, ".sql") {
			continue
		}
		v, err := strconv.Atoi(strings.SplitN(n, "_", 2)[0])
		if err != nil {
			return nil, fmt.Errorf("migration %s: bad version prefix", n)
		}
		raw, err := migrationFS.ReadFile("migrations/" + n)
		if err != nil {
			return nil, err
		}
		out = append(out, migration{version: v, name: n, sql: string(raw)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].version < out[j].version })
	for i, m := range out {
		if m.version != i+1 {
			return nil, fmt.Errorf("migration %s: versions must be contiguous from 1", m.name)
		}
	}
	return out, nil
}

// Version returns the applied schema version (0 = fresh file).
func (s *Store) Version(ctx context.Context) (int, error) {
	if _, err := s.db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (version INTEGER PRIMARY KEY, name TEXT NOT NULL, applied_at TEXT NOT NULL)`); err != nil {
		return 0, err
	}
	var v sql.NullInt64
	if err := s.db.QueryRowContext(ctx, `SELECT MAX(version) FROM schema_migrations`).Scan(&v); err != nil {
		return 0, err
	}
	return int(v.Int64), nil
}

// Pending reports how many migrations are not yet applied.
func (s *Store) Pending(ctx context.Context) (version, pending int, err error) {
	ms, err := migrations()
	if err != nil {
		return 0, 0, err
	}
	v, err := s.Version(ctx)
	if err != nil {
		return 0, 0, err
	}
	return v, len(ms) - v, nil
}

// Migrate applies every pending migration, each in its own transaction, and returns
// the final version and how many were applied.
func (s *Store) Migrate(ctx context.Context) (version, applied int, err error) {
	ms, err := migrations()
	if err != nil {
		return 0, 0, err
	}
	cur, err := s.Version(ctx)
	if err != nil {
		return 0, 0, err
	}
	for _, m := range ms {
		if m.version <= cur {
			continue
		}
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return cur, applied, err
		}
		if _, err := tx.ExecContext(ctx, m.sql); err != nil {
			tx.Rollback()
			return cur, applied, fmt.Errorf("migration %s: %w", m.name, err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO schema_migrations (version, name, applied_at) VALUES (?, ?, ?)`, m.version, m.name, Now()); err != nil {
			tx.Rollback()
			return cur, applied, err
		}
		if err := tx.Commit(); err != nil {
			return cur, applied, err
		}
		cur = m.version
		applied++
	}
	return cur, applied, nil
}

// Audit appends one row to audit_log (append-only by trigger).
func (s *Store) Audit(ctx context.Context, actorID int64, action, target, detail string) error {
	var actor any
	if actorID > 0 {
		actor = actorID
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO audit_log (at, actor_id, action, target, detail) VALUES (?, ?, ?, ?, ?)`, Now(), actor, action, target, detail)
	return err
}

// Setting reads one product setting as its raw string.
func (s *Store) Setting(ctx context.Context, key string) (string, error) {
	var v string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	return v, err
}

// SettingBool reads a bool setting ("true"/"false").
func (s *Store) SettingBool(ctx context.Context, key string) (bool, error) {
	v, err := s.Setting(ctx, key)
	if err != nil {
		return false, err
	}
	return v == "true", nil
}

// SettingInt reads an int setting.
func (s *Store) SettingInt(ctx context.Context, key string) (int64, error) {
	v, err := s.Setting(ctx, key)
	if err != nil {
		return 0, err
	}
	return strconv.ParseInt(v, 10, 64)
}

// SetSetting updates an existing setting, recording who changed it. Unknown keys are rejected
// so a typo cannot create a setting nobody reads.
func (s *Store) SetSetting(ctx context.Context, key, value string, actorID int64) error {
	var actor any
	if actorID > 0 {
		actor = actorID
	}
	res, err := s.db.ExecContext(ctx, `UPDATE settings SET value = ?, updated_at = ?, updated_by = ? WHERE key = ?`, value, Now(), actor, key)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// Setting row for listing.
type SettingRow struct {
	Key, Value, Type, UpdatedAt string
}

// Settings lists every product setting ordered by key.
func (s *Store) Settings(ctx context.Context) ([]SettingRow, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT key, value, type, updated_at FROM settings ORDER BY key`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SettingRow
	for rows.Next() {
		var r SettingRow
		if err := rows.Scan(&r.Key, &r.Value, &r.Type, &r.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ErrBadValue means the value does not match the setting's declared type.
var ErrBadValue = errors.New("value does not match setting type")

// SetSettingChecked validates value against the row's type, then updates it. Returns the
// type on ErrBadValue so the caller can name it.
func (s *Store) SetSettingChecked(ctx context.Context, key, value string, actorID int64) (typ string, err error) {
	if err := s.db.QueryRowContext(ctx, `SELECT type FROM settings WHERE key = ?`, key).Scan(&typ); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", ErrNotFound
		}
		return "", err
	}
	switch typ {
	case "bool":
		if value != "true" && value != "false" {
			return typ, ErrBadValue
		}
	case "int":
		if _, err := strconv.ParseInt(value, 10, 64); err != nil {
			return typ, ErrBadValue
		}
	}
	return typ, s.SetSetting(ctx, key, value, actorID)
}
