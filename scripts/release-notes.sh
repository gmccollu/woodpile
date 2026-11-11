#!/bin/bash
# Prints the GitHub release notes for a tag, taken from CHANGELOG.md.
#
#   scripts/release-notes.sh v1.0.0 [CHANGELOG.md] > notes.md
#
# CHANGELOG.md uses "## [X.Y.Z] - YYYY-MM-DD" headings (Keep a Changelog).
#   - A release tag (v1.0.0) needs its own section, dated: it fails if the section is
#     missing or still says "unreleased", so a release never goes out with stale notes.
#   - A prerelease tag (v0.9.0-rc1) uses its own section if there is one, otherwise the
#     newest section, under a line saying it's a prerelease of that version.
set -euo pipefail

tag=${1:?usage: release-notes.sh <tag> [CHANGELOG.md]}
file=${2:-CHANGELOG.md}
version=${tag#v}

[ -f "$file" ] || { echo "release-notes.sh: $file not found" >&2; exit 1; }

# heading_for <version>: the "## [version] ..." line, or nothing.
heading_for() {
	grep -m1 -F "## [$1]" "$file" || true
}

# body_of <heading line>: the lines after it up to the next "## [" heading, trimmed.
body_of() {
	awk -v h="$1" '
		found && /^## \[/ { exit }
		found { lines[++n] = $0 }
		$0 == h { found = 1 }
		END {
			s = 1; while (s <= n && lines[s] ~ /^[[:space:]]*$/) s++
			e = n; while (e >= s && lines[e] ~ /^[[:space:]]*$/) e--
			for (i = s; i <= e; i++) print lines[i]
		}' "$file"
}

heading=$(heading_for "$version")

case "$version" in
	*-*) # prerelease
		if [ -z "$heading" ]; then
			heading=$(grep -m1 '^## \[' "$file" || true)
			[ -n "$heading" ] || { echo "release-notes.sh: no '## [' section in $file" >&2; exit 1; }
			upcoming=$(printf '%s\n' "$heading" | sed -n 's/^## \[\([^]]*\)\].*/\1/p')
			printf '**Prerelease %s, for testing.** The notes below are for the upcoming %s.\n\n' "$tag" "$upcoming"
		else
			printf '**Prerelease %s, for testing.**\n\n' "$tag"
		fi
		;;
	*)
		if [ -z "$heading" ]; then
			echo "release-notes.sh: $file has no '## [$version]' section; add one before tagging $tag" >&2
			exit 1
		fi
		if printf '%s\n' "$heading" | grep -qi 'unreleased'; then
			echo "release-notes.sh: '$heading' is still marked unreleased; set the release date before tagging $tag" >&2
			exit 1
		fi
		;;
esac

body=$(body_of "$heading")
[ -n "$body" ] || { echo "release-notes.sh: the '$heading' section is empty" >&2; exit 1; }
printf '%s\n' "$body"
