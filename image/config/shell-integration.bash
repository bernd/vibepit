# shellcheck shell=bash

# Opt in only for interactive desktop sessions. Sourcing twice is harmless.
[[ $- == *i* && ${VIBEPIT_SHELL_INTEGRATION:-} == 1 && ${TERM:-dumb} != dumb ]] || return 0
[[ ${__vibepit_shell_integration:-} != 1 ]] || return 0
__vibepit_shell_integration=1

__vibepit_shell_prompt() {
	local status=$?
	local LC_ALL=C encoded='' char hex i
	local hostname=${HOSTNAME//[^a-zA-Z0-9.-]/}
	# Encode UTF-8 byte by byte so spaces and control bytes cannot end the OSC.
	for ((i = 0; i < ${#PWD}; i++)); do
		char=${PWD:i:1}
		case "$char" in
		[a-zA-Z0-9/._~-]) encoded+=$char ;;
		*) printf -v hex '%%%02X' "'$char"; encoded+=$hex ;;
		esac
	done
	printf '\033]133;D;%s\007\033]7;file://%s%s\007\033]133;A\007' "$status" "${hostname:-localhost}" "$encoded"
	# Existing PROMPT_COMMAND hooks must still see the command's original status.
	return "$status"
}

# PS0 runs once per entered command, including compound commands; no DEBUG trap.
PS0='\e]133;C\a'${PS0-}
PS1=${PS1-}'\[\e]133;B\a\]'
PROMPT_COMMAND=(__vibepit_shell_prompt "${PROMPT_COMMAND[@]}")
