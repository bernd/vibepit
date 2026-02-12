# shellcheck shell=bash
# Sourced by entrypoint and other vibepit shell scripts.
# Respects NO_COLOR (https://no-color.org) and non-TTY streams.

vp_status() {
	if [ -t 1 ] && [ -z "${NO_COLOR-}" ]; then
		printf '\033[1;36m%s\033[0m\n' "$*"
	else
		printf '%s\n' "$*"
	fi
}

vp_error() {
	if [ -t 2 ] && [ -z "${NO_COLOR-}" ]; then
		printf '\033[1;38;2;255;135;0m%s\033[0m\n' "$*" >&2
	else
		printf '%s\n' "$*" >&2
	fi
}
