#!/bin/bash

# migrate_home_volume relocates files from an old-style volume layout (where the
# volume was mounted at /home/code) to the new layout (volume mounted at /home).
# After the mount point change, old user files appear at the volume root and need
# to move into a "code" subdirectory.
#
# Usage: migrate_home_volume <base_dir>
#   base_dir: the volume mount point (e.g. /home)
migrate_home_volume() {
	local base="$1"

	if [ ! -f "$base/.vibepit-initialized" ] && [ ! -f "$base/.bashrc" ]; then
		return 0
	fi

	# Serialize concurrent migrations on the shared home volume.
	local lockfile="$base/.vibepit-migrate-lock"
	(
		flock 9

		# Re-check after acquiring the lock; another process may have
		# completed the migration already.
		if [ ! -f "$base/.vibepit-initialized" ] && [ ! -f "$base/.bashrc" ]; then
			exit 0
		fi

		# If a rename fails mid-migration (I/O error, full disk), files may be
		# split between $base/ and $base/.migrate-$$/, and the re-check on
		# next start would skip migration. Recovery: manually move contents of
		# any leftover .migrate-*/ directory into $base/code/. This is
		# unlikely since all renames are within the same filesystem.
		set -e
		tmpname=".migrate-$$"
		mkdir "$base/$tmpname"
		cd "$base"
		# extglob must be enabled before bash parses the !(pattern) glob.
		# -O enables shell options before the command string is parsed.
		# Exclude only the exact temp dir and the lockfile.
		bash -O extglob -O dotglob -c 'mv -- !("$1"|.vibepit-migrate-lock) "$2/"' _ "$tmpname" "$base/$tmpname"
		mv "$base/$tmpname" "$base/code"

		# Relocate linuxbrew from old path to new path.
		if [ -d "$base/code/.linuxbrew" ]; then
			mkdir -p "$base/linuxbrew"
			mv "$base/code/.linuxbrew" "$base/linuxbrew/.linuxbrew"
		fi
	) 9>"$lockfile"
	rm -f "$lockfile"
}
