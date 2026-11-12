#!/bin/bash
# Regenerates the committed app icons from the logo (web/public/favicon.svg), on macOS:
#   packaging/icons/woodshed.icns   the macOS launcher (/Applications/Woodshed.app)
#   packaging/icons/woodshed.ico    the Windows Start-menu shortcut and Add/Remove Programs
# The Linux packages use the SVG itself. Run it after changing the logo, and commit the result.
set -euo pipefail

here=$(cd "$(dirname "$0")" && pwd)
repo=$(cd "$here/../.." && pwd)
svg="$repo/web/public/favicon.svg"
work=$(mktemp -d "${TMPDIR:-/tmp}/woodshed-icons.XXXXXX")
trap 'rm -rf "$work"' EXIT

# QuickLook renders SVG; sips and iconutil take it from there.
qlmanage -t -s 1024 -o "$work" "$svg" >/dev/null
png="$work/favicon.svg.png"
[ -f "$png" ] || { echo "make-icons.sh: QuickLook didn't render $svg" >&2; exit 1; }

set_dir="$work/woodshed.iconset"
mkdir "$set_dir"
for s in 16 32 128 256 512; do
	sips -z "$s" "$s" "$png" --out "$set_dir/icon_${s}x${s}.png" >/dev/null
	sips -z $((s * 2)) $((s * 2)) "$png" --out "$set_dir/icon_${s}x${s}@2x.png" >/dev/null
done
iconutil -c icns "$set_dir" -o "$here/woodshed.icns"

# An .ico whose entries are PNGs (supported since Windows Vista).
for s in 16 32 48 256; do
	sips -z "$s" "$s" "$png" --out "$work/ico_$s.png" >/dev/null
done
python3 -I - "$work" "$here/woodshed.ico" <<'EOF'
import struct, sys
from pathlib import Path
work, out = Path(sys.argv[1]), Path(sys.argv[2])
sizes = [16, 32, 48, 256]
pngs = [(work / f"ico_{s}.png").read_bytes() for s in sizes]
header = struct.pack("<HHH", 0, 1, len(sizes))
offset = 6 + 16 * len(sizes)
entries, data = b"", b""
for s, png in zip(sizes, pngs):
    entries += struct.pack("<BBBBHHII", s % 256, s % 256, 0, 0, 1, 32, len(png), offset + len(data))
    data += png
out.write_bytes(header + entries + data)
EOF
echo "wrote $here/woodshed.icns and $here/woodshed.ico"
