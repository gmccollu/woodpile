#!/bin/sh
# Shared by the pkg preinstall and postinstall scripts (sourced, not run).
# The variables below are used by the scripts that source this file.
# shellcheck shell=sh disable=SC2034

LABEL=com.woodpile.daemon
PLIST=/Library/LaunchDaemons/$LABEL.plist
SVC_USER=_woodpile
SVC_GROUP=_woodpile
DATA_DIR="/Library/Application Support/Woodpile"
LOG_DIR=/Library/Logs/Woodpile
UI_URL=http://127.0.0.1:8514/

log() {
	echo "woodpile: $*"
}

# The daemon is loaded in the system domain.
daemon_loaded() {
	launchctl print "system/$LABEL" >/dev/null 2>&1
}

# First ID in 499..300 that is free both as a UniqueID and as a PrimaryGroupID.
# macOS reserves IDs below 500 for system accounts; Apple's own sit mostly below 300.
free_system_id() {
	used=$(
		dscl . -list /Users UniqueID | awk '{print $2}'
		dscl . -list /Groups PrimaryGroupID | awk '{print $2}'
	)
	id=499
	while [ "$id" -ge 300 ]; do
		if ! printf '%s\n' "$used" | grep -qx "$id"; then
			echo "$id"
			return 0
		fi
		id=$((id - 1))
	done
	return 1
}

# Creates the _woodpile group and user if they are missing. Idempotent.
ensure_service_account() {
	if dscl . -read "/Groups/$SVC_GROUP" PrimaryGroupID >/dev/null 2>&1; then
		gid=$(dscl . -read "/Groups/$SVC_GROUP" PrimaryGroupID | awk '{print $2}')
	else
		gid=$(free_system_id) || {
			log "no free system ID between 300 and 499" >&2
			return 1
		}
		dscl . -create "/Groups/$SVC_GROUP"
		dscl . -create "/Groups/$SVC_GROUP" PrimaryGroupID "$gid"
		dscl . -create "/Groups/$SVC_GROUP" RealName "Woodpile syslog server"
		dscl . -create "/Groups/$SVC_GROUP" Password '*'
		log "created group $SVC_GROUP ($gid)"
	fi

	if ! dscl . -read "/Users/$SVC_USER" UniqueID >/dev/null 2>&1; then
		# Reuse the group's ID for the user when it's free, like Apple's own accounts.
		uid=$gid
		if dscl . -list /Users UniqueID | awk '{print $2}' | grep -qx "$uid"; then
			uid=$(free_system_id) || {
				log "no free system ID between 300 and 499" >&2
				return 1
			}
		fi
		dscl . -create "/Users/$SVC_USER"
		dscl . -create "/Users/$SVC_USER" UniqueID "$uid"
		dscl . -create "/Users/$SVC_USER" PrimaryGroupID "$gid"
		dscl . -create "/Users/$SVC_USER" UserShell /usr/bin/false
		dscl . -create "/Users/$SVC_USER" NFSHomeDirectory /var/empty
		dscl . -create "/Users/$SVC_USER" RealName "Woodpile syslog server"
		dscl . -create "/Users/$SVC_USER" Password '*'
		dscl . -create "/Users/$SVC_USER" IsHidden 1
		log "created user $SVC_USER ($uid)"
	fi

	if ! dscl . -read "/Groups/$SVC_GROUP" GroupMembership 2>/dev/null | grep -qw "$SVC_USER"; then
		dscl . -append "/Groups/$SVC_GROUP" GroupMembership "$SVC_USER"
	fi
}
