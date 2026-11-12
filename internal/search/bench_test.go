package search

import (
	"testing"
	"time"
)

// BenchmarkSearch pages through the big fixture (60,000 visible lines in plain and
// compressed files over two days) with typical queries, so a change to the
// per-line checks shows up as ns/op. Run it with -benchtime=Nx for stable
// numbers; building the fixture takes a few seconds and isn't timed.
func BenchmarkSearch(b *testing.B) {
	f := newBigFixture(b)
	all := Filter{From: f.day0, To: f.day0.Add(48 * time.Hour)}
	with := func(fn func(q *Query)) Query {
		q := Query{Filter: all, Limit: MaxLimit}
		fn(&q)
		return q
	}
	cases := []struct {
		name string
		q    Query
		full bool // page through everything; otherwise the first page only
	}{
		{"browse_first_page", Query{Filter: all, Limit: DefaultLimit}, false},
		{"browse_all", with(func(q *Query) {}), true},
		{"text_all", with(func(q *Query) { q.Q = "seq=00" }), true},
		{"text_rare", with(func(q *Query) { q.Q = "seq=0012" }), true},
		{"regex_all", with(func(q *Query) { q.Q = `seq=\d+5$`; q.Regex = true }), true},
		{"severity_all", with(func(q *Query) { q.Severities = sevs(3, 4) }), true},
	}
	for _, c := range cases {
		b.Run(c.name, func(b *testing.B) {
			var n int
			for b.Loop() {
				if c.full {
					res, _ := f.all(c.q)
					n = len(res)
					continue
				}
				p, err := f.eng.Search(bg, c.q)
				if err != nil {
					b.Fatal(err)
				}
				n = len(p.Results)
			}
			b.ReportMetric(float64(n), "results")
		})
	}
}
