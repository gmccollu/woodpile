package loadgen

import (
	"errors"
	"net"
	"os"
	"testing"
)

func writeErr(errno error) error {
	return &net.OpError{Op: "write", Net: "udp", Err: os.NewSyscallError("write", errno)}
}

func TestSendErrnos(t *testing.T) {
	if !isNoBufs(writeErr(errNoBufs)) {
		t.Errorf("isNoBufs missed %v", errNoBufs)
	}
	if !isConnRefused(writeErr(errConnRefused)) {
		t.Errorf("isConnRefused missed %v", errConnRefused)
	}
	other := errors.New("other")
	if isNoBufs(writeErr(other)) || isConnRefused(writeErr(other)) {
		t.Error("matched an unrelated error")
	}
}
