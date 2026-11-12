#!/bin/bash
# The pkg's /Applications/Woodshed.app, on a disposable macOS CI machine (it installs the
# pkg for real, as root, and uninstalls it again):
#
#   scripts/smoke/mac-launcher.sh dist/macos/woodshed_X_macos_universal.pkg
#
# Checks the bundle is installed and valid, that its launcher returns 0 and opens the UI
# (with `open` stubbed on PATH) while the service answers, and that uninstall removes it.
# The service-down path shows a dialog, so it isn't run here.
set -euo pipefail

pkg=${1:?pkg}
app=/Applications/Woodshed.app
passes=0
ok() { passes=$((passes + 1)); printf 'PASS %s\n' "$*"; }
die() { printf 'FAIL %s\n' "$*"; exit 1; }

echo "==> install $pkg"
sudo installer -pkg "$pkg" -target / >/dev/null
for _ in $(seq 60); do
	curl -fs --max-time 2 -o /dev/null http://localhost:8514/healthz && break
	sleep 1
done
curl -fs --max-time 2 -o /dev/null http://localhost:8514/healthz || die "the service didn't answer on 8514"

[ -d "$app" ] || die "$app isn't installed"
plutil -lint "$app/Contents/Info.plist" >/dev/null || die "Info.plist doesn't lint"
[ "$(defaults read "$app/Contents/Info" CFBundleIdentifier)" = com.woodshed.launcher ] || die "wrong bundle id"
[ -x "$app/Contents/MacOS/Woodshed" ] || die "the launcher isn't executable"
[ -f "$app/Contents/Resources/woodshed.icns" ] || die "no icon"
mdls -raw -name kMDItemContentType "$app" 2>/dev/null | grep -q application-bundle || echo "note: Spotlight hasn't indexed $app yet"
ok "$app is installed: valid Info.plist, executable launcher, icon"

stub=$(mktemp -d)
printf '#!/bin/sh\nprintf "%%s\\n" "$*" >"%s/opened"\n' "$stub" >"$stub/open"
chmod +x "$stub/open"
PATH="$stub:$PATH" "$app/Contents/MacOS/Woodshed" || die "the launcher exited $? with the service up"
grep -qx 'http://localhost:8514/' "$stub/opened" || die "the launcher opened '$(cat "$stub/opened" 2>/dev/null)', not http://localhost:8514/"
ok "the launcher returns 0 and opens http://localhost:8514/ while the service answers"

echo "==> uninstall"
sudo /usr/local/share/woodshed/uninstall.sh --delete-logs >/dev/null
[ ! -e "$app" ] || die "uninstall.sh left $app"
ok "uninstall.sh removed $app"

echo "mac launcher smoke test: $passes checks passed"
