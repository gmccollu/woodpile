package search

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"math"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/gmccollu/woodshed/internal/hub"
	"github.com/gmccollu/woodshed/internal/model"
	"github.com/gmccollu/woodshed/internal/store/logfile"
	"github.com/gmccollu/woodshed/internal/syslog"
)

// Summaries: the Events field panel and histogram (GET /api/search/summary,
// SPEC §10.1.1, §10.1.2 and §10.1.5).
//
// One scan reads newest first and counts every matching line; it never
// samples. It reads like a desc search, but instead of merging lines into
// pages it moves every line it finds into a holdback, and commits a held line
// to the counts once no unread line can be newer: when its received time is
// above every stream's bound. So each response covers [counted_from, to)
// exactly, with counted_from one past the highest bound still unread, even
// when lines within a file are slightly out of order. The running counts, the
// holdback and the scan's position (a search cursor) stay on the server; the
// client's cursor only names them.
//
// The histogram of an unfiltered query is read from the stats and checkpoints
// instead (histindex.go), without opening a log file.

// Summary limits and defaults (SPEC §10.1.5).
const (
	DefaultSummaryBudget = 2 * time.Second
	MaxSummaryBudget     = 10 * time.Second
	DefaultTop           = 10
	MaxTop               = 50
	MaxDistinct          = 10000 // distinct values tracked per field
	MaxBuckets           = 1000
	SummaryTTL           = 10 * time.Minute
	MaxSummaries         = 8 // running summaries kept
	MaxSummaryScans      = 2 // summary scans running at once
)

// BucketSizes are the histogram bucket sizes a client may ask for.
var BucketSizes = []time.Duration{time.Second, 5 * time.Second, 15 * time.Second, time.Minute, 5 * time.Minute,
	15 * time.Minute, time.Hour, 2 * time.Hour, 6 * time.Hour, 24 * time.Hour, 7 * 24 * time.Hour}

// DefaultBucket is the bucket size for a range (SPEC §10.1.2): at most 120
// buckets, then 7 days.
func DefaultBucket(rng time.Duration) time.Duration {
	limits := []time.Duration{2 * time.Minute, 10 * time.Minute, 30 * time.Minute, 2 * time.Hour, 10 * time.Hour,
		30 * time.Hour, 5 * 24 * time.Hour, 10 * 24 * time.Hour, 30 * 24 * time.Hour, 120 * 24 * time.Hour}
	for i, l := range limits {
		if rng <= l {
			return BucketSizes[i]
		}
	}
	return BucketSizes[len(BucketSizes)-1]
}

// weekOrigin puts 7-day buckets on Mondays: 1970-01-05 was a Monday.
const weekOrigin = 4 * 24 * 3600 * 1000

// bucketStart is the start (ms) of the bucket of size ms holding t.
func bucketStart(t, size int64) int64 {
	off := int64(0)
	if size == 7*24*3600*1000 {
		off = weekOrigin
	}
	return floorDiv(t-off, size)*size + off
}

func floorDiv(a, b int64) int64 {
	q := a / b
	if a%b != 0 && (a < 0) != (b < 0) {
		q--
	}
	return q
}

// SummaryQuery is one /api/search/summary request.
type SummaryQuery struct {
	Filter
	Devices        []string
	Group          string
	Ungrouped      bool
	ExcludeDevices []string
	// Fields are the field keys to count, in any order; nil means every field,
	// and an empty non-nil slice none (fields=none).
	Fields      []string
	Top         int  // values per field (default 10, at most 50)
	NoHistogram bool // histogram=false
	Bucket      time.Duration
	Budget      time.Duration // default 2 s, at most 10 s
	Cursor      string

	forceScan bool // tests: never use the index histogram
}

// Bucket is one histogram bucket. Count is nil while it hasn't been counted.
type Bucket struct {
	Start  time.Time
	Count  *int64
	Approx bool
}

// Histogram is a summary's histogram.
type Histogram struct {
	BucketSeconds int64
	Source        string // "index" or "scan"
	Buckets       []Bucket
}

// FieldValue is one value of a field.
type FieldValue struct {
	Value, Label string
	Count        int64
	Filterable   bool
}

// FieldCounts is one field's counts.
type FieldCounts struct {
	Key, Label     string
	Values         []FieldValue
	Other, None    int64
	Distinct       int
	DistinctCapped bool
}

