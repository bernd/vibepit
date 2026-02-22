#!/bin/bash

# migrate_linuxbrew_volume moves legacy Homebrew data from
# <code_home>/.linuxbrew to <linuxbrew_home>/.linuxbrew.
#
# Usage: migrate_linuxbrew_volume [code_home] [linuxbrew_home]
#   code_home: legacy code home (default: /home/code)
#   linuxbrew_home: linuxbrew home root (default: /home/linuxbrew)
migrate_linuxbrew_volume() {
	local code_home="${1:-/home/code}"
	local linuxbrew_home="${2:-/home/linuxbrew}"
	local src="$code_home/.linuxbrew"
	local dst="$linuxbrew_home/.linuxbrew"
	local staging="${dst}.migrating"
	local lockfile="$linuxbrew_home/.vibepit-linuxbrew-migrate-lock"
	local rc

	if [ ! -d "$src" ] || [ -e "$dst" ]; then
		return 0
	fi

	mkdir -p "$linuxbrew_home"

	type vp_status &>/dev/null && vp_status "Migrating Homebrew to $dst..."
	(
		flock 9

		# Re-check after lock acquisition.
		if [ ! -d "$src" ] || [ -e "$dst" ]; then
			exit 0
		fi

		# Ensure stale staging data from a prior interrupted migration does not block retry.
		rm -rf "$staging"

		# Use staged copy+atomic rename to avoid partial destination state on interruption.
		cp -a "$src" "$staging"
		mv "$staging" "$dst"
		rm -rf "$src"
	) 9>"$lockfile"
	rc=$?
	rm -f "$lockfile"
	if [ "$rc" -ne 0 ]; then
		return "$rc"
	fi

	type vp_status &>/dev/null && vp_status "Homebrew migration complete." || true
}
