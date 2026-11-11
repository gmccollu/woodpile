// Command verifyseq checks that the numbered messages of a `woodpile send` run
// (see `woodpile send --run`) are in a Woodpile data dir exactly once. It reads the
// log files directly (compressed or not) and never changes them. Used by the CI
// install smoke tests and scripts/soak.sh.
//
//	verifyseq -data /var/lib/woodpile -run abcd1234 -n 10000 [-wait 30s] [-missing-ok 0] [-json]
//
// Exit status: 0 when no sequence number is duplicated and exactly -missing-ok of
// them are missing (default 0), 1 otherwise, 2 on a usage or read error. With
// -wait it re-reads until the counts are right or the time is up, since the server
// writes its buffers about once a second.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/gmccollu/woodpile/internal/loadgen"
)

func main() {
	data := flag.String("data", "", "Woodpile data `dir` (holds logs/)")
	run := flag.String("run", "", "run `id` given to woodpile send --run")
	n := flag.Int("n", 0, "messages sent: sequence numbers 0..n-1")
	wait := flag.Duration("wait", 0, "keep re-reading for up to this `long` until the counts match")
	missingOK := flag.Int64("missing-ok", 0, "exact `number` of missing messages expected (dropped ones)")
	asJSON := flag.Bool("json", false, "print the result as JSON")
	flag.Parse()
	if *data == "" || *run == "" || *n <= 0 {
		fmt.Fprintln(os.Stderr, "usage: verifyseq -data DIR -run ID -n N [-wait 30s] [-missing-ok K] [-json]")
		os.Exit(2)
	}
	deadline := time.Now().Add(*wait)
	for {
		v, err := loadgen.Verify(*data, *run, *n)
		if err != nil {
			fmt.Fprintln(os.Stderr, "verifyseq:", err)
			os.Exit(2)
		}
		ok := v.Duplicates == 0 && v.Missing == *missingOK
		if ok || time.Now().After(deadline) || v.Duplicates > 0 || v.Missing < *missingOK {
			if *asJSON {
				json.NewEncoder(os.Stdout).Encode(map[string]any{
					"ok": ok, "run": *run, "sent": *n, "received": v.Received, "missing": v.Missing,
					"duplicates": v.Duplicates, "other_lines": v.Foreign, "log_bytes": v.LogBytes,
				})
			} else {
				verdict := "PASS"
				if !ok {
					verdict = "FAIL"
				}
				fmt.Printf("%s run %s: %d sent, %d on disk, %d missing (expected %d), %d duplicates, %d other lines\n",
					verdict, *run, *n, v.Received, v.Missing, *missingOK, v.Duplicates, v.Foreign)
			}
			if !ok {
				os.Exit(1)
			}
			return
		}
		time.Sleep(time.Second)
	}
}
