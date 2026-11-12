package api

import (
	"runtime/metrics"
	"testing"
	"time"
)

// TestArgonKeyReturnsMemory: a default-size hash leaves its 64 MiB with the OS,
// not as free heap the scavenger may keep for minutes (the idle-RSS report).
func TestArgonKeyReturnsMemory(t *testing.T) {
	argonKey([]byte("password-123"), []byte("saltsaltsaltsalt"), 1, defaultArgon.Memory, 1, 32)
	s := []metrics.Sample{{Name: "/memory/classes/heap/free:bytes"}}
	metrics.Read(s)
	if free := s[0].Value.Uint64(); free > 24<<20 {
		t.Errorf("%d MiB of free heap kept after the hash", free>>20)
	}
}

// TestArgonKeyOneAtATime: a second hash waits for the first.
func TestArgonKeyOneAtATime(t *testing.T) {
	argonSlot <- struct{}{} // another hash in progress
	done := make(chan struct{})
	go func() {
		argonKey([]byte("pw"), []byte("saltsalt"), 1, 8, 1, 16)
		close(done)
	}()
	select {
	case <-done:
		t.Fatal("ran beside another hash")
	case <-time.After(50 * time.Millisecond):
	}
	<-argonSlot
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("never ran")
	}
}
