package main

import (
	"fmt"
	"strings"
)

type ruleState int

const (
	ruleUnknown ruleState = iota // no rule names this program (or the output wasn't readable)
	ruleAllows                   // an enabled inbound rule allows it
	ruleBlocks                   // only block (or disabled) rules name it
)

// firewallRuleState reads `netsh advfirewall firewall show rule name=all dir=in
// verbose` (English output; anything else reads as ruleUnknown) for the rules
// whose Program is exe.
func firewallRuleState(netsh, exe string) ruleState {
	netsh = strings.ReplaceAll(netsh, "\r\n", "\n")
	state := ruleUnknown
	for _, block := range strings.Split(netsh, "\n\n") {
		fields := map[string]string{}
		for _, line := range strings.Split(block, "\n") {
			k, v, ok := strings.Cut(line, ":")
			if ok {
				fields[strings.TrimSpace(k)] = strings.TrimSpace(v)
			}
		}
		if !strings.EqualFold(fields["Program"], exe) {
			continue
		}
		if strings.EqualFold(fields["Enabled"], "Yes") && strings.EqualFold(fields["Action"], "Allow") {
			return ruleAllows
		}
		state = ruleBlocks
	}
	return state
}

// firewallCommands are the netsh commands that let devices reach this copy (re-test
// N17): this program only, UDP and TCP on the syslog port only, on Private and
// Domain networks only.
func firewallCommands(exe string, port int) string {
	var s strings.Builder
	for _, proto := range []string{"UDP", "TCP"} {
		fmt.Fprintf(&s, "      netsh advfirewall firewall add rule name=\"Woodshed (portable) %s %d\" dir=in action=allow "+
			"program=\"%s\" protocol=%s localport=%d profile=private,domain enable=yes\n", proto, port, exe, proto, port)
	}
	s.WriteString("  These allow Private and Domain networks only. If the devices are on a network Windows calls\n" +
		"  Public, mark that network Private in Settings, or add profile=public to the commands.\n")
	return s.String()
}
