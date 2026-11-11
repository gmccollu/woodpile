#!/bin/bash
# Runs every Go fuzz target for a short time each (SPEC 3.2: the parser is fuzzed in CI).
#
#   scripts/fuzz.sh [fuzztime] [packages]     defaults: 30s ./internal/...
#
# `go test -fuzz` accepts one package and one target per run, so this lists the
# targets and runs them one by one. With no fuzz targets it exits 0.
set -euo pipefail

fuzztime=${1:-30s}
pkgs=${2:-./internal/...}

# `go test -list` prints each package's matching names, then an "ok <pkg>" line.
targets=$(go test -list '^Fuzz' "$pkgs" | awk '
	/^Fuzz/ { names[++n] = $1; next }
	/^ok / { for (i = 1; i <= n; i++) print $2, names[i]; n = 0 }
')

if [ -z "$targets" ]; then
	echo "fuzz.sh: no fuzz targets in $pkgs"
	exit 0
fi

while read -r pkg name; do
	echo "==> $pkg $name ($fuzztime)"
	go test -run '^$' -fuzz "^${name}\$" -fuzztime "$fuzztime" "$pkg"
done <<<"$targets"
