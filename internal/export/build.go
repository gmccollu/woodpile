package export

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"hash"
	"os"
	"strings"
	"time"

	"github.com/gmccollu/woodshed/internal/hub"
	"github.com/gmccollu/woodshed/internal/index"
	"github.com/gmccollu/woodshed/internal/store/logfile"
)

type buildResult struct {
	lines, size int64
	files       int
}

// Manifest is manifest.json.
type Manifest struct {
	Format          string         `json:"format"` // "woodshed-export/1"
	WoodshedVersion string         `json:"woodshed_version"`
	ExportedAt      time.Time      `json:"exported_at"`
	ID              string         `json:"id"`
	Request         ManifestReq    `json:"request"`
	Files           []ManifestFile `json:"files"`
	Lines           int64          `json:"lines"`
	Gaps            []Gap          `json:"gaps"`
	GapsNote        string         `json:"gaps_note"`
	EventsFile      string         `json:"events_file"`
	Events          int            `json:"events"`
}

// ManifestReq echoes the request.
type ManifestReq struct {
	From    time.Time        `json:"from"`
	To      time.Time        `json:"to"`
	Devices []ManifestDevice `json:"devices"`
	Filters ManifestFilters  `json:"filters"`
}

// ManifestDevice is one exported device.
type ManifestDevice struct {
	ID    string `json:"id"`
	IP    string `json:"ip"`
	Name  string `json:"name"`
	Group string `json:"group"`
}

// ManifestFilters are the request's filters in API form.
type ManifestFilters struct {
	Devices    []string `json:"devices"` // as named in the request
	Group      string   `json:"group"`
	Ungrouped  bool     `json:"ungrouped"`
	Severities []any    `json:"severities"` // [] = all; ["none"] = nothing
	Q          string   `json:"q"`
	Regex      bool     `json:"regex"`
	Case       bool     `json:"case"`
}

// ManifestFile is one log file in the zip.
type ManifestFile struct {
	Path     string    `json:"path"`
	DeviceID string    `json:"device_id"`
	Day      string    `json:"day"`
	Lines    int64     `json:"lines"` // messages
	Bytes    int64     `json:"bytes"`
	SHA256   string    `json:"sha256"`
	First    time.Time `json:"first"`
	Last     time.Time `json:"last"`
}

// SeverityList renders a severity set as the API's list: [] for all, ["none"],
// or the severities with "unknown".
func SeverityList(s hub.SeveritySet) []any {
	out := []any{}
	if s == hub.SeverityNone {
		return append(out, "none")
	}
	if s == 0 {
		return out
	}
	for i := 0; i < 8; i++ {
		if s.Has(i) {
			out = append(out, i)
		}
	}
	if s.Has(-1) {
		out = append(out, "unknown")
	}
	return out
}

// fileOut is the log file being written, with its checksum and counts.
type fileOut struct {
	mf  ManifestFile
	sum hash.Hash
}

