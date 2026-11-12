package index

import (
	"context"
	"time"
)

// RetentionInputs are what the retention estimate is computed from, read from
// the files table alone (one indexed query each, cheap at any device count).
type RetentionInputs struct {
	// Raw24h is the uncompressed bytes of stored lines received in the last 24 h,
	// apportioned by each file's received-time span (assumes an even rate within
	// a file).
	Raw24h float64
	// Earliest is the oldest received time on disk (zero if no files), exact when
	// it's within the last 24 h; otherwise some time before the window (enough to
	// say there's a full day of data). With less than 24 h of data the estimate
	// scales from the hours available.
	Earliest time.Time
	// GzRaw and GzSize are the uncompressed and on-disk bytes of files gzipped in
	// the last 7 days (by last received time), for the compression ratio.
	GzRaw, GzSize int64
}

// RetentionInputs reads the inputs of the retention estimate at now.
func (d *DB) RetentionInputs(ctx context.Context, now time.Time) (RetentionInputs, error) {
	var in RetentionInputs
	from, to := ms(now.Add(-24*time.Hour)), ms(now)
	// Each file's raw bytes times the share of its [first, last] span inside the
	// window; a file whose span is a single instant counts whole.
	err := d.db.QueryRowContext(ctx, `SELECT COALESCE(sum(CASE
			WHEN last_received <= first_received THEN raw_size
			ELSE raw_size * (min(last_received, ?2) - max(first_received, ?1)) * 1.0 / (last_received - first_received) END), 0)
		FROM files WHERE last_received >= ?1 AND first_received <= ?2`, from, to).Scan(&in.Raw24h)
	if err != nil {
		return in, err
	}
	// Both use files_time: a file that ended before the window means a full day
	// of data; otherwise the oldest start among the window's files.
	var earliest int64
	if err := d.db.QueryRowContext(ctx, `SELECT COALESCE(min(last_received), 0) FROM files`).Scan(&earliest); err != nil {
		return in, err
	}
	if earliest == 0 || earliest >= from {
		err := d.db.QueryRowContext(ctx, `SELECT COALESCE(min(first_received), 0) FROM files WHERE last_received >= ?`,
			from).Scan(&earliest)
		if err != nil {
			return in, err
		}
	}
	if earliest != 0 {
		in.Earliest = tm(earliest)
	}
	err = d.db.QueryRowContext(ctx, `SELECT COALESCE(sum(raw_size), 0), COALESCE(sum(size), 0) FROM files
		WHERE compressed = 1 AND last_received >= ?`, ms(now.Add(-7*24*time.Hour))).Scan(&in.GzRaw, &in.GzSize)
	return in, err
}

// LastEvent is the time of the latest event of kind at or after since (zero if
// none).
func (d *DB) LastEvent(ctx context.Context, kind Kind, since time.Time) (time.Time, error) {
	var t int64
	err := d.db.QueryRowContext(ctx, `SELECT COALESCE(max(time), 0) FROM events WHERE time >= ? AND kind = ?`,
		ms(since), string(kind)).Scan(&t)
	if err != nil || t == 0 {
		return time.Time{}, err
	}
	return tm(t), nil
}
