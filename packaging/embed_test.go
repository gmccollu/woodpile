package packaging

import "testing"

func TestEmbedded(t *testing.T) {
	for name, s := range map[string]string{"unit": SystemdUnit, "sysctl": SysctlDropIn, "plist": LaunchdPlist,
		"newsyslog": NewsyslogConf, "account script": MacAccountScript} {
		if len(s) < 50 {
			t.Errorf("%s is empty", name)
		}
	}
}
