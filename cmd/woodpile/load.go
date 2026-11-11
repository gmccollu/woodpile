package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"time"

	"woodpile/internal/loadgen"
)

func cmdSend(args []string, stdout, stderr io.Writer) int {
	fs := newFlagSet("send", "", `
Send test syslog messages to a syslog server (Woodpile or any other). Each
message is RFC 5424 and carries a run tag and a sequence number
("seq=<run>:<n>"), so you can find it in the logs.

Examples:
  woodpile send --message "hello from woodpile"          one message to 127.0.0.1:514 over UDP
  woodpile send --host 192.0.2.10 --proto tcp --count 100
  woodpile send --port 15514 --rate 200 --duration 60s    a steady stream for a minute`, stderr)
	host := fs.String("host", "127.0.0.1", "syslog server `host` name or IP")
	port := fs.Int("port", 514, "syslog server `port`")
	proto := fs.String("proto", "udp", "`protocol`: udp or tcp")
	framing := fs.String("framing", "lf", "TCP `framing`: lf (newline) or octet (RFC 6587 octet counting)")
	count := fs.Int("count", 0, "`number` of messages (default 1, or rate x duration)")
	rate := fs.Float64("rate", 0, "`messages` per second (0: as fast as possible)")
	duration := fs.Duration("duration", 0, "send at --rate for this `duration` (e.g. 60s), instead of --count")
	message := fs.String("message", "", "message `text` after the sequence tag (default: padding to --size)")
	size := fs.Int("size", loadgen.DefaultSize, "approximate message size in `bytes` when --message isn't set")
	source := fs.String("source", "", "local `IP` to send from (default: chosen by the OS)")
	hostname := fs.String("hostname", loadgen.DefaultHostname, "the messages' HOSTNAME `field`")
	if code, done := parse(fs, args, stdout); done {
		return code
	}
	usageErr := func(err error) int {
		fmt.Fprintf(stderr, "woodpile send: %v\n\n%s", err, fs.help())
		return 2
	}
	if *port < 1 || *port > 65535 {
		return usageErr(fmt.Errorf("--port %d is out of range (1-65535)", *port))
	}
	*proto = strings.ToLower(*proto)
	if *proto != "udp" && *proto != "tcp" {
		return usageErr(fmt.Errorf("--proto must be udp or tcp, not %q", *proto))
	}
	*framing = strings.ToLower(*framing)
	if *framing != "lf" && *framing != "octet" {
		return usageErr(fmt.Errorf("--framing must be lf or octet, not %q", *framing))
	}
	if *count < 0 || *rate < 0 || *duration < 0 {
		return usageErr(errors.New("--count, --rate and --duration can't be negative"))
	}
	if *duration > 0 && *rate == 0 {
		return usageErr(errors.New("--duration needs --rate"))
	}
	if *duration > 0 && *count > 0 {
		return usageErr(errors.New("give --count or --duration, not both"))
	}
	if *count == 0 && *duration == 0 {
		*count = 1
	}
	o := loadgen.SendOptions{
		Addr: net.JoinHostPort(*host, strconv.Itoa(*port)), Proto: *proto, Framing: *framing,
		Count: *count, Rate: *rate, Duration: *duration, Size: *size,
		Message: *message, Source: *source, Hostname: *hostname,
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	res, err := loadgen.Send(ctx, o)
	what := strings.ToUpper(*proto)
	if *proto == "tcp" {
		what += " (" + *framing + ")"
	}
	plural := "s"
	if res.Sent == 1 {
		plural = ""
	}
	switch {
	case res.Sent == 1 && err == nil:
		fmt.Fprintf(stdout, "Sent 1 message to %s over %s, run %s.\n", o.Addr, what, res.RunID)
	case res.Sent > 0 || err == nil:
		fmt.Fprintf(stdout, "Sent %d message%s to %s over %s in %v (%.0f msg/s), run %s.\n",
			res.Sent, plural, o.Addr, what, res.Elapsed.Round(time.Millisecond), res.Rate, res.RunID)
	}
	if res.Sent > 0 {
		fmt.Fprintf(stdout, "Find them by searching for %q.\n", loadgen.SeqTag(res.RunID))
	}
	if err != nil {
		if errors.Is(err, context.Canceled) {
			fmt.Fprintln(stderr, "woodpile send: interrupted")
			return 1
		}
		return fail(stderr, "send", err)
	}
	if *proto == "udp" {
		fmt.Fprintln(stdout, "(UDP has no delivery confirmation: check the receiver to see that they arrived.)")
	}
	return 0
}

func cmdBench(args []string, stdout, stderr io.Writer) int {
	fs := newFlagSet("bench", "", `
Measure sustained throughput: run an in-process Woodpile (listener on
127.0.0.1, store and live hub) in a temporary data folder, send to it at --rate
for --duration, then read the files back and check that every message arrived
exactly once. It doesn't touch a running Woodpile or its data.

Exit status: 0 when every message arrived exactly once, 1 otherwise.`, stderr)
	proto := fs.String("proto", "udp", "`protocol`: udp, tcp or all (udp, tcp lf, tcp octet in turn)")
	framing := fs.String("framing", "lf", "TCP `framing`: lf or octet")
	rate := fs.Float64("rate", loadgen.DefaultBenchRate, "target `messages` per second")
	duration := fs.Duration("duration", loadgen.DefaultBenchDuration, "how long to send (a `duration` such as 30s or 10m)")
	size := fs.Int("size", loadgen.DefaultSize, "approximate message size in `bytes`")
	dataDir := fs.String("data-dir", "", "write the logs here instead of a temporary `folder` (kept afterwards)")
	keep := fs.Bool("keep", false, "keep the temporary data folder")
	asJSON := fs.Bool("json", false, "print the results as JSON")
	if code, done := parse(fs, args, stdout); done {
		return code
	}
	type run struct{ proto, framing string }
	var runs []run
	switch strings.ToLower(*proto) {
	case "udp":
		runs = []run{{"udp", ""}}
	case "tcp":
		if *framing != "lf" && *framing != "octet" {
			fmt.Fprintf(stderr, "woodpile bench: --framing must be lf or octet, not %q\n", *framing)
			return 2
		}
		runs = []run{{"tcp", *framing}}
	case "all":
		runs = []run{{"udp", ""}, {"tcp", "lf"}, {"tcp", "octet"}}
	default:
		fmt.Fprintf(stderr, "woodpile bench: --proto must be udp, tcp or all, not %q\n\n%s", *proto, fs.help())
		return 2
	}
	if *rate <= 0 || *duration <= 0 {
		fmt.Fprintln(stderr, "woodpile bench: --rate and --duration must be positive")
		return 2
	}
	if *dataDir != "" && len(runs) > 1 {
		fmt.Fprintln(stderr, "woodpile bench: --data-dir works with one protocol at a time")
		return 2
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	ok := true
	var results []loadgen.BenchResult
	for _, r := range runs {
		if !*asJSON {
			fmt.Fprintf(stdout, "bench: %s %s at %.0f msg/s for %v...\n", strings.ToUpper(r.proto), r.framing, *rate, *duration)
		}
		res, err := loadgen.Bench(ctx, loadgen.BenchOptions{
			Proto: r.proto, Framing: r.framing, Rate: *rate, Duration: *duration, Size: *size,
			DataDir: *dataDir, Keep: *keep,
		})
		results = append(results, res)
		if err != nil {
			ok = false
			fmt.Fprintf(stderr, "woodpile bench: %s: %v\n", r.proto, strings.TrimPrefix(err.Error(), "loadgen: "))
			if ctx.Err() != nil {
				break
			}
			continue
		}
		if !res.OK() {
			ok = false
		}
		if !*asJSON {
			fmt.Fprint(stdout, res.String())
			if *keep || *dataDir != "" {
				fmt.Fprintf(stdout, "  data kept in %s\n", res.DataDir)
			}
		}
	}
	if *asJSON {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(results)
	}
	if !ok {
		return 1
	}
	return 0
}
