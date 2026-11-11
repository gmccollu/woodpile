// Command woodpile is a simple, reliable syslog server with a web UI.
package main

import (
	"fmt"
	"os"
)

// Set at build time with -ldflags "-X main.version=... -X main.commit=...".
var (
	version = "0.0.0-dev"
	commit  = "none"
)

const usage = `usage: woodpile <command>

commands:
  run        run the server in the foreground
  install    install and start the system service
  uninstall  stop and remove the system service (keeps logs)
  status     show service and listener status
  reindex    rebuild the index from the log files
  send       send test messages to a syslog server
  bench      measure sustained throughput
  version    print the version
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	switch os.Args[1] {
	case "version":
		fmt.Printf("woodpile %s (%s)\n", version, commit)
	default:
		fmt.Fprintf(os.Stderr, "woodpile: %q is not implemented yet\n\n%s", os.Args[1], usage)
		os.Exit(2)
	}
}
