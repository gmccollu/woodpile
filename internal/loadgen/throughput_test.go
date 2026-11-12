package loadgen

import (
	"context"
	"os"
	"testing"
	"time"
)

// TestThroughputGate is SPEC §2's throughput gate: 10,000 msg/s sustained over UDP
// and over TCP on 127.0.0.1 with zero messages missing or duplicated on disk.
//
// By default it runs for 5 s per protocol (skipped with -short). Set
// WOODSHED_THROUGHPUT=1 for the full 30 s gate, or WOODSHED_THROUGHPUT=<duration>
// (e.g. 10m) for a longer one.
func TestThroughputGate(t *testing.T) {
	dur := 5 * time.Second
	switch v := os.Getenv("WOODSHED_THROUGHPUT"); v {
	case "":
		if testing.Short() {
			t.Skip("throughput gate (set WOODSHED_THROUGHPUT=1 for the full 30 s run)")
		}
	case "1":
		dur = 30 * time.Second
	default:
		d, err := time.ParseDuration(v)
		if err != nil {
			t.Fatalf("WOODSHED_THROUGHPUT=%q: %v", v, err)
		}
		dur = d
	}
	rate := 10000.0
	if raceEnabled {
		// The race detector slows every goroutine several-fold; the gate itself is
		// meant for normal builds.
		rate = 2500
	}
	for _, tc := range []struct{ proto, framing string }{{"udp", ""}, {"tcp", "lf"}, {"tcp", "octet"}} {
		if tc.framing == "octet" && os.Getenv("WOODSHED_THROUGHPUT") == "" {
			continue // the quick run checks one TCP framing
		}
		t.Run(tc.proto+tc.framing, func(t *testing.T) {
			res, err := Bench(context.Background(), BenchOptions{Proto: tc.proto, Framing: tc.framing, Rate: rate, Duration: dur})
			if err != nil {
				t.Fatal(err)
			}
			t.Logf("race=%v\n%s", raceEnabled, res.String())
			if !res.OK() {
				t.Errorf("%d missing, %d duplicates of %d sent", res.Missing, res.Duplicates, res.Sent)
			}
			if res.AchievedRate < 0.98*rate {
				t.Errorf("sender reached only %.0f msg/s of %.0f", res.AchievedRate, rate)
			}
		})
	}
}
