package listen

import "time"

func portHolder(proto string, port int) string {
	return procPortHolder(procRoot, proto, port, time.Now().Add(holderTimeout))
}
