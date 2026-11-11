package index

import (
	"time"

	"woodpile/internal/store/logfile"
)

// Checkpoint is a point in a file's uncompressed stream; see logfile.Checkpoint for
// the exact meaning of Received.
type Checkpoint = logfile.Checkpoint

// File is one log file.
type File struct {
	ID       int64 // stable across a part rename and compression
	DeviceID string
	Path     string // relative to the data dir, '/'-separated
	Day      string // YYYY-MM-DD (UTC) from the file name
	Part     int    // 0 for "<day>.log" (the day's newest part), else the part number
	// FirstReceived and LastReceived are the lowest and highest received time of any
	// line in the file. Order a device's files by FirstReceived.
	FirstReceived time.Time
	LastReceived  time.Time
	Size          int64 // bytes on disk (compressed size for .gz)
	RawSize       int64 // bytes of the uncompressed stream
	Lines         int64
	Compressed    bool
}

// FileSpan is a file overlapping a queried time range, with the checkpoint to start
// reading at for the range's start.
type FileSpan struct {
	File
	Start Checkpoint
}

// Device is a sender.
type Device struct {
	ID          string // model.DeviceID of the IP; also the logs/ directory name
	IP          string
	Name        string
	Group       string
	Hidden      bool
	FirstSeen   time.Time
	LastHeard   time.Time
	TotalMsgs   int64 // messages stored, all time
	TotalBytes  int64 // raw message bytes stored, all time
	BytesOnDisk int64 // sum of the device's file sizes now
}

// DeviceUpdate changes a device's user-set fields; nil fields are left alone.
type DeviceUpdate struct {
	Name   *string
	Group  *string
	Hidden *bool
}

// Counts are message counters for a device or a time range.
type Counts struct {
	Msgs      int64 // messages stored
	Bytes     int64 // raw bytes stored
	Truncated int64 // messages cut at the max message size (still stored)
	Dropped   int64 // messages Woodpile received but could not store
}

// Add adds o to c.
func (c *Counts) Add(o Counts) {
	c.Msgs += o.Msgs
	c.Bytes += o.Bytes
	c.Truncated += o.Truncated
	c.Dropped += o.Dropped
}

// MinuteCounts are the counts for the minute starting at Minute.
type MinuteCounts struct {
	Minute time.Time
	Counts
}

// HourCounts are the counts for the hour starting at Hour.
type HourCounts struct {
	Hour time.Time
	Counts
}

// Activity is a counter delta for one device and one minute, as the store reports it.
type Activity struct {
	DeviceID   string
	IP         string
	Minute     time.Time // start of the minute (truncated if not)
	FirstHeard time.Time // earliest message in the delta (for first_seen)
	LastHeard  time.Time // latest message in the delta (dropped ones count as heard)
	Counts
}

// Level is an event's level.
type Level string

// Event levels.
const (
	LevelInfo  Level = "info"
	LevelWarn  Level = "warn"
	LevelError Level = "error"
)

// Kind classifies an event.
type Kind string

// Event kinds. The store records rotate, compress (failures), delete, drop, pause,
// resume and recover; the service and listeners record start, stop and bind.
const (
	KindStart    Kind = "start"
	KindStop     Kind = "stop"
	KindBind     Kind = "bind"
	KindRotate   Kind = "rotate"
	KindCompress Kind = "compress"
	KindDelete   Kind = "delete"
	KindDrop     Kind = "drop"
	KindPause    Kind = "pause"
	KindResume   Kind = "resume"
	KindRecover  Kind = "recover"
	KindError    Kind = "error"
)

// Event is one of the service's own events.
type Event struct {
	ID       int64
	Time     time.Time
	Level    Level
	Kind     Kind
	DeviceID string // "" when not about one device
	Text     string
}

// Reason is why a file was deleted.
type Reason string

// Deletion reasons.
const (
	ReasonAge     Reason = "age"
	ReasonCap     Reason = "cap"
	ReasonLowDisk Reason = "low_disk"
)

// Deletion is one entry of the storage history.
type Deletion struct {
	ID       int64
	Time     time.Time
	File     string // path relative to the data dir
	DeviceID string
	Bytes    int64 // bytes freed on disk
	Reason   Reason
}

// FileOpKind is the kind of a FileOp.
type FileOpKind int

// File operations.
const (
	// OpUpsert creates or updates the row for File.Path with File's fields (ID is
	// ignored) and adds Checkpoints (existing offsets are kept).
	OpUpsert FileOpKind = iota + 1
	// OpRename moves the row at OldPath to File.Path and sets File's fields, keeping
	// the row's ID and checkpoints (a part rename, or compression).
	OpRename
	// OpDelete removes the row for File.Path. With Deletion set, it also records
	// the deletion in the storage history and as an event.
	OpDelete
)

// FileOp is one change to the files table.
type FileOp struct {
	Kind        FileOpKind
	File        File
	OldPath     string
	Checkpoints []Checkpoint
	// ResetCheckpoints (OpUpsert) replaces the file's checkpoints with Checkpoints
	// instead of adding to them, after the store re-read a file.
	ResetCheckpoints bool
	Deletion         *Deletion
}

// Batch is a set of changes applied in one transaction, file ops in order.
type Batch struct {
	Files    []FileOp
	Activity []Activity
	Events   []Event
}

// Empty reports whether the batch has nothing to apply.
func (b *Batch) Empty() bool { return len(b.Files) == 0 && len(b.Activity) == 0 && len(b.Events) == 0 }