func (m *Manager) build(ctx context.Context, e *entry) (buildResult, error) {
	m.mu.Lock()
	info := e.info
	m.mu.Unlock()
	req := Request{Devices: info.Named, From: info.From, To: info.To, Filters: info.Filters}
	flt := req.filter()

	est, err := m.eng.Estimate(ctx, info.Devices, flt)
	if err != nil {
		return buildResult{}, err
	}
	hint := map[[2]string]int64{}
	for _, p := range est.Parts {
		hint[[2]string{p.Device, p.Day}] = p.Bytes
	}
	total := max(est.Bytes, 1)

	tmp := m.tmpPath(info.ID)
	f, err := os.OpenFile(tmp, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0o640)
	if err != nil {
		return buildResult{}, err
	}
	ok := false
	defer func() {
		f.Close()
		if !ok {
			os.Remove(tmp)
		}
	}()
	exportedAt := m.now().UTC()
	zw := newZipWriter(f, m.opts.zip64Limit, exportedAt, m.opts.Level)

	var res buildResult
	var files []ManifestFile
	var cur *fileOut
	finish := func() {
		if cur != nil {
			cur.mf.SHA256 = hex.EncodeToString(cur.sum.Sum(nil))
			files = append(files, cur.mf)
			cur = nil
		}
	}
	var done int64
	var line []byte
	for _, dev := range info.Devices {
		sc, err := m.eng.NewScanner(ctx, dev, flt)
		if err != nil {
			return res, err
		}
		n := 0
		for {
			h, more := sc.Next()
			if !more {
				break
			}
			if cur == nil || cur.mf.Day != h.Day {
				finish()
				name := "logs/" + dev + "/" + h.Day + ".log"
				size := int64(-1)
				if b, ok := hint[[2]string{dev, h.Day}]; ok {
					size = b + 64<<10 // lines the index hadn't seen yet
				}
				if err := zw.create(name, size); err != nil {
					sc.Close()
					return res, err
				}
				cur = &fileOut{mf: ManifestFile{Path: name, DeviceID: dev, Day: h.Day, First: h.Received}, sum: sha256.New()}
			}
			if m.opts.testHookLine != nil {
				m.opts.testHookLine()
			}
			// The line as stored: "<received> <sender> <message>", with CR, LF and
			// backslash in the message escaped, so every message is one line.
			line = append(append(line[:0], h.Line...), '\n')
			if _, err := zw.Write(line); err != nil {
				sc.Close()
				return res, err
			}
			cur.sum.Write(line)
			cur.mf.Lines++
			cur.mf.Bytes += int64(len(line))
			cur.mf.Last = h.Received
			res.lines++
			if n++; n%4096 == 0 {
				m.setProgress(e, 0.97*float64(done+sc.Scanned())/float64(total))
				if ctx.Err() != nil {
					break
				}
			}
		}
		done += sc.Scanned()
		sc.Close()
		if err := sc.Err(); err != nil {
			return res, err
		}
		if err := ctx.Err(); err != nil {
			return res, err
		}
		finish()
	}
	finish()

	// The service's own events in the range, and the gaps they show.
	evs, err := m.idx.Events(ctx, time.Time{}, info.To, 0)
	if err != nil {
		return res, err
	}
	var inRange []index.Event
	for _, ev := range evs {
		if !ev.Time.Before(info.From) {
			inRange = append(inRange, ev)
		}
	}
	allFiles, err := m.idx.Files(ctx, "")
	if err != nil {
		return res, err
	}
	lastMessage := func(t time.Time) time.Time {
		var last time.Time
		for _, fl := range allFiles {
			if fl.LastReceived.Before(t) && fl.LastReceived.After(last) {
				last = fl.LastReceived
			}
		}
		return last
	}
	gaps := ComputeGaps(evs, info.From, info.To, exportedAt, lastMessage)

	if err := zw.create("woodshed-events.log", -1); err != nil {
		return res, err
	}
	var eb []byte
	for _, ev := range inRange {
		eb = eb[:0]
		eb = logfile.AppendTime(eb, ev.Time)
		eb = fmt.Appendf(eb, " %s %s", ev.Level, ev.Kind)
		if ev.DeviceID != "" {
			eb = append(eb, ' ')
			eb = append(eb, ev.DeviceID...)
		}
		eb = append(eb, ' ')
		eb = logfile.AppendEscaped(eb, []byte(ev.Text))
		eb = append(eb, '\n')
		if _, err := zw.Write(eb); err != nil {
			return res, err
		}
	}

	if err := zw.create("README.txt", -1); err != nil {
		return res, err
	}
	if _, err := zw.Write([]byte(readme(info))); err != nil {
		return res, err
	}

	devs := make([]ManifestDevice, 0, len(info.Devices))
	for _, id := range info.Devices {
		d, err := m.idx.Device(ctx, id)
		if err != nil {
			d = index.Device{ID: id}
		}
		devs = append(devs, ManifestDevice{ID: d.ID, IP: d.IP, Name: d.Name, Group: d.Group})
	}
	man := Manifest{
		Format: "woodshed-export/1", WoodshedVersion: m.opts.Version, ExportedAt: exportedAt, ID: info.ID,
		Request: ManifestReq{From: info.From, To: info.To, Devices: devs, Filters: ManifestFilters{
			Devices: nonNil(info.Named), Group: info.Filters.Group, Ungrouped: info.Filters.Ungrouped, Severities: SeverityList(info.Filters.Severities),
			Q: info.Filters.Q, Regex: info.Filters.Regex, Case: info.Filters.Case}},
		Files: nonNil(files), Lines: res.lines, Gaps: nonNil(gaps), GapsNote: GapsNote,
		EventsFile: "woodshed-events.log", Events: len(inRange),
	}
	mb, err := json.MarshalIndent(man, "", "  ")
	if err != nil {
		return res, err
	}
	if err := zw.create("manifest.json", -1); err != nil {
		return res, err
	}
	if _, err := zw.Write(append(mb, '\n')); err != nil {
		return res, err
	}
	if err := zw.close(); err != nil {
		return res, err
	}
	if err := ctx.Err(); err != nil {
		return res, err
	}
	if err := f.Sync(); err != nil {
		return res, err
	}
	st, err := f.Stat()
	if err != nil {
		return res, err
	}
	if err := f.Close(); err != nil {
		return res, err
	}
	if err := os.Rename(tmp, m.zipPath(info.ID)); err != nil {
		return res, err
	}
	ok = true
	res.size, res.files = st.Size(), len(files)
	return res, nil
}

func nonNil[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}

func readme(info Info) string {
	var b bytes.Buffer
	fmt.Fprintf(&b, "Woodshed export %s\n", info.ID)
	fmt.Fprintf(&b, "Range: %s to %s (UTC, received time; the end is exclusive)\n\n",
		info.From.Format(time.RFC3339), info.To.Format(time.RFC3339))
	b.WriteString(strings.TrimLeft(`
logs/<device>/<YYYY-MM-DD>.log
    The messages Woodshed received from each device on that day (UTC), oldest
    first, one message per line, exactly as Woodshed stores them:
        <received time, UTC> <sender IP> <message>
    In <message>, a carriage return is written \r, a line feed \n and a
    backslash \\, so a message never spans lines; every other byte is as
    received. To get the messages back with their own line breaks:
        perl -pe 's/\\(\\|r|n)/$1 eq "r" ? "\r" : $1 eq "n" ? "\n" : "\\"/ge' <file>
    <device> is the sender's IP address (":" written as "_" for IPv6).

manifest.json
    What was exported: the request (devices with their names and groups, the
    range and any filters), each log file's line count and SHA-256, and "gaps":
    periods in the range when Woodshed wasn't running or a listener was down,
    so "the device sent nothing" can be told apart from "Woodshed wasn't
    listening". "gaps_note" says how the gaps are worked out.

woodshed-events.log
    Woodshed's own events in the range: starts and stops, listener bind errors,
    file rotations and deletions, dropped messages, paused writes.

To check a file: sha256sum logs/<device>/<day>.log (or on Windows:
certutil -hashfile <file> SHA256) and compare with manifest.json.
`, "\n"))
	return b.String()
}
