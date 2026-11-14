package index

import (
	"context"
	"encoding/json"
)

// Reads for the Events histogram's index source (SPEC §10.1.2): per-device
// stats summed into time buckets, each device's oldest remaining file, and the
// checkpoints of many files at once.

// OldestFiles returns, for each of deviceIDs that has files, the lowest
// FirstReceived (Unix ms) of any of its files.
func (d *DB) OldestFiles(ctx context.Context, deviceIDs []string) (map[string]int64, error) {
	out := map[string]int64{}
	if len(deviceIDs) == 0 {
		return out, nil
	}
	in, args := inList(deviceIDs)
	rows, err := d.db.QueryContext(ctx, `SELECT device_id, min(first_received) FROM files WHERE device_id IN `+in+
		` GROUP BY device_id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var first int64
		if err := rows.Scan(&id, &first); err != nil {
			return nil, err
		}
		out[id] = first
	}
	return out, rows.Err()
}

// StatsBuckets sums the stored-message counts (msgs) of stats rows into
// buckets of size ms starting at origin: a row starting at s goes into bucket
// (s-origin)/size. Only rows of the devices in cutoffs count, and only those
// starting in [fromMs, toMs) and at or after the device's cutoff (Unix ms).
// With minuteOnly, only the per-minute table is read; otherwise both the
// minute and hourly tables (a message lives in exactly one row of the two).
func (d *DB) StatsBuckets(ctx context.Context, cutoffs map[string]int64, minuteOnly bool, fromMs, toMs, origin, size int64) (map[int64]int64, error) {
	out := map[int64]int64{}
	if len(cutoffs) == 0 || fromMs >= toMs || size <= 0 {
		return out, nil
	}
	b, err := json.Marshal(cutoffs)
	if err != nil {
		return nil, err
	}
	q := `SELECT (s.minute - ?) / ?, sum(s.msgs) FROM json_each(?) j JOIN stats s ON s.device_id = j.key
		WHERE s.minute >= ? AND s.minute < ? AND s.minute >= j.value GROUP BY 1`
	args := []any{origin, size, string(b), fromMs, toMs}
	if !minuteOnly {
		q = `SELECT (start - ?) / ?, sum(msgs) FROM (
			SELECT s.minute AS start, s.msgs AS msgs FROM json_each(?) j JOIN stats s ON s.device_id = j.key
				WHERE s.minute >= ? AND s.minute < ? AND s.minute >= j.value
			UNION ALL
			SELECT h.hour AS start, h.msgs AS msgs FROM json_each(?) j JOIN stats_hourly h ON h.device_id = j.key
				WHERE h.hour >= ? AND h.hour < ? AND h.hour >= j.value) GROUP BY 1`
		args = []any{origin, size, string(b), fromMs, toMs, string(b), fromMs, toMs}
	}
	rows, err := d.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var i, n int64
		if err := rows.Scan(&i, &n); err != nil {
			return nil, err
		}
		out[i] += n
	}
	return out, rows.Err()
}

// CheckpointsFor returns the checkpoints of each of fileIDs, in offset order.
// Files without checkpoints are absent.
func (d *DB) CheckpointsFor(ctx context.Context, fileIDs []int64) (map[int64][]Checkpoint, error) {
	out := map[int64][]Checkpoint{}
	if len(fileIDs) == 0 {
		return out, nil
	}
	b, err := json.Marshal(fileIDs)
	if err != nil {
		return nil, err
	}
	rows, err := d.db.QueryContext(ctx, `SELECT c.file_id, c.offset, c.line, c.received FROM json_each(?) j
		JOIN checkpoints c ON c.file_id = j.value ORDER BY c.file_id, c.offset`, string(b))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id, r int64
		var c Checkpoint
		if err := rows.Scan(&id, &c.Offset, &c.Line, &r); err != nil {
			return nil, err
		}
		c.Received = tm(r)
		out[id] = append(out[id], c)
	}
	return out, rows.Err()
}
