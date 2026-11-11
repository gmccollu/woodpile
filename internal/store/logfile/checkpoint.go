package logfile

import "time"

// Checkpoint rule. A checkpoint is taken at the first line of a file, then at the next
// line once CheckpointBytes have passed, or once CheckpointInterval has passed and at
// least CheckpointMinBytes have been written since the previous checkpoint. The minimum
// keeps quiet devices (one line a minute) from getting a checkpoint per line; a file
// smaller than CheckpointMinBytes is cheap to scan anyway.
const (
	CheckpointBytes    = 1 << 20
	CheckpointMinBytes = 64 << 10
	CheckpointInterval = time.Minute
)

// Checkpoint marks a line in a file's uncompressed byte stream.
//
// Received is the highest received time of any line up to and including the line at
// Offset, so every line before Offset was received at or before Received. To read from
// a time T, start at the last checkpoint whose Received is before T. (Received times
// within a file are nearly but not strictly ordered: concurrent receivers can
// interleave by a few milliseconds.)
type Checkpoint struct {
	Received time.Time
	Offset   int64 // byte offset of the line in the uncompressed stream
	Line     int64 // number of lines before Offset (the 0-based index of the line)
}

// Checkpointer applies the checkpoint rule to a file's lines, in order. The writer and
// the scanner both use it, so a rebuilt index has exactly the writer's checkpoints.
type Checkpointer struct {
	started bool
	last    Checkpoint
	hwm     time.Time
}

// ResumeCheckpointer returns the state after last, the newest checkpoint of a file,
// with hwm the highest received time seen so far. Observing lines from last.Offset on
// then continues the file's checkpoint sequence.
func ResumeCheckpointer(last Checkpoint, hwm time.Time) Checkpointer {
	if hwm.Before(last.Received) {
		hwm = last.Received
	}
	return Checkpointer{started: true, last: last, hwm: hwm}
}

// Observe records the line at byte offset off (line index line) received at t, and
// returns the checkpoint to store if one is due at this line.
func (c *Checkpointer) Observe(off, line int64, t time.Time) (Checkpoint, bool) {
	if !c.started || t.After(c.hwm) {
		c.hwm = t
	}
	if !c.started {
		c.started = true
		c.last = Checkpoint{Received: c.hwm, Offset: off, Line: line}
		return c.last, true
	}
	d := off - c.last.Offset
	if d >= CheckpointBytes || (d >= CheckpointMinBytes && c.hwm.Sub(c.last.Received) >= CheckpointInterval) {
		c.last = Checkpoint{Received: c.hwm, Offset: off, Line: line}
		return c.last, true
	}
	return Checkpoint{}, false
}

// HighWater returns the highest received time observed.
func (c *Checkpointer) HighWater() time.Time { return c.hwm }

// Last returns the newest checkpoint, and false if no line has been observed.
func (c *Checkpointer) Last() (Checkpoint, bool) { return c.last, c.started }
