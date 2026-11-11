package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"woodpile/internal/server"
	"woodpile/internal/service"
)

func cmdRun(args []string, stdout, stderr io.Writer) int {
	fs := newFlagSet("run", "", `
Run the Woodpile server in the foreground until Ctrl-C (or SIGTERM).

With no --config it uses this OS's service config file. A missing config file
is created with defaults; a config created anywhere else than the default keeps
its data (logs, index) in the same folder.

If a syslog port can't be bound (514 is often taken by the OS's own syslog
daemon, and needs administrator rights on Linux), the server still starts: the
UI shows a banner with the error and a Retry button.`, stderr)
	cfg := configFlag(fs)
	asService := fs.Bool("service", false, "run under the OS service manager (used by the installed service)")
	if code, done := parse(fs, args, stdout); done {
		return code
	}

	if *asService {
		err := service.Run(*cfg, server.DefaultShutdownTimeout, func(ctx context.Context, log *slog.Logger) error {
			return serve(ctx, *cfg, log, nil)
		})
		if err != nil {
			return fail(stderr, "run", err)
		}
		return 0
	}

	log := slog.New(slog.NewTextHandler(stderr, nil))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := serve(ctx, *cfg, log, stdout); err != nil {
		return fail(stderr, "run", err)
	}
	return 0
}

// serve opens and runs the server until ctx is done. With hello set, it prints
// where the UI is once the server is open.
func serve(ctx context.Context, cfgPath string, log *slog.Logger, hello io.Writer) error {
	srv, err := server.Open(cfgPath, server.Options{
		Version:     version,
		Commit:      commit,
		ReleasesURL: releasesURL,
		Logger:      log,
	})
	if err != nil {
		if errors.Is(err, os.ErrPermission) {
			return fmt.Errorf("%w\n(pass --config with a file in a folder you can write, e.g. --config ./woodpile/woodpile.toml)", err)
		}
		return err
	}
	if hello != nil {
		fmt.Fprintf(hello, "Woodpile %s: config %s, data in %s. Press Ctrl-C to stop.\n", version, srv.ConfigPath(), srv.DataDir())
	}
	return srv.Run(ctx)
}