// Summary is one /api/search/summary response.
type Summary struct {
	From, To     time.Time
	Matched      int64
	CountedFrom  time.Time
	Histogram    *Histogram // nil: not asked for
	Fields       []FieldCounts
	NextCursor   string
	Partial      bool
	ScannedBytes int64
	ScannedFiles int
	Elapsed      time.Duration
}

// ErrSummaryExpired is the error for a cursor whose running counts are gone.
var ErrSummaryExpired = &QueryError{Msg: "This summary expired; start it again."}

// ---- running state ----

// slot is one tracked value of a field.
type slot struct {
	value string // the first spelling seen
	n     int64
}

// tally counts one free-text field (host, app). Values are grouped by a key
// folded the way the field's filter matches (case, and a leading '%' for
// app), so a value's count is what filtering on it returns.
type tally struct {
	byKey    map[string]int32
	slots    []slot
	overflow int64 // lines whose value wasn't tracked (past MaxDistinct)
	none     int64 // lines without a value
	capped   bool
}

const (
	idNone     = -1 // the line has no value
	idOverflow = -2 // a value past MaxDistinct
)

func newTally() *tally { return &tally{byKey: map[string]int32{}} }

// id returns the slot of v, adding it if there's room.
func (t *tally) id(v string, app bool) int32 {
	if v == "" {
		return idNone
	}
	k := foldKey(v, app)
	if i, ok := t.byKey[k]; ok {
		return i
	}
	if len(t.slots) >= MaxDistinct {
		t.capped = true
		return idOverflow
	}
	i := int32(len(t.slots))
	t.byKey[strings.Clone(k)] = i
	t.slots = append(t.slots, slot{value: strings.Clone(v)})
	return i
}

func (t *tally) add(id int32) {
	switch {
	case id >= 0:
		t.slots[id].n++
	case id == idOverflow:
		t.overflow++
	default:
		t.none++
	}
}

// foldKey is the grouping key of a host (case folded) or app (case folded,
// without a leading '%') value. It doesn't allocate for lower-case ASCII.
func foldKey(v string, app bool) string {
	if app {
		v = trimPct(v)
	}
	for i := 0; i < len(v); i++ {
		c := v[i]
		if c >= utf8.RuneSelf || ('A' <= c && c <= 'Z') {
			return strings.ToLower(v)
		}
	}
	return v
}

// held is a line found but not yet committed to the counts.
type held struct {
	ms        int64
	dev       int32
	pri       int16 // -1: no PRI
	host, app int32
}

type sumState struct {
	id   [16]byte
	seq  uint64
	bind []byte

	fromMs, toMs     int64
	bucketMs, origin int64
	nb               int

	devs   []string
	labels []string
	sevs   hub.SeveritySet
	facs   FacilitySet

	wantFields []string // keys in schema order
	wantHist   bool
	scanHist   bool // the histogram comes from the scan
	idxHist    []Bucket

	started    bool
	done       bool
	scanCursor string // the scan's position (a search cursor)
	committed  int64  // lines committed
	countedMs  int64  // every line in [countedMs, toMs) is committed

	devN       []int64
	sevN       [9]int64 // 0-7, unknown
	facN       [25]int64
	host, app  *tally
	hist       []int64
	hold       []held
	holdFlushN int

	used time.Time // for SummaryTTL
	lru  uint64    // put order, for MaxSummaries
}

// summaries keeps the running summaries, least recently used out first.
type summaries struct {
	mu   sync.Mutex
	byID map[[16]byte]*sumState
	tick uint64
	sem  chan struct{}
}

func newSummaries() *summaries {
	return &summaries{byID: map[[16]byte]*sumState{}, sem: make(chan struct{}, MaxSummaryScans)}
}

// take removes and returns the state for id at seq, or nil.
func (c *summaries) take(id [16]byte, seq uint64, now time.Time) *sumState {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.expireLocked(now)
	st := c.byID[id]
	if st == nil || st.seq != seq {
		return nil
	}
	delete(c.byID, id)
	return st
}

func (c *summaries) put(st *sumState, now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	st.used = now
	c.tick++
	st.lru = c.tick
	c.byID[st.id] = st
	c.expireLocked(now)
	for len(c.byID) > MaxSummaries {
		var old *sumState
		for _, s := range c.byID {
			if old == nil || s.lru < old.lru {
				old = s
			}
		}
		delete(c.byID, old.id)
	}
}

