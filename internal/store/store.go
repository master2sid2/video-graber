// Package store persists users, sessions, invites, categories, history and
// settings in a SQLite database (pure Go driver, no cgo).
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// ErrNotFound is returned when a row does not exist.
var ErrNotFound = errors.New("not found")

// ErrConflict is returned on unique constraint violations.
var ErrConflict = errors.New("already exists")

// Store wraps the database handle.
type Store struct {
	db *sql.DB
}

var migrations = []string{
	// 1: initial schema
	`
CREATE TABLE users (
	id            INTEGER PRIMARY KEY,
	username      TEXT NOT NULL UNIQUE COLLATE NOCASE,
	password_hash TEXT NOT NULL,
	is_admin      INTEGER NOT NULL DEFAULT 0,
	disabled      INTEGER NOT NULL DEFAULT 0,
	created_at    INTEGER NOT NULL,
	last_login_at INTEGER
);
CREATE TABLE sessions (
	token_hash TEXT PRIMARY KEY,
	user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	created_at INTEGER NOT NULL,
	expires_at INTEGER NOT NULL,
	ip         TEXT NOT NULL DEFAULT '',
	user_agent TEXT NOT NULL DEFAULT ''
);
CREATE INDEX sessions_user ON sessions(user_id);
CREATE TABLE invites (
	id         INTEGER PRIMARY KEY,
	code       TEXT NOT NULL UNIQUE,
	note       TEXT NOT NULL DEFAULT '',
	created_by INTEGER REFERENCES users(id) ON DELETE SET NULL,
	created_at INTEGER NOT NULL,
	expires_at INTEGER,
	max_uses   INTEGER NOT NULL DEFAULT 1,
	uses       INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE categories (
	id         INTEGER PRIMARY KEY,
	user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	name       TEXT NOT NULL,
	color      TEXT NOT NULL DEFAULT '',
	created_at INTEGER NOT NULL,
	UNIQUE(user_id, name)
);
CREATE TABLE history (
	id          INTEGER PRIMARY KEY,
	user_id     INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	category_id INTEGER REFERENCES categories(id) ON DELETE SET NULL,
	page_url    TEXT NOT NULL,
	title       TEXT NOT NULL DEFAULT '',
	subtitle    TEXT NOT NULL DEFAULT '',
	thumbnail   TEXT NOT NULL DEFAULT '',
	extractor   TEXT NOT NULL DEFAULT '',
	options     TEXT NOT NULL DEFAULT '{}',
	status      TEXT NOT NULL DEFAULT '',
	error       TEXT NOT NULL DEFAULT '',
	size        INTEGER NOT NULL DEFAULT 0,
	created_at  INTEGER NOT NULL,
	finished_at INTEGER
);
CREATE INDEX history_user ON history(user_id, created_at DESC);
CREATE TABLE settings (
	key   TEXT PRIMARY KEY,
	value TEXT NOT NULL
);
CREATE TABLE user_settings (
	user_id    INTEGER PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
	prefs      TEXT NOT NULL DEFAULT '{}',
	cookies    TEXT NOT NULL DEFAULT '',
	updated_at INTEGER NOT NULL
);
`,
	// 2: persisted job queue
	`
CREATE TABLE jobs (
	id         TEXT PRIMARY KEY,
	owner      INTEGER NOT NULL DEFAULT 0,
	data       TEXT NOT NULL,
	updated_at INTEGER NOT NULL
);
CREATE INDEX jobs_updated ON jobs(updated_at);
`,
}

// Open opens (and migrates) the database inside dir.
func Open(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	path := filepath.Join(dir, "videograber.db")
	dsn := "file:" + path + "?_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=synchronous(NORMAL)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(4)
	s := &Store{db: db}
	if err := s.migrate(context.Background()); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}
	return s, nil
}

// Close closes the database.
func (s *Store) Close() error { return s.db.Close() }

func (s *Store) migrate(ctx context.Context) error {
	var version int
	if err := s.db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return err
	}
	for i := version; i < len(migrations); i++ {
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, migrations[i]); err != nil {
			tx.Rollback()
			return fmt.Errorf("migration %d: %w", i+1, err)
		}
		if _, err := tx.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", i+1)); err != nil {
			tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}

func now() int64 { return time.Now().Unix() }

func unixPtr(v sql.NullInt64) *time.Time {
	if !v.Valid {
		return nil
	}
	return new(time.Unix(v.Int64, 0))
}

func isUnique(err error) bool {
	return err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed")
}

// ---------- settings ----------

// GetSetting returns a global setting value ("" when missing).
func (s *Store) GetSetting(ctx context.Context, key string) (string, error) {
	var v string
	err := s.db.QueryRowContext(ctx, "SELECT value FROM settings WHERE key = ?", key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return v, err
}

// SetSetting stores a global setting; an empty value deletes it.
func (s *Store) SetSetting(ctx context.Context, key, value string) error {
	if value == "" {
		_, err := s.db.ExecContext(ctx, "DELETE FROM settings WHERE key = ?", key)
		return err
	}
	_, err := s.db.ExecContext(ctx,
		"INSERT INTO settings(key, value) VALUES(?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value", key, value)
	return err
}
