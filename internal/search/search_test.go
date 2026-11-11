package search

import (
	"errors"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gmccollu/woodpile/internal/hub"
	"github.com/gmccollu/woodpile/internal/model"
	"github.com/gmccollu/woodpile/internal/store"
	"github.com/gmccollu/woodpile/internal/store/logfile"
	"github.com/gmccollu/woodpile/internal/syslog"
)

func TestSeverityAgreesWithParse(t *testing.T) {
	corpus := []string{"", "<", "<>", "<1>", "<191>x", "<192>x", "<0>", "<007>x", "<1234>x", "<12", " <13>x", "\t\r\n\x00<14>x",
		"\xef\xbb\xbf<15>x", "\xef\xbb\xbf \xef\xbb\xbf<11>x", "x<13>", "\xef\xbb<13>", "<-1>x", "<+1>x", "<1 >x", "\n\n<165>1 - - -"}
	rng := rand.New(rand.NewSource(1))
	alphabet := []byte("<>0123456789 \t\r\n\x00\xef\xbb\xbfa")
	for i := 0; i < 20000; i++ {
		b := make([]byte, rng.Intn(10))
		for j := range b {
			b[j] = alphabet[rng.Intn(len(alphabet))]
		}
		corpus = append(corpus, string(b))
	}
	for _, s := range corpus {
		var m model.Message
		syslog.Parse([]byte(s), &m)
		if got := Severity([]byte(s)); got != m.Severity {
			t.Fatalf("Severity(%q) = %d, syslog.Parse says %d", s, got, m.Severity)
		}
	}
}