func (c *summaries) expireLocked(now time.Time) {
	for id, s := range c.byID {
		if now.Sub(s.used) > SummaryTTL {
			delete(c.byID, id)
		}
	}
}

// ---- cursor ----

const summaryCursorVersion = 1

func (q *SummaryQuery) binding() []byte {
	sq := Query{Filter: q.Filter, Devices: q.Devices, Group: q.Group, Ungrouped: q.Ungrouped, ExcludeDevices: q.ExcludeDevices}
	h := sha256.New()
	h.Write([]byte("woodshed-summary-v1"))
	h.Write(sq.binding())
	var b []byte
	if q.Fields == nil {
		b = append(b, 0xff)
	} else {
		fs := sortedUnique(q.Fields)
		b = binary.AppendUvarint(b, uint64(len(fs)))
		for _, f := range fs {
			b = binary.AppendUvarint(b, uint64(len(f)))
			b = append(b, f...)
		}
	}
	b = binary.AppendVarint(b, int64(q.Bucket))
	if q.NoHistogram {
		b = append(b, 1)
	} else {
		b = append(b, 0)
	}
	h.Write(b)
	return h.Sum(nil)[:bindLen]
}

func (e *Engine) encodeSummaryCursor(st *sumState) string {
	b := []byte{summaryCursorVersion}
	b = append(b, st.bind...)
	b = append(b, st.id[:]...)
	b = binary.AppendUvarint(b, st.seq)
	mac := hmac.New(sha256.New, e.key)
	mac.Write(b)
	return base64.RawURLEncoding.EncodeToString(mac.Sum(b)[:len(b)+macLen])
}

func (e *Engine) decodeSummaryCursor(s string, bind []byte) (id [16]byte, seq uint64, err error) {
	raw, err := base64.RawURLEncoding.Strict().DecodeString(s)
	if err != nil || len(raw) < 1+bindLen+16+1+macLen {
		return id, 0, errCursorInvalid
	}
	body, sum := raw[:len(raw)-macLen], raw[len(raw)-macLen:]
	mac := hmac.New(sha256.New, e.key)
	mac.Write(body)
	if !hmac.Equal(mac.Sum(nil)[:macLen], sum) || body[0] != summaryCursorVersion {
		return id, 0, errCursorInvalid
	}
	if !bytes.Equal(body[1:1+bindLen], bind) {
		return id, 0, errCursorOther
	}
	copy(id[:], body[1+bindLen:])
	seq, n := binary.Uvarint(body[1+bindLen+16:])
	if n <= 0 || 1+bindLen+16+n != len(body) {
		return id, 0, errCursorInvalid
	}
	return id, seq, nil
}

// ---- the request ----

// knownField reports whether k is a field key of the syslog type.
func knownField(k string) bool {
	for _, f := range syslogFields {
		if f.Key == k {
			return true
		}
	}
	return false
}

// CheckBucket validates a bucket size for a range: one of BucketSizes, giving
// at most MaxBuckets buckets.
func CheckBucket(size time.Duration, fromMs, toMs int64) error {
	if !slices.Contains(BucketSizes, size) {
		return &QueryError{Msg: "bucket must be one of 1, 5, 15, 60, 300, 900, 3600, 7200, 21600, 86400 and 604800 (seconds)."}
	}
	ms := size.Milliseconds()
	n := (bucketStart(toMs-1, ms)-bucketStart(fromMs, ms))/ms + 1
	if n > MaxBuckets {
		return &QueryError{Msg: fmt.Sprintf("bucket=%d gives %d buckets over this range; the limit is %d.", int64(size/time.Second), n, MaxBuckets)}
	}
	return nil
}

// histogramUnfiltered reports whether f filters only by devices and time, so
// the histogram can come from the index.
func histogramUnfiltered(f Filter) bool {
	return f.Q == "" && f.Exclude == "" && len(f.Apps) == 0 && len(f.Hosts) == 0 && len(f.ExcludeApps) == 0 &&
		len(f.ExcludeHosts) == 0 && f.Severities == 0 && f.Facilities == 0
}

