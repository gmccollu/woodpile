package service

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// runner runs a command and returns its combined output (faked in tests).
type runner interface {
	run(name string, args ...string) (string, error)
}

type execRunner struct{}

func (execRunner) run(name string, args ...string) (string, error) {
	var out bytes.Buffer
	cmd := exec.Command(name, args...)
	cmd.Stdout, cmd.Stderr = &out, &out
	err := cmd.Run()
	s := strings.TrimSpace(out.String())
	if err != nil {
		if s != "" {
			return s, fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, s)
		}
		return s, fmt.Errorf("%s %s: %w", name, strings.Join(args, " "), err)
	}
	return s, nil
}

// host is the machine an installer changes: commands and files. The real one is
// the root filesystem; tests use a temp dir as the root and record commands.
type host struct {
	runner
	root string // prefix for every path ("" for the real machine)
	out  io.Writer
}

func realHost(out io.Writer) *host { return &host{runner: execRunner{}, out: out} }

func (h *host) path(p string) string {
	if h.root == "" {
		return p
	}
	return filepath.Join(h.root, p)
}

func (h *host) exists(p string) bool {
	_, err := os.Stat(h.path(p))
	return err == nil
}

func (h *host) readFile(p string) (string, error) {
	b, err := os.ReadFile(h.path(p))
	return string(b), err
}

func (h *host) writeFile(p, data string, perm os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(h.path(p)), 0o755); err != nil {
		return err
	}
	tmp := h.path(p) + ".woodpile-tmp"
	if err := os.WriteFile(tmp, []byte(data), perm); err != nil {
		return err
	}
	if err := os.Chmod(tmp, perm); err != nil {
		return err
	}
	return os.Rename(tmp, h.path(p))
}

func (h *host) mkdirAll(p string, perm os.FileMode) error { return os.MkdirAll(h.path(p), perm) }

func (h *host) chmod(p string, perm os.FileMode) error { return os.Chmod(h.path(p), perm) }

// copyFile copies src (a real path, not under root) to dst.
func (h *host) copyFile(src, dst string, perm os.FileMode) error {
	if h.root == "" {
		if same, _ := sameFile(src, dst); same {
			return nil
		}
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	if err := os.MkdirAll(filepath.Dir(h.path(dst)), 0o755); err != nil {
		return err
	}
	tmp := h.path(dst) + ".woodpile-tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, perm)
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, in); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp, perm); err != nil {
		return err
	}
	return os.Rename(tmp, h.path(dst))
}

func sameFile(a, b string) (bool, error) {
	fa, err := os.Stat(a)
	if err != nil {
		return false, err
	}
	fb, err := os.Stat(b)
	if err != nil {
		return false, err
	}
	return os.SameFile(fa, fb), nil
}

// remove deletes a file; a missing one is fine.
func (h *host) remove(p string) error {
	err := os.Remove(h.path(p))
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

func (h *host) removeAll(p string) error { return os.RemoveAll(h.path(p)) }

// say prints a progress line.
func (h *host) say(format string, args ...any) {
	fmt.Fprintf(h.out, "  "+format+"\n", args...)
}

// quoteIfSpace quotes a path for a systemd unit line when it has spaces.
func quoteIfSpace(s string) string {
	if strings.ContainsAny(s, " \t") {
		return `"` + s + `"`
	}
	return s
}