func TestParseMillis(t *testing.T) {
	rng := rand.New(rand.NewSource(2))
	for i := 0; i < 20000; i++ {
		tm := time.UnixMilli(rng.Int63n(1 << 42)).UTC()
		b := logfile.AppendTime(nil, tm)
		ms, ok := parseMillis(b)
		if !ok || ms != tm.UnixMilli() {
			t.Fatalf("%s: %d %v", b, ms, ok)
		}
	}
	line := []byte("2026-11-10T20:41:07.112+00:00 192.0.2.1 <134>hi")
	ms, esc, ok := splitLine(line)
	if !ok || string(esc) != "<134>hi" || ms != time.Date(2026, 11, 10, 20, 41, 7, 112e6, time.UTC).UnixMilli() {
		t.Errorf("slow path: %d %q %v", ms, esc, ok)
	}
	for _, bad := range []string{"", "2026-11-10T20:41:07.112Z", "2026-11-10T20:41:07.112Z 192.0.2.1", "garbage line here"} {
		if _, _, ok := splitLine([]byte(bad)); ok {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestMatcher(t *testing.T) {
	cases := []struct {
		q            string
		regex, cs    bool
		raw          string
		want, prefil bool
	}{
		{"link down", false, false, "<11>Link DOWN on port", true, true},
		{"LINK", false, true, "<11>Link DOWN", false, true},
		{"Link", false, true, "<11>Link DOWN", true, true},
		{"ünï", false, false, "<11>ÜNÏCODE", true, false},
		{`a\nb`, false, false, "x a\\nb y", true, false},
		{"port [0-9]+$", true, false, "Link down on PORT 12", true, false},
		{"PORT [0-9]+$", true, true, "Link down on port 12", false, false},
		{"zz", false, false, "z", false, true},
		{"", false, false, "anything", true, false},
	}
	for _, c := range cases {
		m, err := newMatcher(c.q, c.regex, c.cs)
		if err != nil {
			t.Fatal(err)
		}
		if got := m.match([]byte(c.raw)); got != c.want {
			t.Errorf("%q regex=%v case=%v on %q: %v", c.q, c.regex, c.cs, c.raw, got)
		}
		if m.prefilter != c.prefil {
			t.Errorf("%q: prefilter %v", c.q, m.prefilter)
		}
	}
	_, err := newMatcher("(unclosed", true, false)
	var qe *QueryError
	if !errors.As(err, &qe) || !strings.HasPrefix(qe.Msg, "error parsing regexp: missing closing )") {
		t.Errorf("regex error: %v", err)
	}
	// The fold prefilter finds every occurrence, either case, in a long buffer.
	m, _ := newMatcher("abc", false, false)
	buf := []byte(strings.Repeat("xaBx", 1000) + "xAbC" + strings.Repeat("a", 5000) + "ABC")
	var found []int
	for i := 0; ; {
		j := m.next(buf, i)
		if j < 0 {
			break
		}
		found = append(found, j)
		i = j + 1
	}
	if len(found) != 2 || found[0] != 4001 || found[1] != len(buf)-3 {
		t.Errorf("fold prefilter found %v", found)
	}
}

func TestSearchFilters(t *testing.T) {
	t.Run("small files", func(t *testing.T) { testSearchFilters(t, newFixture(t, 3, 400)) })
	t.Run("checkpointed files", func(t *testing.T) { testSearchFilters(t, newBigFixture(t)) })
}

func testSearchFilters(t *testing.T, f *fixture) {
	all := Filter{From: f.day0, To: f.day0.Add(72 * time.Hour)}
	with := func(fn func(q *Query)) Query {
		q := Query{Filter: all, Limit: MaxLimit}
		fn(&q)
		return q
	}
	cases := map[string]Query{
		"everything":            with(func(q *Query) {}),
		"q fold":                with(func(q *Query) { q.Q = "LINK down" }),
		"q case":                with(func(q *Query) { q.Q = "Link DOWN"; q.Case = true }),
		"q unicode fold":        with(func(q *Query) { q.Q = "ünïCODE" }),
		"q across escapes":      with(func(q *Query) { q.Q = "multi\nline\r\n" }),
		"q backslash":           with(func(q *Query) { q.Q = `C:\temp\new` }),
		"q not in prefix":       with(func(q *Query) { q.Q = "192.0.2.1" }),
		"regex":                 with(func(q *Query) { q.Q = `port \d+ seq=0+1\d`; q.Regex = true }),
		"regex case":            with(func(q *Query) { q.Q = `^<1[0-9]>host`; q.Regex = true; q.Case = true }),
		"regex anchors raw":     with(func(q *Query) { q.Q = `^no pri`; q.Regex = true }),
		"devices":               with(func(q *Query) { q.Devices = []string{"198.51.100.7", "192.0.2.2"} }),
		"group":                 with(func(q *Query) { q.Group = "HQ" }),
		"ungrouped":             with(func(q *Query) { q.Ungrouped = true }),
		"hidden named":          with(func(q *Query) { q.Devices = []string{"2001_db8__5"} }),
		"group and devices":     with(func(q *Query) { q.Group = "HQ"; q.Devices = []string{"192.0.2.1", "198.51.100.7"} }),
		"severities":            with(func(q *Query) { q.Severities = sevs(0, 3, model.Unknown) }),
		"severities unknown":    with(func(q *Query) { q.Severities = hub.SeverityUnknownBit }),
		"severities none":       with(func(q *Query) { q.Severities = hub.SeverityNone }),
		"time range":            with(func(q *Query) { q.From = f.day0.Add(30 * time.Hour); q.To = f.day0.Add(31*time.Hour + 7*time.Minute) }),
		"time range within day": with(func(q *Query) { q.From = f.day0.Add(20 * time.Hour); q.To = f.day0.Add(20*time.Hour + 30*time.Minute) }),
		"combined":              with(func(q *Query) { q.Q = "seq=0"; q.Group = "HQ"; q.Severities = sevs(1, 2, 6) }),
		"default range":         {Filter: Filter{}, Limit: MaxLimit},
	}
	f.clock.Set(f.day0.Add(40 * time.Hour)) // "now" for the default range
	// The checkpointed fixture must really seek: a 1 h range reads far less than
	// the files.
	{
		p, err := f.eng.Search(bg, Query{Filter: Filter{From: f.day0.Add(30 * time.Hour), To: f.day0.Add(31 * time.Hour)}, Limit: MaxLimit})
		if err != nil {
			t.Fatal(err)
		}
		files, _ := f.idx.Files(bg, "")
		var total int64
		for _, fl := range files {
			total += fl.RawSize
		}
		t.Logf("1 h of %d bytes: scanned %d bytes in %d files", total, p.ScannedBytes, p.ScannedFiles)
	}
	for name, q := range cases {
		for _, asc := range []bool{false, true} {
			q.Asc = asc
			got, _ := f.all(q)
			label := name + map[bool]string{true: " asc", false: " desc"}[asc]
			want := f.expect(q)
			if len(want) == 0 && name != "severities none" && name != "q not in prefix" {
				t.Errorf("%s: the fixture has no matches; the case tests nothing", label)
			}
			same(t, label, got, want)
		}
	}
	if _, hidden := f.all(Query{Filter: all, Limit: MaxLimit}); hidden == 0 {
		t.Fatal("no pages")
	}
	got, _ := f.all(Query{Filter: all, Limit: MaxLimit})
	for _, l := range got {
		if l.DeviceID == "2001_db8__5" {
			t.Fatal("hidden device searched without being named")
		}
	}
}

func TestPaginationContinuity(t *testing.T) {
	f := newFixture(t, 3, 300)
	for _, q := range []Query{
		{Filter: Filter{From: f.day0, To: f.day0.Add(72 * time.Hour)}},
		{Filter: Filter{From: f.day0, To: f.day0.Add(72 * time.Hour), Q: "seq=00"}},
		{Filter: Filter{From: f.day0.Add(20 * time.Hour), To: f.day0.Add(52 * time.Hour), Severities: sevs(4, 5, model.Unknown)}},
	} {
		for _, asc := range []bool{false, true} {
			q.Asc = asc
			full := q
			full.Limit = MaxLimit
			one, _ := f.all(full)
			for _, limit := range []int{1, 7, 64} {
				if limit == 1 && q.Q == "" {
					continue // thousands of pages; 7 covers the same paths
				}
				q.Limit = limit
				paged, pages := f.all(q)
				if pages < 2 {
					t.Fatalf("limit %d: only %d page", limit, pages)
				}
				same(t, "paged", paged, toRecs(one))
				// And both equal the brute force.
				same(t, "full", one, f.expect(q))
			}
		}
	}
	// limit may change from page to page.
	q := Query{Filter: Filter{From: f.day0, To: f.day0.Add(72 * time.Hour)}, Limit: 5}
	var got []Line
	for i := 0; ; i++ {
		p, err := f.eng.Search(bg, q)
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, p.Results...)
		if p.NextCursor == "" {
			break
		}
		q.Cursor, q.Limit = p.NextCursor, 1+i%900
	}
	same(t, "changing limit", got, f.expect(q))
}

func toRecs(ls []Line) []rec {
	out := make([]rec, len(ls))
	for i, l := range ls {
		out[i] = rec{ms: l.Received.UnixMilli(), dev: l.DeviceID, raw: string(l.Raw)}
	}
	return out
}

func TestPartialAndResume(t *testing.T) {
	f := newFixture(t, 3, 300)
	f.eng.budget = time.Nanosecond // every request reads one block, then stops
	for _, asc := range []bool{false, true} {
		for _, text := range []string{"", "seq=0001", "nothing matches this"} {
			q := Query{Filter: Filter{From: f.day0, To: f.day0.Add(72 * time.Hour), Q: text}, Asc: asc, Limit: 100}
			var got []Line
			partials := 0
			for n := 0; ; n++ {
				p, err := f.eng.Search(bg, q)
				if err != nil {
					t.Fatal(err)
				}
				if p.Partial {
					partials++
					if p.NextCursor == "" {
						t.Fatal("partial without a cursor")
					}
				}
				if p.ScannedFiles > 4 {
					t.Fatalf("one-block budget read %d files", p.ScannedFiles)
				}
				got = append(got, p.Results...)
				if p.NextCursor == "" {
					break
				}
				q.Cursor = p.NextCursor
				if n > 200000 {
					t.Fatal("no progress")
				}
			}
			if partials == 0 {
				t.Errorf("%q asc=%v: never partial", text, asc)
			}
			same(t, "partial "+text, got, f.expect(q))
		}
	}
}

func TestCursorTamperAndBinding(t *testing.T) {
	f := newFixture(t, 1, 200)
	q := Query{Filter: Filter{From: f.day0, To: f.day0.Add(24 * time.Hour), Q: "seq"}, Limit: 10}
	p, err := f.eng.Search(bg, q)
	if err != nil || p.NextCursor == "" {
		t.Fatal(err, p.NextCursor)
	}
	var qe *QueryError
	for i := 0; i < len(p.NextCursor); i += 7 {
		b := []byte(p.NextCursor)
		b[i] ^= 1
		if b[i] == '`' || b[i] == '.' || b[i] == '/' || b[i] == '+' {
			continue
		}
		q2 := q
		q2.Cursor = string(b)
		if _, err := f.eng.Search(bg, q2); !errors.As(err, &qe) {
			t.Fatalf("tampered cursor (byte %d): %v", i, err)
		}
	}
	for _, change := range []func(q *Query){
		func(q *Query) { q.Q = "seq=" },
		func(q *Query) { q.Asc = true },
		func(q *Query) { q.Devices = []string{"192.0.2.1"} },
		func(q *Query) { q.To = q.To.Add(time.Second) },
		func(q *Query) { q.Severities = sevs(3) },
		func(q *Query) { q.Regex = true },
	} {
		q2 := q
		q2.Cursor = p.NextCursor
		change(&q2)
		if _, err := f.eng.Search(bg, q2); !errors.As(err, &qe) || !strings.Contains(qe.Msg, "different search") {
			t.Errorf("changed parameters: %v", err)
		}
	}
	// A cursor from another process (another key) is refused.
	other := New(Options{DataDir: f.dir, Index: f.idx, Now: f.clock.Now, Logger: quiet})
	q.Cursor = p.NextCursor
	if _, err := other.Search(bg, q); !errors.As(err, &qe) || !strings.Contains(qe.Msg, "restarted") {
		t.Errorf("foreign cursor: %v", err)
	}
	// Regex errors and bad ranges are QueryErrors too.
	if _, err := f.eng.Search(bg, Query{Filter: Filter{Q: "a(", Regex: true}}); !errors.As(err, &qe) {
		t.Errorf("regex: %v", err)
	}
	if _, err := f.eng.Search(bg, Query{Filter: Filter{From: f.day0, To: f.day0}}); !errors.As(err, &qe) {
		t.Errorf("empty range: %v", err)
	}
}

func TestContextAcrossFiles(t *testing.T) {
	f := newFixture(t, 2, 600)
	devA := model.DeviceID(devA)
	var mine []rec
	for _, r := range f.recs {
		if r.dev == devA {
			mine = append(mine, r)
		}
	}
	files, err := f.idx.Files(bg, devA)
	if err != nil || len(files) < 4 {
		t.Fatalf("want several files, have %d (%v)", len(files), err)
	}
	q := Query{Filter: Filter{From: f.day0, To: f.day0.Add(48 * time.Hour)}, Devices: []string{devA}, Asc: true, Limit: MaxLimit}
	res, _ := f.all(q)
	if len(res) != len(mine) {
		t.Fatalf("%d results, %d written", len(res), len(mine))
	}
	// Every file's first line: context crosses back into the previous file.
	check := func(i, before, after int) {
		t.Helper()
		cp, err := f.eng.Context(bg, res[i].Ref, before, after)
		if err != nil {
			t.Fatalf("line %d: %v", i, err)
		}
		lo, hi := max(i-before, 0), min(i+after, len(mine)-1)
		same(t, "context", cp.Lines, mine[lo:hi+1])
		if cp.HitIndex != i-lo {
			t.Errorf("line %d: hit index %d, want %d", i, cp.HitIndex, i-lo)
		}
		if cp.AtStart != (i-before <= 0) || cp.AtEnd != (i+after >= len(mine)-1) {
			t.Errorf("line %d (±%d/%d): at_start %v at_end %v", i, before, after, cp.AtStart, cp.AtEnd)
		}
		if cp.Lines[cp.HitIndex].Ref != res[i].Ref {
			t.Errorf("line %d: the hit's ref changed", i)
		}
	}
	idxOfFile := map[int64]int{}
	for i, r := range res {
		id, _, _, _ := decodeRef(r.Ref)
		if _, ok := idxOfFile[id]; !ok {
			idxOfFile[id] = i
		}
	}
	if len(idxOfFile) < 4 {
		t.Fatalf("results span %d files", len(idxOfFile))
	}
	for _, i := range idxOfFile {
		check(i, 50, 50)
		if i > 0 {
			check(i-1, 3, 3) // the last line of the previous file
		}
	}
	check(0, 50, 5)
	check(len(res)-1, 5, 50)
	check(len(res)/2, 500, 500)
	check(len(res)/3, 0, 0)

	var qe *QueryError
	if _, err := f.eng.Context(bg, "!!!", 5, 5); !errors.As(err, &qe) {
		t.Errorf("bad ref: %v", err)
	}
	id, off, ms, _ := decodeRef(res[10].Ref)
	if _, err := f.eng.Context(bg, encodeRef(id, off+3, ms), 5, 5); !errors.As(err, &qe) {
		t.Errorf("misaligned ref: %v", err)
	}
	if _, err := f.eng.Context(bg, encodeRef(999999, 0, ms), 5, 5); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown file: %v", err)
	}
}

func TestVanishingFileIsSkipped(t *testing.T) {
	f := newFixture(t, 3, 300)
	files, _ := f.idx.Files(bg, model.DeviceID(devB))
	victim := files[len(files)/2]
	deleted := false
	f.eng.testHookOpen = func(rel string) {
		if !deleted && rel == victim.Path {
			deleted = true
			os.Remove(filepath.Join(f.dir, filepath.FromSlash(victim.Path)))
		}
	}
	for _, asc := range []bool{false, true} {
		q := Query{Filter: Filter{From: f.day0, To: f.day0.Add(72 * time.Hour)}, Asc: asc, Limit: 97}
		got, _ := f.all(q)
		if !deleted {
			t.Fatal("hook didn't fire")
		}
		// Everything except the victim's lines.
		var want []rec
		for _, r := range f.expect(q) {
			if r.dev == victim.DeviceID && r.ms >= victim.FirstReceived.UnixMilli() && r.ms <= victim.LastReceived.UnixMilli() {
				continue
			}
			want = append(want, r)
		}
		same(t, "after a vanished file", got, want)
	}
}

func TestBigFixturePartialAndContext(t *testing.T) {
	f := newBigFixture(t)
	// A compressed file read newest-first needs one forward pass; a budget far
	// below one pass makes that quadratic, so give each request a few blocks.
	f.eng.budget, f.eng.block = 3*time.Millisecond, 64<<10
	for _, asc := range []bool{false, true} {
		q := Query{Filter: Filter{From: f.day0.Add(5 * time.Hour), To: f.day0.Add(40 * time.Hour), Q: "seq=0012"}, Asc: asc, Limit: 50}
		got, pages := f.all(q)
		if pages < 3 {
			t.Errorf("asc=%v: %d pages with a tiny budget", asc, pages)
		}
		same(t, "big partial", got, f.expect(q))
	}
	f.eng.budget = DefaultBudget
	dev := model.DeviceID(devC)
	q := Query{Filter: Filter{From: f.day0, To: f.day0.Add(48 * time.Hour)}, Devices: []string{dev}, Asc: true, Limit: MaxLimit}
	res, _ := f.all(q)
	mine := f.expect(q)
	seen := map[int64]bool{}
	for i, r := range res {
		id, _, _, _ := decodeRef(r.Ref)
		if seen[id] && i%997 != 0 {
			continue
		}
		seen[id] = true
		for _, n := range []int{1, 500} {
			cp, err := f.eng.Context(bg, r.Ref, n, n)
			if err != nil {
				t.Fatal(err)
			}
			lo, hi := max(i-n, 0), min(i+n, len(mine)-1)
			same(t, "big context", cp.Lines, mine[lo:hi+1])
			if cp.HitIndex != i-lo {
				t.Fatalf("hit index %d, want %d", cp.HitIndex, i-lo)
			}
		}
	}
}

// TestSearchWhileWriting pages through searches while the store writes,
// rotates parts, compresses and deletes by retention underneath. No request may
// fail, no line may repeat, and each device's lines come in file order.
func TestSearchWhileWriting(t *testing.T) {
	f := newFixture(t, 2, 200)
	f.st.SetLimits(store.Limits{RetentionDays: 30, RetentionBytes: 400 << 10, PartBytes: 8 << 10})
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		at := f.day0.Add(48*time.Hour + time.Minute)
		seq := 1 << 20
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			for _, a := range f.devs {
				at = at.Add(time.Millisecond)
				f.st.Write(model.Message{Received: at, Sender: a, Raw: []byte(msgText(seq))})
				seq++
			}
			if i%20 == 0 {
				f.clock.Set(at)
				f.st.Maintain()
			}
		}
	}()
	deadline := time.Now().Add(3 * time.Second)
	rounds := 0
	for time.Now().Before(deadline) {
		for _, asc := range []bool{false, true} {
			q := Query{Filter: Filter{From: f.day0, To: f.day0.Add(72 * time.Hour)}, Asc: asc, Limit: 37}
			seen := map[string]bool{}
			last := map[string]int64{}
			for n := 0; ; n++ {
				p, err := f.eng.Search(bg, q)
				if err != nil {
					t.Fatalf("round %d page %d: %v", rounds, n, err)
				}
				for _, l := range p.Results {
					if seen[l.Ref] {
						t.Fatalf("line repeated: %s %q", l.DeviceID, l.Raw)
					}
					seen[l.Ref] = true
					if prev, ok := last[l.DeviceID]; ok && (asc && l.Received.UnixMilli() < prev || !asc && l.Received.UnixMilli() > prev) {
						t.Fatalf("device %s out of order", l.DeviceID)
					}
					last[l.DeviceID] = l.Received.UnixMilli()
				}
				if p.NextCursor == "" || n > 300 {
					break
				}
				q.Cursor = p.NextCursor
			}
			rounds++
		}
	}
	close(stop)
	<-done
	hist, _ := f.idx.Deletions(bg, time.Time{}, time.Time{}, 0)
	t.Logf("%d search rounds; %d files deleted by retention meanwhile", rounds, len(hist))
	if len(hist) == 0 {
		t.Error("retention never deleted a file; the test didn't exercise it")
	}
}
