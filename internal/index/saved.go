package index

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"strings"
	"time"
)

// Saved searches (Events, SPEC §10.1.4): named, shared by everyone who can
// open the UI, stored one row each. The query is opaque JSON here; the caller
// validates it.

// MaxSavedSearches is how many saved searches there can be, of all types.
const MaxSavedSearches = 200

// Saved-search errors.
var (
	ErrSavedLimit   = errors.New("index: too many saved searches")
	ErrNameTaken    = errors.New("index: a saved search with that name already exists")
	ErrSavedChanged = errors.New("index: the saved search changed")
)

// SavedSearch is one saved search.
type SavedSearch struct {
	ID      string // "ss_" and 8 hex digits
	Name    string
	Type    string
	Query   string // JSON
	Created time.Time
	Updated time.Time
	Version int64 // 1 when created, +1 on each update
}

// NameKey folds a saved search's name for the unique-ignoring-case rule.
func NameKey(name string) string { return strings.ToLower(strings.TrimSpace(name)) }

const savedCols = `id, name, type, query, created, updated, version`

func scanSaved(r interface{ Scan(...any) error }) (SavedSearch, error) {
	var s SavedSearch
	var c, u int64
	err := r.Scan(&s.ID, &s.Name, &s.Type, &s.Query, &c, &u, &s.Version)
	s.Created, s.Updated = tm(c), tm(u)
	return s, err
}

// SavedSearches returns the saved searches of type typ ("" for every type),
// sorted by name ignoring case.
func (d *DB) SavedSearches(ctx context.Context, typ string) ([]SavedSearch, error) {
	q := `SELECT ` + savedCols + ` FROM saved_searches`
	var args []any
	if typ != "" {
		q += ` WHERE type = ?`
		args = append(args, typ)
	}
	rows, err := d.db.QueryContext(ctx, q+` ORDER BY name_key, id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []SavedSearch{}
	for rows.Next() {
		s, err := scanSaved(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// SavedSearch returns one saved search, or ErrNotFound.
func (d *DB) SavedSearch(ctx context.Context, id string) (SavedSearch, error) {
	s, err := scanSaved(d.db.QueryRowContext(ctx, `SELECT `+savedCols+` FROM saved_searches WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return SavedSearch{}, ErrNotFound
	}
	return s, err
}

func nameTaken(ctx context.Context, tx *sql.Tx, key, exceptID string) (bool, error) {
	var n int
	err := tx.QueryRowContext(ctx, `SELECT count(*) FROM saved_searches WHERE name_key = ? AND id != ?`, key, exceptID).Scan(&n)
	return n > 0, err
}

// CreateSavedSearch adds a saved search (Name trimmed, Type and Query as
// given; ID, times and Version are set here) in one transaction. It returns
// ErrSavedLimit at MaxSavedSearches and ErrNameTaken for a name already in use
// (ignoring case).
func (d *DB) CreateSavedSearch(ctx context.Context, s SavedSearch, now time.Time) (SavedSearch, error) {
	s.Name = strings.TrimSpace(s.Name)
	var b [4]byte
	_, _ = rand.Read(b[:])
	s.ID = "ss_" + hex.EncodeToString(b[:])
	s.Created, s.Updated, s.Version = now.UTC().Truncate(time.Millisecond), now.UTC().Truncate(time.Millisecond), 1
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return SavedSearch{}, err
	}
	defer tx.Rollback()
	var n int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM saved_searches`).Scan(&n); err != nil {
		return SavedSearch{}, err
	}
	if n >= MaxSavedSearches {
		return SavedSearch{}, ErrSavedLimit
	}
	if taken, err := nameTaken(ctx, tx, NameKey(s.Name), ""); err != nil {
		return SavedSearch{}, err
	} else if taken {
		return SavedSearch{}, ErrNameTaken
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO saved_searches (id, name, name_key, type, query, created, updated, version)
		VALUES (?, ?, ?, ?, ?, ?, ?, 1)`, s.ID, s.Name, NameKey(s.Name), s.Type, s.Query, ms(s.Created), ms(s.Updated)); err != nil {
		return SavedSearch{}, err
	}
	return s, tx.Commit()
}

// UpdateSavedSearch replaces a saved search's name, type and query. With
// ifVersion > 0 it saves only if the stored row is at that version
// (ErrSavedChanged otherwise). ErrNotFound for an unknown id, ErrNameTaken for
// a name another saved search has.
func (d *DB) UpdateSavedSearch(ctx context.Context, s SavedSearch, ifVersion int64, now time.Time) (SavedSearch, error) {
	s.Name = strings.TrimSpace(s.Name)
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return SavedSearch{}, err
	}
	defer tx.Rollback()
	cur, err := scanSaved(tx.QueryRowContext(ctx, `SELECT `+savedCols+` FROM saved_searches WHERE id = ?`, s.ID))
	if errors.Is(err, sql.ErrNoRows) {
		return SavedSearch{}, ErrNotFound
	}
	if err != nil {
		return SavedSearch{}, err
	}
	if ifVersion > 0 && cur.Version != ifVersion {
		return SavedSearch{}, ErrSavedChanged
	}
	if taken, err := nameTaken(ctx, tx, NameKey(s.Name), s.ID); err != nil {
		return SavedSearch{}, err
	} else if taken {
		return SavedSearch{}, ErrNameTaken
	}
	s.Created, s.Version = cur.Created, cur.Version+1
	s.Updated = now.UTC().Truncate(time.Millisecond)
	if _, err := tx.ExecContext(ctx, `UPDATE saved_searches SET name = ?, name_key = ?, type = ?, query = ?, updated = ?, version = ?
		WHERE id = ?`, s.Name, NameKey(s.Name), s.Type, s.Query, ms(s.Updated), s.Version, s.ID); err != nil {
		return SavedSearch{}, err
	}
	return s, tx.Commit()
}

// DeleteSavedSearch removes a saved search, or returns ErrNotFound.
func (d *DB) DeleteSavedSearch(ctx context.Context, id string) error {
	res, err := d.db.ExecContext(ctx, `DELETE FROM saved_searches WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}
