#!/usr/bin/env bats

setup() {
	TEST_DIR="$(mktemp -d)"
	export HOME="$TEST_DIR/home"
	export VIBEPIT_PROJECT_DIR="$TEST_DIR/project with spaces"
	export VIBEPIT_WORKING_DIR="$VIBEPIT_PROJECT_DIR/nested dir"
	mkdir -p "$HOME" "$VIBEPIT_WORKING_DIR"
	shopt -s expand_aliases
	source "$BATS_TEST_DIRNAME/../config/profile"
}

teardown() {
	rm -rf "$TEST_DIR"
}

@test "cdp changes to the project directory" {
	cd /
	eval cdp

	[ "$PWD" = "$VIBEPIT_PROJECT_DIR" ]
}

@test "cdw changes to the initial working directory" {
	cd /
	eval cdw

	[ "$PWD" = "$VIBEPIT_WORKING_DIR" ]
}
