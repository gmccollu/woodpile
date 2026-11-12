#!/bin/bash
# Builds the macOS installer: a universal (arm64 + x86_64) woodshed binary, a component
# package (pkgbuild) and the product archive (productbuild).
#
# The pkg installs the daemon, the binary and /Applications/Woodshed.app (a launcher for the UI).
#
#   packaging/macos/build-pkg.sh [--version 1.2.3] [--commit abc1234] [--out dist/macos]
#                                [--universal path/to/woodshed]
#                                [--arm64 path/to/woodshed --amd64 path/to/woodshed]
#
# --universal packages a universal binary as is (the release uses GoReleaser's, so the pkg
# and the darwin archive ship the same file). --arm64/--amd64 are combined with lipo. With
# neither, it builds both with `go build`; build the frontend first (make web) so the real
# UI is embedded.
#
# Optional signing, used only when the variables are set:
#   MACOS_APP_SIGN_IDENTITY        "Developer ID Application: ..."  signs the binary (hardened runtime)
#   MACOS_INSTALLER_SIGN_IDENTITY  "Developer ID Installer: ..."    signs the .pkg
#   MACOS_KEYCHAIN                 keychain that holds them (default: the search list)
# Notarization happens afterwards, in the release workflow.
#
# Never needs sudo, and never installs anything.
set -euo pipefail

here=$(cd "$(dirname "$0")" && pwd)
repo=$(cd "$here/../.." && pwd)

version=""
commit=""
out="$repo/dist/macos"
bin_arm64=""
bin_amd64=""
bin_universal=""

while [ $# -gt 0 ]; do
	case "$1" in
		--version) version=$2; shift 2 ;;
		--commit) commit=$2; shift 2 ;;
		--out) out=$2; shift 2 ;;
		--arm64) bin_arm64=$2; shift 2 ;;
		--amd64) bin_amd64=$2; shift 2 ;;
		--universal) bin_universal=$2; shift 2 ;;
		-h | --help) sed -n '2,22p' "$0" | sed 's/^# \{0,1\}//'; exit 0 ;;
		*) echo "build-pkg.sh: unknown option $1" >&2; exit 2 ;;
	esac
done

if [ -z "$version" ]; then
	version=$(git -C "$repo" describe --tags --abbrev=0 2>/dev/null || echo v0.0.0)
fi
version=${version#v}
if [ -z "$commit" ]; then
	commit=$(git -C "$repo" rev-parse --short HEAD 2>/dev/null || echo none)
fi
# pkg versions are dotted numbers; drop any -prerelease/+build suffix for the receipt.
pkg_version=${version%%[-+]*}
case "$pkg_version" in
	"" | *[!0-9.]*) pkg_version=0.0.0 ;; # e.g. `git describe --always` on an untagged tree
esac

work=$(mktemp -d "${TMPDIR:-/tmp}/woodshed-pkg.XXXXXX")
trap 'rm -rf "$work"' EXIT

build_one() { # goarch output
	CGO_ENABLED=0 GOOS=darwin GOARCH=$1 go -C "$repo" build -trimpath \
		-ldflags "-s -w -X main.version=$version -X main.commit=$commit" \
		-o "$2" ./cmd/woodshed
}

if [ -z "$bin_universal" ] && { [ -z "$bin_arm64" ] || [ -z "$bin_amd64" ]; }; then
	echo "==> go build darwin/arm64 and darwin/amd64 ($version, $commit)"
	bin_arm64="$work/woodshed-arm64"
	bin_amd64="$work/woodshed-amd64"
	build_one arm64 "$bin_arm64"
	build_one amd64 "$bin_amd64"
fi

root="$work/root"
mkdir -p "$root/usr/local/bin" \
	"$root/usr/local/share/woodshed" \
	"$root/Library/LaunchDaemons" \
	"$root/private/etc/newsyslog.d"

if [ -n "$bin_universal" ]; then
	echo "==> using the universal binary $bin_universal"
	cp "$bin_universal" "$root/usr/local/bin/woodshed"
else
	echo "==> lipo (universal binary)"
	lipo -create -output "$root/usr/local/bin/woodshed" "$bin_arm64" "$bin_amd64"
fi
archs=$(lipo -archs "$root/usr/local/bin/woodshed")
case "$archs" in
	*x86_64*arm64* | *arm64*x86_64*) echo "    archs: $archs" ;;
	*) echo "build-pkg.sh: universal binary has archs '$archs'" >&2; exit 1 ;;
esac
chmod 0755 "$root/usr/local/bin/woodshed"

if [ -n "${MACOS_APP_SIGN_IDENTITY:-}" ]; then
	echo "==> codesign the binary"
	codesign --force --options runtime --timestamp \
		${MACOS_KEYCHAIN:+--keychain "$MACOS_KEYCHAIN"} \
		--sign "$MACOS_APP_SIGN_IDENTITY" "$root/usr/local/bin/woodshed"
	codesign --verify --strict --verbose=2 "$root/usr/local/bin/woodshed"