// Summary runs one request of a summary.
func (e *Engine) Summary(ctx context.Context, q SummaryQuery) (Summary, error) {
	start := time.Now()
	budget := q.Budget
	if budget <= 0 {
		budget = DefaultSummaryBudget
	}
	budget = min(budget, MaxSummaryBudget)
	deadline := start.Add(budget)
	c, err := e.compile(q.Filter)
	if err != nil {
		return Summary{}, err
	}
	for _, f := range q.Fields {
		if !knownField(f) {
			return Summary{}, &QueryError{Msg: fmt.Sprintf("fields: unknown field %q.", f)}
		}
	}
	top := q.Top
	if top <= 0 {
		top = DefaultTop
	}
	top = min(top, MaxTop)
	bind := q.binding()

	var st *sumState
	if q.Cursor != "" {
		id, seq, err := e.decodeSummaryCursor(q.Cursor, bind)
		if err != nil {
			return Summary{}, err
		}
		if st = e.sums.take(id, seq, e.now()); st == nil {
			return Summary{}, ErrSummaryExpired
		}
	} else {
		if st, err = e.newSummary(ctx, q, bind); err != nil {
			return Summary{}, err
		}
	}

	var scanned int64
	var files int
	if !st.done {
		// At most MaxSummaryScans scans at once; the others wait within their own
		// budget and return what they have.
		t := time.NewTimer(time.Until(deadline))
		select {
		case e.sums.sem <- struct{}{}:
			t.Stop()
			scanned, files, err = e.summaryScan(ctx, st, c, deadline)
			<-e.sums.sem
			if err != nil {
				return Summary{}, err // the state is dropped: a cancelled scan can't resume
			}
		case <-t.C:
		case <-ctx.Done():
			t.Stop()
			return Summary{}, ctx.Err()
		}
	}
	out := e.summaryOut(st, top)
	out.ScannedBytes, out.ScannedFiles, out.Elapsed = scanned, files, time.Since(start)
	if !st.done {
		st.seq++
		e.sums.put(st, e.now())
		out.NextCursor = e.encodeSummaryCursor(st)
		out.Partial = true
	}
	return out, nil
}

func (e *Engine) newSummary(ctx context.Context, q SummaryQuery, bind []byte) (*sumState, error) {
	st := &sumState{bind: bind, wantHist: !q.NoHistogram, sevs: q.Severities, facs: q.Facilities}
	_, _ = rand.Read(st.id[:])
	var err error
	if st.fromMs, st.toMs, err = e.resolveRange(q.Filter); err != nil {
		return nil, err
	}
	size := q.Bucket
	if size == 0 {
		size = DefaultBucket(time.Duration(st.toMs-st.fromMs) * time.Millisecond)
	} else if err := CheckBucket(size, st.fromMs, st.toMs); err != nil {
		return nil, err
	}
	st.bucketMs = size.Milliseconds()
	st.origin = bucketStart(st.fromMs, st.bucketMs)
	st.nb = int((bucketStart(st.toMs-1, st.bucketMs)-st.origin)/st.bucketMs) + 1
	for _, f := range syslogFields {
		if q.Fields == nil || slices.Contains(q.Fields, f.Key) {
			st.wantFields = append(st.wantFields, f.Key)
		}
	}
	all, err := e.idx.ListDevices(ctx)
	if err != nil {
		return nil, err
	}
	none := q.Severities == hub.SeverityNone || q.Facilities == FacilityNone
	if !none {
		st.devs = selectDevices(all, q.Devices, q.Group, q.Ungrouped, q.ExcludeDevices)
	}
	label := map[string]string{}
	for _, d := range all {
		l := d.Name
		if l == "" {
			l = d.IP
		}
		if l == "" {
			l = d.ID
		}
		label[d.ID] = l
	}
	for _, d := range st.devs {
		st.labels = append(st.labels, label[d])
	}
	st.devN = make([]int64, len(st.devs))
	st.host, st.app = newTally(), newTally()
	st.countedMs = st.toMs
	if st.wantHist {
		if !q.forceScan && st.bucketMs >= time.Minute.Milliseconds() && histogramUnfiltered(q.Filter) && !none {
			if st.idxHist, err = e.indexHistogram(ctx, st); err != nil {
				return nil, err
			}
		} else {
			st.scanHist = true
			st.hist = make([]int64, st.nb)
		}
	}
	// No scan is needed for the index histogram alone.
	st.done = len(st.devs) == 0 || (len(st.wantFields) == 0 && st.wantHist && !st.scanHist)
	if st.done {
		st.countedMs = st.fromMs
	}
	return st, nil
}

