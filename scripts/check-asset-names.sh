#!/bin/bash
# Fails if any release file would be renamed by GitHub on upload. GitHub keeps only
# letters, digits, ".", "-" and "_" in asset names (it turned the "~" in
# woodpile_0.9.0~rc1_amd64.deb into "."), and then checksums.txt names files that
# aren't on the release.
#
#   scripts/check-asset-names.sh dist [more folders...]
#
# Checks the files directly in each folder (GoReleaser's dist/ holds its build folders
# and metadata next to them; only files are uploaded).
set -euo pipefail

[ $# -gt 0 ] || { echo "usage: check-asset-names.sh <folder>..." >&2; exit 2; }
bad=0
n=0
for dir in "$@"; do
	for f in "$dir"/*; do
		[ -f "$f" ] || continue
		name=$(basename "$f")
		n=$((n + 1))
		if ! printf '%s' "$name" | grep -Eq '^[A-Za-z0-9._-]+$'; then
			echo "GitHub would rename this asset: $name" >&2
			bad=1
		fi
	done
done
[ "$n" -gt 0 ] || { echo "check-asset-names.sh: no files in $*" >&2; exit 1; }
[ "$bad" -eq 0 ] && echo "all $n file names are safe for GitHub release assets"
exit "$bad"
