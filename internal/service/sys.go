package service

import (
	"bytes"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
)

func lookupIDs(name, group string) (int, int, error) {
	u, err := user.Lookup(name)
	if err != nil {
		return 0, 0, fmt.Errorf("look up the %s user: %w", name, err)
	}
	g, err := user.LookupGroup(group)
	if err != nil {
		return 0, 0, fmt.Errorf("look up the %s group: %w", group, err)
	}
	uid, err := strconv.Atoi(u.Uid)
	if err != nil {
		return 0, 0, err
	}
	gid, err := strconv.Atoi(g.Gid)
	if err != nil {
		return 0, 0, err
	}
	return uid, gid, nil
}

// runner runs a command and returns its combined output (faked in tests).
type runner interface {
	run(name string, args ...string) (string, error)
	// runInput is run with stdin from input.
	runInput(input, name string, args ...string) (string, error)
}

type execRunner struct{}

func (r execRunner) run(name string, args ...string) (string, error) {
	return r.runInput("", name, args...)
}

func (execRunner) runInput(input, name string, args ...string) (string, error) {
	var out bytes.Buffer
	cmd := exec.Command(name, args...)
	if input != "" {
		cmd.Stdin = strings.NewReader(input)
	}
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
	// chown changes a path's owner without following a symlink (os.Lchown on the
	// real machine; recorded in tests).
	chown func(path string, uid, gid int) error
	// lookupIDs resolves a user and group name.
	lookupIDs func(user, group string) (uid, gid int, err error)
}

func realHost(out io.Writer) *host {
	return &host{runner: execRunner{}, out: out, chown: os.Lchown, lookupIDs: lookupIDs}
}

// chownTree gives dir and everything in it to user:group (security review M1c,
// M1f): it never follows a symlink (they're skipped, and dir itself must not be
// one) and skips any file with more than one link, which could be a hard link to
// a file outside the folder. It returns how many entries it skipped.
func (h *host) chownTree(dir, user, group string) (skipped int, err error) {
	uid, gid, err := h.lookupIDs(user, group)
	if err != nil {
		return 0, err
	}
	root := h.path(dir)
	fi, err := os.Lstat(root)
	if err != nil {
		return 0, err
	}
	if fi.Mode()&os.ModeSymlink != 0 || !fi.IsDir() {
		return 0, fmt.Errorf("%s is a link or not a folder", dir)
	}
	err = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type()&fs.ModeSymlink != 0 {
			skipped++
			return nil
		}
		if !d.IsDir() {
			info, err := d.Info()
			if err != nil {
				return err
			}
			if !info.Mode().IsRegular() || linkCount(info) > 1 {
				skipped++
				return nil
			}
		}
		return h.chown(p, uid, gid)
	})
	return skipped, err
}

// chownFile gives one file (not a symlink, one link) to user:group.
func (h *host) chownFile(path, user, group string) error {
	uid, gid, err := h.lookupIDs(user, group)
	if err != nil {
		return err
	}
	fi, err := os.Lstat(h.path(path))
	if err != nil {
		return err
	}
	if fi.Mode()&os.ModeSymlink != 0 || !fi.Mode().IsRegular() || linkCount(fi) > 1 {
		return fmt.Errorf("%s is a link or not a regular file; not changing its owner", path)
	}
	return h.chown(h.path(path), uid, gid)
}

// chownDir gives one folder (not a symlink) to user:group.
func (h *host) chownDir(path, user, group string) error {
	uid, gid, err := h.lookupIDs(user, group)
	if err != nil {
		return err
	}
	fi, err := os.Lstat(h.path(path))
	if err != nil {
		return err
	}
	if fi.Mode()&os.ModeSymlink != 0 || !fi.IsDir() {
		return fmt.Errorf("%s is a link or not a folder; not changing its owner", path)
	}
	return h.chown(h.path(path), uid, gid)
}

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
