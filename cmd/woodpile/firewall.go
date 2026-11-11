package main

import "strings"

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
