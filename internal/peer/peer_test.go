package peer

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

const launchctlRunning = `system/com.woodshed.daemon = {
	active count = 1
	path = /Library/LaunchDaemons/com.woodshed.daemon.plist
	type = LaunchDaemon
	state = running

	program = /usr/local/bin/woodshed
	arguments = {
		/usr/local/bin/woodshed
		run
	}

	pid = 4210
	immediate reason = speculative
	endpoints = {
		state = active
	}
}
`

func TestParseLaunchctl(t *testing.T) {
	if r, pid := parseLaunchctl([]byte(launchctlRunning)); !r || pid != 4210 {
		t.Errorf("running: %v %d", r, pid)
	}
	stopped := "system/com.woodshed.daemon = {\n\tstate = not running\n\tlast exit code = 0\n}\n"
	if r, pid := parseLaunchctl([]byte(stopped)); r || pid != 0 {
		t.Errorf("stopped: %v %d", r, pid)
	}
}

func TestParseSystemctl(t *testing.T) {
	if r, pid := parseSystemctl([]byte("MainPID=812\nActiveState=active\n")); !r || pid != 812 {
		t.Errorf("active: %v %d", r, pid)
	}
	if r, _ := parseSystemctl([]byte("MainPID=0\nActiveState=inactive\n")); r {
		t.Error("inactive read as running")
	}
}

func writeState(t *testing.T, dir string, body string) string {
	t.Helper()
	p := filepath.Join(dir, "woodshed.state.json")
	if err := os.WriteFile(p, []byte(body), 0o640); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestFind(t *testing.T) {
	ctx := context.Background()
	svcState := writeState(t, t.TempDir(), `{"pid": 4210, "ui": {"listening": false, "url": "", "error": "listen tcp 127.0.0.1:8514: bind: address already in use"}}`)
	copyState := writeState(t, t.TempDir(), `{"pid": 777, "ui": {"listening": true, "url": "http://localhost:8514/"}}`)
	query := func(running bool, pid int) func(context.Context, *Options) (bool, int) {
		return func(context.Context, *Options) (bool, int) { return running, pid }
	}
	alive := func(pids ...int) func(int) bool {
		return func(p int) bool {
			for _, x := range pids {
				if x == p {
					return true
				}
			}
			return false
		}
	}
	// The service, with its readable state file.
	info, ok := Find(ctx, Options{StateFiles: []string{copyState, svcState}, SelfPID: 1, query: query(true, 4210), alive: alive(777)})
	if !ok || info.Kind != KindService || info.PID != 4210 || !info.UIKnown || info.UIListening || info.UIError == "" {
		t.Errorf("service: %+v %v", info, ok)
	}
	// Its config and listeners come from the state file too.
	listening := writeState(t, t.TempDir(), `{"pid": 4210, "config": "/etc/woodshed/woodshed.toml", "ui": {"listening": true, "url": "http://localhost:8514/"},
		"listeners": [{"proto": "udp", "addr": "0.0.0.0", "port": 514, "ok": true}, {"proto": "tcp", "addr": "0.0.0.0", "port": 1514, "ok": false}]}`)
	info, ok = Find(ctx, Options{StateFiles: []string{listening}, SelfPID: 1, query: query(true, 4210)})
	if !ok || info.Config != "/etc/woodshed/woodshed.toml" || len(info.Listeners) != 2 || info.Listeners[0] != (Listener{"udp", 514}) || info.Listeners[1] != (Listener{"tcp", 1514}) {
		t.Errorf("service listeners: %+v", info)
	}
	// The service, state file unreadable: kind and PID only.
	info, ok = Find(ctx, Options{StateFiles: []string{"/nonexistent/woodshed.state.json"}, query: query(true, 4210)})
	if !ok || info.Kind != KindService || info.PID != 4210 || info.UIKnown {
		t.Errorf("service without state: %+v", info)
	}
	// No service: a live copy from its state file.
	info, ok = Find(ctx, Options{StateFiles: []string{copyState}, SelfPID: 1, query: query(false, 0), alive: alive(777)})
	if !ok || info.Kind != KindCopy || info.PID != 777 || info.UIURL != "http://localhost:8514/" {
		t.Errorf("copy: %+v %v", info, ok)
	}
	// A dead copy, or ourselves, isn't a peer; nor is the service when it's us.
	if _, ok := Find(ctx, Options{StateFiles: []string{copyState}, query: query(false, 0), alive: alive()}); ok {
		t.Error("dead copy reported")
	}
	if _, ok := Find(ctx, Options{StateFiles: []string{copyState}, SelfPID: 777, query: query(false, 0), alive: alive(777)}); ok {
		t.Error("self reported")
	}
	if _, ok := Find(ctx, Options{SelfPID: 4210, query: query(true, 4210)}); ok {
		t.Error("the service reported itself")
	}
	// The real query runs (and finds nothing in a test sandbox, or the real
	// service): it must not fail or hang.
	Find(ctx, Options{SelfPID: os.Getpid()})
}