// summaryScan advances st's scan until the deadline or the end.
func (e *Engine) summaryScan(ctx context.Context, st *sumState, c compiled, deadline time.Time) (int64, int, error) {
	r := &run{e: e, ctx: ctx, deadline: deadline, maxSteps: e.testMaxSteps, now: e.now(), desc: true, m: c.m, ex: c.ex,
		fields: c.fields, sevs: st.sevs, facs: st.facs, need: 1 << 30, files: map[int64]struct{}{}, ringBytes: math.MaxInt}
	r.fromMs, r.toMs = st.fromMs, st.toMs
	devIdx := make(map[string]int32, len(st.devs))
	for i, d := range st.devs {
		devIdx[d] = int32(i)
	}
	var streams []*stream
	if st.started {
		cur, err := e.decodeCursor(st.scanCursor, st.bind)
		if err != nil {
			return 0, 0, err
		}
		devs := make([]string, len(cur.streams))
		for i, cs := range cur.streams {
			devs[i] = cs.dev
		}
		files, err := e.filesByDevice(ctx, devs, r.fromMs, r.toMs)
		if err != nil {
			return 0, 0, err
		}
		for _, cs := range cur.streams {
			streams = append(streams, cs.resume(files[cs.dev], true))
		}
	} else {
		files, err := e.filesByDevice(ctx, st.devs, r.fromMs, r.toMs)
		if err != nil {
			return 0, 0, err
		}
		for _, d := range st.devs {
			s := newStream(d, files[d])
			s.fi = len(s.files) - 1
			streams = append(streams, s)
		}
		st.started = true
	}
	r.streams = streams
	defer func() {
		for _, s := range streams {
			s.close()
		}
	}()
	wantHost := slices.Contains(st.wantFields, FieldHost)
	wantApp := slices.Contains(st.wantFields, FieldApp)
	var msg model.Message
	var unesc []byte
	cur := int32(0) // the stream whose lines the sink receives
	r.sink = func(h hit) {
		raw := h.esc
		if bytes.IndexByte(raw, '\\') >= 0 {
			unesc = logfile.Unescape(unesc[:0], raw)
			raw = unesc
		}
		x := held{ms: h.ms, dev: cur, pri: int16(PRI(raw)), host: idNone, app: idNone}
		if wantHost || wantApp {
			syslog.Parse(raw, &msg)
			if wantHost {
				x.host = st.host.id(msg.Hostname, false)
			}
			if wantApp {
				x.app = st.app.id(msg.App, true)
			}
		}
		st.hold = append(st.hold, x)
	}
	if st.holdFlushN == 0 {
		st.holdFlushN = 4096
	}
	for {
		// Read the stream with the newest unread lines next, so the counted span
		// grows from to downwards.
		best, bestKey := -1, int64(0)
		for i, s := range streams {
			if s.done {
				continue
			}
			k, _ := s.key(r)
			if best < 0 || k > bestKey || (k == bestKey && s.dev < streams[best].dev) {
				best, bestKey = i, k
			}
		}
		if best < 0 {
			break
		}
		if r.steps > 0 && r.expired() {
			break
		}
		r.steps++
		s := streams[best]
		cur = devIdx[s.dev]
		s.step(r)
		if r.err != nil {
			return 0, 0, r.err
		}
		for s.head() != nil {
			r.sink(s.pop())
		}
		if len(st.hold) >= st.holdFlushN {
			st.flush(r, streams)
			st.holdFlushN = max(4096, 2*len(st.hold))
		}
	}
	st.flush(r, streams)
	st.scanCursor = e.encodeCursor(st.bind, r, streams)
	if st.scanCursor == "" {
		st.done = true
		st.flush(r, nil) // nothing is unread: commit the rest
		st.countedMs = st.fromMs
	}
	return r.scanned, len(r.files), nil
}

// flush commits the held lines that are newer than every unread line.
func (st *sumState) flush(r *run, streams []*stream) {
	bound := int64(math.MinInt64)
	for _, s := range streams {
		if s.done {
			continue
		}
		if k, _ := s.key(r); k > bound {
			bound = k
		}
	}
	keep := st.hold[:0]
	for _, h := range st.hold {
		if h.ms > bound {
			st.commit(h)
		} else {
			keep = append(keep, h)
		}
	}
	clear(st.hold[len(keep):])
	st.hold = keep
	if bound == math.MinInt64 {
		st.countedMs = st.fromMs
	} else {
		st.countedMs = min(max(bound+1, st.fromMs), st.countedMs)
	}
}

