#!/usr/bin/env bats

setup() {
	source "$BATS_TEST_DIRNAME/../entrypoint-lib.sh"
	TEST_DIR="$(mktemp -d)"
}

teardown() {
	rm -rf "$TEST_DIR"
}

@test "no migration when neither .vibepit-initialized nor .bashrc exist" {
	echo "hello" > "$TEST_DIR/somefile"

	migrate_home_volume "$TEST_DIR"

	[ -f "$TEST_DIR/somefile" ]
	[ ! -d "$TEST_DIR/code" ]
}

@test "migration triggers on .bashrc when .vibepit-initialized is missing" {
	echo "rc" > "$TEST_DIR/.bashrc"
	echo "profile" > "$TEST_DIR/.profile"

	migrate_home_volume "$TEST_DIR"

	[ -d "$TEST_DIR/code" ]
	[ "$(cat "$TEST_DIR/code/.bashrc")" = "rc" ]
	[ "$(cat "$TEST_DIR/code/.profile")" = "profile" ]
}

@test "basic migration moves files into code/" {
	date > "$TEST_DIR/.vibepit-initialized"
	echo "alias ls='ls --color'" > "$TEST_DIR/.bashrc"
	echo "export PATH" > "$TEST_DIR/.profile"
	mkdir -p "$TEST_DIR/projects"
	echo "readme" > "$TEST_DIR/projects/README.md"

	migrate_home_volume "$TEST_DIR"

	[ -d "$TEST_DIR/code" ]
	[ -f "$TEST_DIR/code/.vibepit-initialized" ]
	[ "$(cat "$TEST_DIR/code/.bashrc")" = "alias ls='ls --color'" ]
	[ "$(cat "$TEST_DIR/code/.profile")" = "export PATH" ]
	[ "$(cat "$TEST_DIR/code/projects/README.md")" = "readme" ]
	[ ! -f "$TEST_DIR/.vibepit-initialized" ]
	[ ! -f "$TEST_DIR/.bashrc" ]
}

@test "migration preserves existing code/ directory" {
	date > "$TEST_DIR/.vibepit-initialized"
	echo "rc" > "$TEST_DIR/.bashrc"
	mkdir -p "$TEST_DIR/code/myproject"
	echo "main.go" > "$TEST_DIR/code/myproject/main.go"

	migrate_home_volume "$TEST_DIR"

	[ -f "$TEST_DIR/code/.bashrc" ]
	[ -f "$TEST_DIR/code/.vibepit-initialized" ]
	[ -d "$TEST_DIR/code/code/myproject" ]
	[ "$(cat "$TEST_DIR/code/code/myproject/main.go")" = "main.go" ]
}

@test "migration relocates .linuxbrew" {
	date > "$TEST_DIR/.vibepit-initialized"
	echo "rc" > "$TEST_DIR/.bashrc"
	mkdir -p "$TEST_DIR/.linuxbrew/bin"
	echo "brew" > "$TEST_DIR/.linuxbrew/bin/brew"

	migrate_home_volume "$TEST_DIR"

	[ ! -d "$TEST_DIR/code/.linuxbrew" ]
	[ -d "$TEST_DIR/linuxbrew/.linuxbrew/bin" ]
	[ "$(cat "$TEST_DIR/linuxbrew/.linuxbrew/bin/brew")" = "brew" ]
}

@test "migration works without .linuxbrew" {
	date > "$TEST_DIR/.vibepit-initialized"
	echo "rc" > "$TEST_DIR/.bashrc"

	migrate_home_volume "$TEST_DIR"

	[ -d "$TEST_DIR/code" ]
	[ -f "$TEST_DIR/code/.bashrc" ]
	[ ! -d "$TEST_DIR/linuxbrew" ]
}

@test "migration handles files starting with dash" {
	date > "$TEST_DIR/.vibepit-initialized"
	echo "dashed" > "$TEST_DIR/-dashed-file"

	migrate_home_volume "$TEST_DIR"

	[ "$(cat "$TEST_DIR/code/-dashed-file")" = "dashed" ]
}

@test "migration includes user files named .migrate-*" {
	date > "$TEST_DIR/.vibepit-initialized"
	echo "userdata" > "$TEST_DIR/.migrate-notes"

	migrate_home_volume "$TEST_DIR"

	[ "$(cat "$TEST_DIR/code/.migrate-notes")" = "userdata" ]
}

@test "migration skips mount point children" {
	date > "$TEST_DIR/.vibepit-initialized"
	echo "rc" > "$TEST_DIR/.bashrc"
	mkdir -p "$TEST_DIR/jane/src"
	echo "project" > "$TEST_DIR/jane/src/main.go"

	# Simulate /home/jane being a mount point child.
	_mountpoint_children() { echo "jane"; }
	export -f _mountpoint_children

	migrate_home_volume "$TEST_DIR"

	# jane/ must remain in place (not moved into code/).
	[ -f "$TEST_DIR/jane/src/main.go" ]
	[ ! -d "$TEST_DIR/code/jane" ]
	# Regular files should still be migrated.
	[ -f "$TEST_DIR/code/.bashrc" ]
	[ -f "$TEST_DIR/code/.vibepit-initialized" ]
}

@test "no temp directory or lockfile remains after migration" {
	date > "$TEST_DIR/.vibepit-initialized"
	echo "rc" > "$TEST_DIR/.bashrc"

	migrate_home_volume "$TEST_DIR"

	# No .migrate-* dirs should remain.
	run bash -c "ls -d '$TEST_DIR'/.migrate-* 2>/dev/null"
	[ "$output" = "" ]
	[ ! -f "$TEST_DIR/.vibepit-migrate-lock" ]
}
