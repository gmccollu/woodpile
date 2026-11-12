package server

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/gmccollu/woodshed/internal/api"
	"github.com/gmccollu/woodshed/internal/index"
	"github.com/gmccollu/woodshed/internal/loadgen"
)

// searchPage is /api/search as a client reads it.
type searchPage struct {
	Results []struct {
		Received string `json:"received"`
		Raw      string `json:"raw"`
		Ref      string `json:"ref"`
	} `json:"results"`
	NextCursor string `json:"next_cursor"`
	Partial    bool   `json:"partial"`
}

func TestSearchContextExportInProcess(t *testing.T) {
	dir := t.TempDir()
	port := freePort(t)
	r := start(t, writeConfig(t, dir, port, 0, nil))
	run := loadgen.NewRunID()
	if _, err := loadgen.Send(context.Background(), loadgen.SendOptions{
		Addr: fmt.Sprintf("127.0.0.1:%d", port), Proto: "tcp", Count: 300, RunID: run, Rate: 5000, Message: "probe link down",
	}); err != nil {
		t.Fatal(err)
	}
	tag := loadgen.SeqTag(run)
	var page searchPage
	waitFor(t, "300 results", func() bool {
		page = searchPage{}
		r.do(t, "GET", "/api/search?limit=5000&q="+url.QueryEscape(tag), nil, &page)
		return len(page.Results) == 300
	})
	var raw struct {
		Results []struct {
			Received string `json:"received"`
			Severity *int   `json:"severity"`
			Raw      string `json:"raw"`
			Ref      string `json:"ref"`
		} `json:"results"`
	}
	r.do(t, "GET", "/api/search?limit=2&q="+url.QueryEscape(tag+"299 "), nil, &raw)
	if len(raw.Results) != 1 || raw.Results[0].Severity == nil || *raw.Results[0].Severity != 6 ||
		!strings.HasSuffix(raw.Results[0].Raw, tag+"299 probe link down") || len(raw.Results[0].Received) != 24 {
		t.Fatalf("newest result %+v", raw.Results)
	}
	// Pages, desc: newest first and no repeats.
	seen := map[string]bool{}
	cursor := ""
	for {
		var p searchPage
		q := "/api/search?limit=7&q=" + url.QueryEscape("LINK DOWN")
		if cursor != "" {
			q += "&cursor=" + cursor
		}
		if code := r.do(t, "GET", q, nil, &p); code != 200 {
			t.Fatalf("page: %d", code)
		}
		for _, l := range p.Results {
			if seen[l.Ref] {
				t.Fatal("repeat")
			}
			seen[l.Ref] = true
		}
		if cursor = p.NextCursor; cursor == "" {
			break
		}
	}
	if len(seen) != 300 {
		t.Fatalf("paged %d", len(seen))
	}
	if code := r.do(t, "GET", "/api/search?q=x&cursor=garbage", nil, nil); code != 400 {
		t.Errorf("bad cursor: %d", code)
	}
	if code := r.do(t, "GET", "/api/search?regex=true&q=(", nil, nil); code != 400 {
		t.Errorf("bad regex: %d", code)
	}

	// Program/tag, host and exclude, and the fields on each result.
	var fp struct {
		Results []struct {
			App  string `json:"app"`
			Host string `json:"host"`
			Raw  string `json:"raw"`
		} `json:"results"`
		NextCursor string `json:"next_cursor"`
	}
	fq := "/api/search?limit=5000&q=" + url.QueryEscape(tag) + "&app=WOODSHED,other*&host=" + url.QueryEscape(loadgen.DefaultHostname)
	if code := r.do(t, "GET", fq, nil, &fp); code != 200 || len(fp.Results) != 300 || fp.Results[0].App != "woodshed" ||
		fp.Results[0].Host != loadgen.DefaultHostname {
		t.Fatalf("app+host: %d, %d results, first %+v", code, len(fp.Results), fp.Results[:min(1, len(fp.Results))])
	}
	fp.Results = nil
	r.do(t, "GET", "/api/search?limit=5000&q="+url.QueryEscape(tag)+"&app=woodsh*&exclude="+url.QueryEscape(tag+"1"), nil, &fp)
	if len(fp.Results) != 300-111 { // 1, 10-19, 100-199
		t.Errorf("exclude: %d results", len(fp.Results))
	}
	for _, bad := range []string{"app=nothing-sends-this", "host=elsewhere"} {
		fp.Results = nil
		if code := r.do(t, "GET", "/api/search?q="+url.QueryEscape(tag)+"&"+bad, nil, &fp); code != 200 || len(fp.Results) != 0 {
			t.Errorf("%s: %d, %d results", bad, code, len(fp.Results))
		}
	}
	if code := r.do(t, "GET", "/api/search?regex=true&q=a&exclude=(", nil, nil); code != 400 {
		t.Errorf("bad exclude regex: %d", code)
	}
	fp.NextCursor = ""
	r.do(t, "GET", "/api/search?limit=5&app=woodshed&q="+url.QueryEscape(tag), nil, &fp)
	if fp.NextCursor == "" {
		t.Fatal("no cursor")
	}
	for _, changed := range []string{"app=woodshed*", "app=woodshed&host=x", "app=woodshed&exclude=x"} {
		if code := r.do(t, "GET", "/api/search?limit=5&"+changed+"&q="+url.QueryEscape(tag)+"&cursor="+fp.NextCursor, nil, nil); code != 400 {
			t.Errorf("cursor with %s: %d", changed, code)
		}
	}

	var mid struct {
		Results []struct {
			Ref string `json:"ref"`
		} `json:"results"`
	}
	r.do(t, "GET", "/api/search?limit=1&q="+url.QueryEscape(tag+"150 "), nil, &mid)
	var cp struct {
		Lines    []struct{ Raw string } `json:"lines"`
		HitIndex int                    `json:"hit_index"`
		AtStart  bool                   `json:"at_start"`
		AtEnd    bool                   `json:"at_end"`
	}
	if code := r.do(t, "GET", "/api/context?before=3&after=2&ref="+mid.Results[0].Ref, nil, &cp); code != 200 || len(cp.Lines) != 6 ||
		cp.HitIndex != 3 || cp.AtStart || cp.AtEnd || !strings.Contains(cp.Lines[3].Raw, tag+"150 ") || !strings.Contains(cp.Lines[0].Raw, tag+"147 ") {
		t.Fatalf("context %d %+v", code, cp)
	}

	// Export.
	now := time.Now().UTC()
	body := map[string]any{"devices": []string{"127.0.0.1"}, "from": now.Add(-time.Hour).Format(time.RFC3339), "to": now.Add(time.Minute).Format(time.RFC3339), "q": tag}
	var est api.ExportEstimate
	if code := r.do(t, "POST", "/api/exports/estimate", body, &est); code != 200 || est.Lines < 300 {
		t.Fatalf("estimate %d %+v", code, est)
	}
	var created map[string]string
	if code := r.do(t, "POST", "/api/exports", body, &created); code != 202 {
		t.Fatalf("create %d", code)
	}
	id := created["id"]
	var info api.ExportInfo
	waitFor(t, "export ready", func() bool {
		r.do(t, "GET", "/api/exports/"+id, nil, &info)
		return info.State == "ready"
	})
	resp, err := http.Get(r.base + "/api/exports/" + id + "/download")
	if err != nil {
		t.Fatal(err)
	}
	zb, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	zr, err := zip.NewReader(bytes.NewReader(zb), int64(len(zb)))
	if err != nil {
		t.Fatal(err)
	}
	var man struct {
		Files []struct {
			Path  string
			Lines int64
		}
		Gaps []any
	}
	for _, f := range zr.File {
		if f.Name == "manifest.json" {
			rc, _ := f.Open()
			json.NewDecoder(rc).Decode(&man)
			rc.Close()
		}
	}
	if len(man.Files) != 1 || man.Files[0].Lines != 300 {
		t.Fatalf("manifest %+v", man)
	}
	// The same export narrowed by program/tag and exclude: the zip holds what the search showed.
	body["app"], body["exclude"] = []string{"WOODSHED"}, tag+"1"
	if code := r.do(t, "POST", "/api/exports", body, &created); code != 202 {
		t.Fatalf("create filtered %d", code)
	}
	var finfo api.ExportInfo
	waitFor(t, "filtered export ready", func() bool {
		r.do(t, "GET", "/api/exports/"+created["id"], nil, &finfo)
		return finfo.State == "ready"
	})
	if finfo.Lines != 300-111 || finfo.Filters.Exclude != tag+"1" || len(finfo.Filters.App) != 1 || finfo.Filters.Host == nil {
		t.Errorf("filtered export %+v", finfo)
	}
	evs, _ := r.srv.idx.Events(context.Background(), time.Time{}, time.Time{}, 0)
	n := 0
	for _, e := range evs {
		if e.Kind == index.KindExport {
			n++
		}
	}
	if n < 2 {
		t.Errorf("export events: %d", n)
	}
	if code := r.do(t, "DELETE", "/api/exports/"+id, nil, nil); code != 204 {
		t.Errorf("delete %d", code)
	}
}
