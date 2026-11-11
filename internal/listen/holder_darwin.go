package listen

import (
	"context"
	"os/exec"
	"strconv"
)

func portHolder(proto string, port int) string {
	bin := "/usr/sbin/lsof"
	if _, err := exec.LookPath(bin); err != nil {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), holderTimeout)
	defer cancel()
	args := []string{"-nP", "-w", "-F", "pc"}
	if proto == "tcp" {
		args = append(args, "-iTCP:"+strconv.Itoa(port), "-sTCP:LISTEN")
	} else {
		args = append(args, "-iUDP:"+strconv.Itoa(port))
	}
	out, _ := exec.CommandContext(ctx, bin, args...).Output() // lsof exits 1 when it finds nothing
	return parseLsof(out)
}
