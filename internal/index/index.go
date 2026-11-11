// Package index is Woodpile's SQLite index (<data>/index.db): log files and their
// time checkpoints, devices, per-minute stats, the service's own events and the
// storage (deletion) history.
//
// The log files are the source of truth. Files and checkpoints can be rebuilt from
// them at any time with Reindex; devices keep their user-set name, group and hidden
// flag across a rebuild.
//
// All times are stored as Unix milliseconds and returned in UTC. File paths are
// relative to the data dir and '/'-separated ("logs/192.0.2.1/2026-11-10.log").
package index

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	_ "modernc.org/sqlite" // registers the "sqlite" driver (pure Go, no cgo)
)

// FileName is the index's file name inside the data dir.
const FileName = "index.db"

// Retention for the rows Prune removes, as defaults for its callers.
const (
	StatsRetention = 7 * 24 * time.Hour
)

// ErrNotFound is returned when a device or file doesn't exist.
var ErrNotFound = errors.New("index: not found")

// DB is an open index. It is safe for concurrent use.
type DB struct {
	db *sql.DB
}

// Open opens (creating if needed) the index at path, in WAL mode, and migrates its
// schema to the current version. A database from a newer Woodpile is refused.
func Open(path string) (*DB, error) {
	if strings.ContainsAny(path, "?#") {
		return nil, fmt.Errorf("index: unsupported character in path %q", path)
	}
	q := url.Values{}
	q.Add("_pragma", "busy_timeout(10000)")
	q.Add("_pragma", "journal_mode(WAL)")
	q.Add("_pragma", "synchronous(NORMAL)")
	q.Add("_pragma", "foreign_keys(1)")
	q.Set("_txlock", "immediate")
	sdb, err := sql.Open("sqlite", path+"?"+q.Encode())
	if err != nil {
		return nil, fmt.Errorf("index: open %s: %w", path, err)
	}
	sdb.SetMaxOpenConns(8)
	sdb.SetMaxIdleConns(4)
	sdb.SetConnMaxIdleTime(5 * time.Minute)
	d := &DB{db: sdb}
	if err := d.migrate(context.Background()); err != nil {
		sdb.Close()
		return nil, err
	}
	return d, nil
}

// Close closes the database.
func (d *DB) Close() error { return d.db.Close() }

// SchemaVersion returns the schema version the database is at.
func (d *DB) SchemaVersion(ctx context.Context) (int, error) {
	var v int
	err := d.db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&v)
	return v, err
}

// migrations[i] moves the schema from version i to i+1. Append only; never edit a
// released migration.
var migrations = []string{
	// 0 -> 1: initial schema.
	`
CREATE TABLE devices (
	id          TEXT PRIMARY KEY,
	ip          TEXT NOT NULL,
	name        TEXT NOT NULL DEFAULT '',
	grp         TEXT NOT NULL DEFAULT '',
	hidden      INTEGER NOT NULL DEFAULT 0,
	first_seen  INTEGER NOT NULL,
	last_heard  INTEGER NOT NULL,
	total_msgs  INTEGER NOT NULL DEFAULT 0,
	total_bytes INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE files (
	id             INTEGER PRIMARY KEY,
	device_id      TEXT NOT NULL,
	path           TEXT NOT NULL UNIQUE,
	day            TEXT NOT NULL,
	part           INTEGER NOT NULL,
	first_received INTEGER NOT NULL,
	last_received  INTEGER NOT NULL,
	size           INTEGER NOT NULL,
	raw_size       INTEGER NOT NULL,
	lines          INTEGER NOT NULL,
	compressed     INTEGER NOT NULL
);
CREATE INDEX files_device_time ON files (device_id, first_received);
CREATE INDEX files_time ON files (last_received);
CREATE TABLE checkpoints (
	file_id  INTEGER NOT NULL REFERENCES files (id) ON DELETE CASCADE,
	offset   INTEGER NOT NULL,
	line     INTEGER NOT NULL,
	received INTEGER NOT NULL,
	PRIMARY KEY (file_id, offset)
) WITHOUT ROWID;
CREATE TABLE stats (
	device_id TEXT NOT NULL,
	minute    INTEGER NOT NULL,
	msgs      INTEGER NOT NULL DEFAULT 0,
	bytes     INTEGER NOT NULL DEFAULT 0,
	truncated INTEGER NOT NULL DEFAULT 0,
	dropped   INTEGER NOT NULL DEFAULT 0,
	PRIMARY KEY (device_id, minute)
) WITHOUT ROWID;
CREATE INDEX stats_minute ON stats (minute);
CREATE TABLE events (
	id        INTEGER PRIMARY KEY,
	time      INTEGER NOT NULL,
	level     TEXT NOT NULL,
	kind      TEXT NOT NULL,
	device_id TEXT NOT NULL DEFAULT '',
	text      TEXT NOT NULL
);
CREATE INDEX events_time ON events (time);
CREATE TABLE deletions (
	id        INTEGER PRIMARY KEY,
	time      INTEGER NOT NULL,
	file      TEXT NOT NULL,
	device_id TEXT NOT NULL,
	bytes     INTEGER NOT NULL,
	reason    TEXT NOT NULL CHECK (reason IN ('age', 'cap', 'low_disk'))
);
CREATE INDEX deletions_time ON deletions (time);
`,
}

// CurrentSchema is the schema version this build writes.
var CurrentSchema = len(migrations)

func (d *DB) migrate(ctx context.Context) error {
	v, err := d.SchemaVersion(ctx)
	if err != nil {
		return fmt.Errorf("index: read schema version: %w", err)
	}
	if v > len(migrations) {
		return fmt.Errorf("index: schema version %d is newer than this build supports (%d)", v, len(migrations))
	}
	for ; v < len(migrations); v++ {
		tx, err := d.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, migrations[v]); err != nil {
			tx.Rollback()
			return fmt.Errorf("index: migrate %d -> %d: %w", v, v+1, err)
		}
		if _, err := tx.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", v+1)); err != nil {
			tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("index: migrate %d -> %d: %w", v, v+1, err)
		}
	}
	return nil
}

func ms(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixMilli()
}

func tm(v int64) time.Time {
	if v == 0 {
		return time.Time{}
	}
	return time.UnixMilli(v).UTC()
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

// inList returns "(?,?,?)" and the args for an IN clause.
func inList(ids []string) (string, []any) {
	var b strings.Builder
	args := make([]any, len(ids))
	b.WriteByte('(')
	for i, id := range ids {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteByte('?')
		args[i] = id
	}
	b.WriteByte(')')
	return b.String(), args
}
