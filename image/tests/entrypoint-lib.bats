#!/usr/bin/env bats

setup() {
	source "$BATS_TEST_DIRNAME/../entrypoint-lib.sh"
	TEST_DIR="$(mktemp -d)"
}

teardown() {
	rm -rf "$TEST_DIR"
}

@test "linuxbrew migration moves legacy code home brew dir" {
	mkdir -p "$TEST_DIR/code/.linuxbrew/bin"
	mkdir -p "$TEST_DIR/linuxbrew"
	echo "brew" > "$TEST_DIR/code/.linuxbrew/bin/brew"

	migrate_linuxbrew_volume "$TEST_DIR/code" "$TEST_DIR/linuxbrew"

	[ ! -d "$TEST_DIR/code/.linuxbrew" ]
	[ -d "$TEST_DIR/linuxbrew/.linuxbrew/bin" ]
	[ "$(cat "$TEST_DIR/linuxbrew/.linuxbrew/bin/brew")" = "brew" ]
	[ ! -f "$TEST_DIR/linuxbrew/.vibepit-linuxbrew-migrate-lock" ]
}

@test "linuxbrew migration is no-op when destination already exists" {
	mkdir -p "$TEST_DIR/code/.linuxbrew/bin"
	mkdir -p "$TEST_DIR/linuxbrew/.linuxbrew/bin"
	echo "old" > "$TEST_DIR/code/.linuxbrew/bin/brew"
	echo "new" > "$TEST_DIR/linuxbrew/.linuxbrew/bin/brew"

	migrate_linuxbrew_volume "$TEST_DIR/code" "$TEST_DIR/linuxbrew"

	# Existing destination remains authoritative.
	[ "$(cat "$TEST_DIR/linuxbrew/.linuxbrew/bin/brew")" = "new" ]
	# Source is untouched because no merge is attempted.
	[ "$(cat "$TEST_DIR/code/.linuxbrew/bin/brew")" = "old" ]
}

@test "linuxbrew migration is no-op when source is missing" {
	mkdir -p "$TEST_DIR/linuxbrew"

	migrate_linuxbrew_volume "$TEST_DIR/code" "$TEST_DIR/linuxbrew"

	[ ! -e "$TEST_DIR/linuxbrew/.linuxbrew" ]
	[ ! -f "$TEST_DIR/linuxbrew/.vibepit-linuxbrew-migrate-lock" ]
}

@test "linuxbrew migration replaces stale staging dir and completes" {
	mkdir -p "$TEST_DIR/code/.linuxbrew/bin"
	mkdir -p "$TEST_DIR/linuxbrew/.linuxbrew.migrating/bin"
	echo "brew" > "$TEST_DIR/code/.linuxbrew/bin/brew"
	echo "stale" > "$TEST_DIR/linuxbrew/.linuxbrew.migrating/bin/brew"

	migrate_linuxbrew_volume "$TEST_DIR/code" "$TEST_DIR/linuxbrew"

	[ ! -d "$TEST_DIR/code/.linuxbrew" ]
	[ ! -e "$TEST_DIR/linuxbrew/.linuxbrew.migrating" ]
	[ -d "$TEST_DIR/linuxbrew/.linuxbrew/bin" ]
	[ "$(cat "$TEST_DIR/linuxbrew/.linuxbrew/bin/brew")" = "brew" ]
}
