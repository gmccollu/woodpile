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

# sips renders the SVG on its transparent background (a QuickLook thumbnail, used before,
# is opaque white: a white square on a dark Dock or taskbar); iconutil takes it from there.
png="$work/logo.png"
sips -s format png -z 1024 1024 "$svg" --out "$png" >/dev/null
[ -f "$png" ] || { echo "make-icons.sh: sips didn't render $svg" >&2; exit 1; }
# The top-left pixel must be transparent. Its bytes are stored unfiltered whatever the first
# row's filter, so they're bytes 1-4 of the decompressed image data (8-bit RGBA).
python3 -I - "$png" <<'EOF'
import struct, sys, zlib
data = open(sys.argv[1], "rb").read()
depth, ctype = data[24], data[25]
idat, i = b"", 8
while i < len(data):
    n, kind = struct.unpack(">I4s", data[i:i + 8])
    if kind == b"IDAT":
        idat += data[i + 8:i + 8 + n]
    i += 12 + n
alpha = zlib.decompress(idat)[4]
if depth != 8 or ctype != 6 or alpha != 0:
    sys.exit(f"make-icons.sh: {sys.argv[1]} isn't transparent (depth {depth}, color type {ctype}, corner alpha {alpha})")
EOF

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
