package main

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"io"
	"runtime"
	"strings"

	"woodpile/internal/config"
)

// cmdFlags is a subcommand's flag set and help text.
type cmdFlags struct {
	*flag.FlagSet
	args   string // positional arguments in the usage line, "" for none
	desc   string
	stderr io.Writer
}

// newFlagSet returns the flag set for a subcommand. Its help is "usage: woodpile
// <name> [flags] <argsUsage>", desc and the flags. Usage errors go to stderr.
func newFlagSet(name, argsUsage, desc string, stderr io.Writer) *cmdFlags {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.Usage = func() {}
	return &cmdFlags{FlagSet: fs, args: argsUsage, desc: desc, stderr: stderr}
}

// help returns the subcommand's help text.
func (f *cmdFlags) help() string {
	var b strings.Builder
	fmt.Fprintf(&b, "usage: woodpile %s", f.Name())
	n := 0
	f.VisitAll(func(*flag.Flag) { n++ })
	if n > 0 {
		b.WriteString(" [flags]")
	}
	if f.args != "" {
		b.WriteString(" " + f.args)
	}
	b.WriteString("\n\n" + strings.TrimSpace(f.desc) + "\n")
	if n > 0 {
		b.WriteString("\nflags:\n")
		var buf bytes.Buffer
		f.SetOutput(&buf)
		f.PrintDefaults()
		f.SetOutput(io.Discard)
		b.WriteString(buf.String())
	}
	return b.String()
}

// parse parses args. done is true when the command must stop now, with code: 0
// after --help (printed to stdout), 2 after a usage error (printed to stderr).
// --help anywhere wins, so asking for help never runs the command.
func parse(f *cmdFlags, args []string, stdout io.Writer) (code int, done bool) {
	for i, a := range args {
		if a == "--" {
			break
		}
		if a == "-h" || a == "-help" || a == "--help" || a == "-?" || (i == 0 && a == "help") {
			fmt.Fprint(stdout, f.help())
			return 0, true
		}
	}
	err := f.Parse(args)
	if errors.Is(err, flag.ErrHelp) {
		fmt.Fprint(stdout, f.help())
		return 0, true
	}
	if err != nil {
		fmt.Fprintf(f.stderr, "woodpile %s: %v\n\n%s", f.Name(), err, f.help())
		return 2, true
	}
	if f.args == "" && f.NArg() > 0 {
		fmt.Fprintf(f.stderr, "woodpile %s: unexpected argument %q\n\n%s", f.Name(), f.Arg(0), f.help())
		return 2, true
	}
	return 0, false
}

// configFlag adds --config with the OS default.
func configFlag(f *cmdFlags) *string {
	return f.String("config", config.DefaultConfigPath(), "config `file`")
}

func platform() string { return runtime.GOOS + "/" + runtime.GOARCH }