func (st *sumState) commit(h held) {
	st.committed++
	st.devN[h.dev]++
	sev, fac := 8, 24
	if h.pri >= 0 {
		sev, fac = int(h.pri&7), int(h.pri>>3)
	}
	st.sevN[sev]++
	st.facN[fac]++
	if slices.Contains(st.wantFields, FieldHost) {
		st.host.add(h.host)
	}
	if slices.Contains(st.wantFields, FieldApp) {
		st.app.add(h.app)
	}
	if st.scanHist {
		st.hist[(h.ms-st.origin)/st.bucketMs]++
	}
}

// ---- the response ----

func (e *Engine) summaryOut(st *sumState, top int) Summary {
	out := Summary{From: time.UnixMilli(st.fromMs).UTC(), To: time.UnixMilli(st.toMs).UTC(), Matched: st.committed,
		CountedFrom: time.UnixMilli(st.countedMs).UTC()}
	if st.wantHist {
		h := &Histogram{BucketSeconds: st.bucketMs / 1000, Source: "scan"}
		if st.idxHist != nil {
			h.Source = "index"
			h.Buckets = st.idxHist
			if len(st.wantFields) == 0 {
				out.Matched = 0
				for _, b := range st.idxHist {
					out.Matched += *b.Count
				}
			}
		} else {
			h.Buckets = make([]Bucket, st.nb)
			for i := range h.Buckets {
				s := st.origin + int64(i)*st.bucketMs
				h.Buckets[i].Start = time.UnixMilli(s).UTC()
				if !st.done && s+st.bucketMs <= st.countedMs {
					continue // wholly before counted_from: not counted yet
				}
				n := st.hist[i]
				h.Buckets[i].Count = &n
			}
		}
		out.Histogram = h
	}
	for _, k := range st.wantFields {
		var fs FieldSchema
		for _, f := range syslogFields {
			if f.Key == k {
				fs = f
			}
		}
		fc := FieldCounts{Key: k, Label: fs.Label, Values: []FieldValue{}}
		var vals []FieldValue
		switch k {
		case FieldDevice:
			for i, n := range st.devN {
				if n > 0 {
					vals = append(vals, FieldValue{Value: st.devs[i], Label: st.labels[i], Count: n, Filterable: true})
				}
			}
		case FieldSeverity, FieldFacility:
			counts := st.sevN[:]
			if k == FieldFacility {
				counts = st.facN[:]
			}
			for i, n := range counts {
				if n > 0 {
					d := fs.Domain[i]
					vals = append(vals, FieldValue{Value: d.Value, Label: d.Label, Count: n, Filterable: true})
				}
			}
		default:
			t := st.host
			if k == FieldApp {
				t = st.app
			}
			for _, s := range t.slots {
				if s.n > 0 {
					vals = append(vals, FieldValue{Value: s.value, Label: s.value, Count: s.n, Filterable: filterable(s.value, k == FieldApp)})
				}
			}
			fc.None, fc.DistinctCapped = t.none, t.capped
			fc.Other += t.overflow
		}
		slices.SortFunc(vals, func(a, b FieldValue) int {
			if a.Count != b.Count {
				if a.Count > b.Count {
					return -1
				}
				return 1
			}
			return strings.Compare(a.Value, b.Value)
		})
		fc.Distinct = len(vals)
		if fs.Domain == nil && len(vals) > top {
			for _, v := range vals[top:] {
				fc.Other += v.Count
			}
			vals = vals[:top]
		}
		fc.Values = append(fc.Values, vals...)
		out.Fields = append(out.Fields, fc)
	}
	return out
}

// filterable reports whether a host or app value can go into its filter
// parameter as is: not with a comma (the list separator), surrounding blanks,
// or (for app) a trailing '*' (a prefix pattern).
func filterable(v string, app bool) bool {
	if strings.Contains(v, ",") || strings.TrimSpace(v) != v || v == "" {
		return false
	}
	return !app || !strings.HasSuffix(v, "*")
}