else
	echo "==> MACOS_APP_SIGN_IDENTITY not set; the binary stays unsigned (ad-hoc arm64 signature only)"
fi

install -m 0644 "$here/com.woodshed.daemon.plist" "$root/Library/LaunchDaemons/com.woodshed.daemon.plist"
install -m 0644 "$here/com.woodshed.daemon.newsyslog.conf" "$root/private/etc/newsyslog.d/com.woodshed.daemon.conf"
install -m 0755 "$here/uninstall.sh" "$root/usr/local/share/woodshed/uninstall.sh"
if [ ! -f "$repo/THIRD_PARTY_NOTICES.txt" ]; then
	echo "build-pkg.sh: THIRD_PARTY_NOTICES.txt is missing; run scripts/third_party_notices.py" >&2
	exit 1
fi
install -m 0644 "$repo/LICENSE" "$root/usr/local/share/woodshed/LICENSE"
install -m 0644 "$repo/THIRD_PARTY_NOTICES.txt" "$root/usr/local/share/woodshed/THIRD_PARTY_NOTICES.txt"

# /Applications/Woodshed.app: a launcher that opens the web UI (packaging/macos/app).
app="$root/Applications/Woodshed.app/Contents"
mkdir -p "$app/MacOS" "$app/Resources"
sed "s/@VERSION@/$pkg_version/g" "$here/app/Info.plist" >"$app/Info.plist"
chmod 0644 "$app/Info.plist"
install -m 0755 "$here/app/Woodshed" "$app/MacOS/Woodshed"
install -m 0644 "$repo/packaging/icons/woodshed.icns" "$app/Resources/woodshed.icns"
plutil -lint "$app/Info.plist" >/dev/null
plutil -lint "$root/Library/LaunchDaemons/com.woodshed.daemon.plist" >/dev/null

scripts="$work/scripts"
mkdir -p "$scripts"
install -m 0755 "$here/scripts/preinstall" "$here/scripts/postinstall" "$scripts/"
install -m 0644 "$here/scripts/common.sh" "$scripts/"

# Every file in the pkg gets the release commit's time (SOURCE_DATE_EPOCH, else the commit),
# not the time this machine checked out or downloaded it. The payload stores UTC times.
epoch=${SOURCE_DATE_EPOCH:-$(git -C "$repo" log -1 --format=%ct 2>/dev/null || date +%s)}
stamp=$(date -u -r "$epoch" +%Y%m%d%H%M.%S)
TZ=UTC0 find "$root" "$scripts" -exec touch -h -t "$stamp" {} +
echo "==> file times: $(date -u -r "$epoch" +%Y-%m-%dT%H:%M:%SZ)"

# Drop removable xattrs. pkgbuild stores any left (macOS adds an undeletable
# com.apple.provenance on some machines) as ._ AppleDouble entries; Installer's
# BOMCopier merges those back into xattrs, so no ._ files land on disk.
xattr -cr "$root" "$scripts" 2>/dev/null || true

mkdir -p "$out"
component="$work/woodshed-component.pkg"
# The app must land in /Applications: not relocatable (Installer would otherwise move it to
# wherever another bundle with its identifier already is) and not upgraded by version alone.
components="$work/components.plist"
pkgbuild --analyze --root "$root" "$components" >/dev/null
n=$(plutil -extract . raw -o - "$components" 2>/dev/null || echo 0)
for ((i = 0; i < n; i++)); do
	plutil -replace "$i.BundleIsRelocatable" -bool NO "$components"
	plutil -replace "$i.BundleOverwriteAction" -string upgrade "$components"
done
echo "==> pkgbuild"
pkgbuild --root "$root" \
	--identifier com.woodshed.pkg \
	--version "$pkg_version" \
	--install-location / \
	--ownership recommended \
	--component-plist "$components" \
	--scripts "$scripts" \
	"$component"

resources="$work/resources"
mkdir -p "$resources"
cp "$repo/LICENSE" "$resources/LICENSE.txt"
cp "$here/conclusion.html" "$resources/conclusion.html"
sed "s/@VERSION@/$pkg_version/" "$here/distribution.xml" >"$work/distribution.xml"

product="$out/woodshed_${version}_macos_universal.pkg"
echo "==> productbuild"
sign_args=()
if [ -n "${MACOS_INSTALLER_SIGN_IDENTITY:-}" ]; then
	sign_args=(--sign "$MACOS_INSTALLER_SIGN_IDENTITY" --timestamp)
	if [ -n "${MACOS_KEYCHAIN:-}" ]; then sign_args+=(--keychain "$MACOS_KEYCHAIN"); fi
else
	echo "    MACOS_INSTALLER_SIGN_IDENTITY not set; the .pkg stays unsigned"
fi
productbuild --distribution "$work/distribution.xml" \
	--package-path "$work" \
	--resources "$resources" \
	${sign_args[@]+"${sign_args[@]}"} \
	"$product"

echo "==> $product"
