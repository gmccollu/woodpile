package index

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"testing"
	"time"
)

func TestSavedSearches(t *testing.T) {
	dir := t.TempDir()
	d := openIn(t, dir)
	a, err := d.CreateSavedSearch(ctx, SavedSearch{Name: "  Edge denies ", Type: "syslog", Query: `{"q":"deny"}`}, t0)
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`^ss_[0-9a-f]{8}$`).MatchString(a.ID) || a.Name != "Edge denies" || a.Version != 1 || !a.Created.Equal(t0) {
		t.Fatalf("%+v", a)
	}
	// Unique ignoring case, on create and on rename.
	if _, err := d.CreateSavedSearch(ctx, SavedSearch{Name: "EDGE DENIES", Type: "syslog", Query: `{}`}, t0); !errors.Is(err, ErrNameTaken) {
		t.Fatalf("clash: %v", err)
	}
	b, err := d.CreateSavedSearch(ctx, SavedSearch{Name: "auth", Type: "syslog", Query: `{}`}, t0)
	if err != nil {
		t.Fatal(err)
	}
	b.Name = "edge Denies"
	if _, err := d.UpdateSavedSearch(ctx, b, 0, t0); !errors.Is(err, ErrNameTaken) {
		t.Fatalf("rename clash: %v", err)
	}
	// Renaming to another case of its own name is fine.
	a.Name = "EDGE denies"
	a2, err := d.UpdateSavedSearch(ctx, a, 1, t0.Add(time.Minute))
	if err != nil || a2.Version != 2 || !a2.Updated.Equal(t0.Add(time.Minute)) || !a2.Created.Equal(t0) {
		t.Fatalf("%+v %v", a2, err)
	}
	// A stale version is refused.
	if _, err := d.UpdateSavedSearch(ctx, a, 1, t0); !errors.Is(err, ErrSavedChanged) {
		t.Fatalf("stale: %v", err)
	}
	if _, err := d.UpdateSavedSearch(ctx, SavedSearch{ID: "ss_00000000", Name: "x"}, 0, t0); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown: %v", err)
	}
	list, err := d.SavedSearches(ctx, "syslog")
	if err != nil || len(list) != 2 || list[0].Name != "auth" || list[1].Name != "EDGE denies" {
		t.Fatalf("%+v %v", list, err)
	}
	if l, _ := d.SavedSearches(ctx, "trap"); len(l) != 0 {
		t.Fatalf("other type: %+v", l)
	}
	// reindex rebuilds files and checkpoints only: saved searches stay.
	os.MkdirAll(filepath.Join(dir, "logs"), 0o750)
	if _, err := d.Reindex(ctx, dir); err != nil {
		t.Fatal(err)
	}
	if got, err := d.SavedSearch(ctx, a.ID); err != nil || got.Query != `{"q":"deny"}` || got.Version != 2 {
		t.Fatalf("after reindex %+v %v", got, err)
	}
	// They survive a reopen.
	d.Close()
	d = openIn(t, dir)
	if err := d.DeleteSavedSearch(ctx, b.ID); err != nil {
		t.Fatal(err)
	}
	if err := d.DeleteSavedSearch(ctx, b.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("delete twice: %v", err)
	}
	if _, err := d.SavedSearch(ctx, b.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("read deleted: %v", err)
	}
	// At most MaxSavedSearches, of every type.
	for i := 1; i < MaxSavedSearches; i++ {
		typ := "syslog"
		if i%2 == 0 {
			typ = "other"
		}
		if _, err := d.CreateSavedSearch(ctx, SavedSearch{Name: fmt.Sprint("s", i), Type: typ, Query: `{}`}, t0); err != nil {
			t.Fatalf("%d: %v", i, err)
		}
	}
	if _, err := d.CreateSavedSearch(ctx, SavedSearch{Name: "one more", Type: "syslog", Query: `{}`}, t0); !errors.Is(err, ErrSavedLimit) {
		t.Fatalf("limit: %v", err)
	}
}

func TestHistogramReads(t *testing.T) {
	d := openIn(t, t.TempDir())
	min0 := t0.Truncate(time.Hour)
	var acts []Activity
	for i := 0; i < 180; i++ {
		for _, dev := range []string{"192.0.2.1", "192.0.2.2"} {
			acts = append(acts, Activity{DeviceID: dev, IP: dev, Minute: min0.Add(time.Duration(i) * time.Minute), Counts: Counts{Msgs: 2}})
		}
	}
	if err := d.Apply(ctx, Batch{Activity: acts}); err != nil {
		t.Fatal(err)
	}
	// Roll the first hour into the hourly table: both together still count it once.
	if _, err := d.RollupStats(ctx, min0.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	from, to := min0.UnixMilli(), min0.Add(3*time.Hour).UnixMilli()
	cut := map[string]int64{"192.0.2.1": from, "192.0.2.2": min0.Add(2 * time.Hour).UnixMilli()}
	got, err := d.StatsBuckets(ctx, cut, false, from, to, from, time.Hour.Milliseconds())
	if err != nil {
		t.Fatal(err)
	}
	if got[0] != 120 || got[1] != 120 || got[2] != 240 {
		t.Errorf("both tables: %v", got)
	}
	got, _ = d.StatsBuckets(ctx, cut, true, from, to, from, time.Hour.Milliseconds())
	if got[0] != 0 || got[1] != 120 || got[2] != 240 {
		t.Errorf("minute table only: %v", got)
	}
	if got, _ := d.StatsBuckets(ctx, map[string]int64{}, false, from, to, from, 1); len(got) != 0 {
		t.Errorf("no devices: %v", got)
	}
	if o, err := d.OldestFiles(ctx, []string{"192.0.2.1"}); err != nil || len(o) != 0 {
		t.Errorf("no files: %v %v", o, err)
	}
	if c, err := d.CheckpointsFor(ctx, []int64{1, 2}); err != nil || len(c) != 0 {
		t.Errorf("no checkpoints: %v %v", c, err)
	}
}
