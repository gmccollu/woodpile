package listen

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

func portHolder(proto string, port int) string {
	deadline := time.Now().Add(holderTimeout)
	inodes := map[uint64]bool{}
	for _, suffix := range []string{"", "6"} {
		f, err := os.Open("/proc/net/" + proto + suffix)
		if err != nil {
			continue
		}
		for _, ino := range procNetInodes(f, port, proto == "tcp") {
			inodes[ino] = true
		}
		f.Close()
	}
	if len(inodes) == 0 {
		return ""
	}
	procs, err := os.ReadDir("/proc")
	if err != nil {
		return ""
	}
	for _, p := range procs {
		pid := p.Name()
		if _, err := strconv.Atoi(pid); err != nil {
			continue
		}
		if time.Now().After(deadline) {
			return ""
		}
		fds, err := os.ReadDir(filepath.Join("/proc", pid, "fd"))
		if err != nil {
			continue // another user's process, unless we're privileged
		}
		for _, fd := range fds {
			target, err := os.Readlink(filepath.Join("/proc", pid, "fd", fd.Name()))
			if err != nil {
				continue
			}
			if ino, ok := parseSocketLink(target); ok && inodes[ino] {
				comm, _ := os.ReadFile(filepath.Join("/proc", pid, "comm"))
				return holderText(strings.TrimSpace(string(comm)), pid)
			}
		}
	}
	return ""
}
