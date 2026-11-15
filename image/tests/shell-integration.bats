#!/usr/bin/env bats

setup() {
	export INTEGRATION="$BATS_TEST_DIRNAME/../config/shell-integration.bash"
}

@test "interactive Bash reports command boundaries and directory changes through a PTY" {
	run python3 "$BATS_TEST_DIRNAME/shell-integration-pty.py"
	[ "$status" -eq 0 ]
}

@test "shell reporting is opt-in and interactive only" {
	run bash --noprofile --norc -c 'export VIBEPIT_SHELL_INTEGRATION=1; source "$INTEGRATION"; declare -F __vibepit_shell_prompt'
	[ "$status" -ne 0 ]
	run env -u VIBEPIT_SHELL_INTEGRATION bash --noprofile --norc -ic 'source "$INTEGRATION"; declare -F __vibepit_shell_prompt'
	[ "$status" -ne 0 ]
}

@test "reports preserve status and encode spaces unicode and control bytes in paths" {
	run env VIBEPIT_SHELL_INTEGRATION=1 TERM=xterm-256color bash --noprofile --norc -ic '
		source "$INTEGRATION"
		PWD=$'"'"'/tmp/a b/世界/%\e\n'"'"'; HOSTNAME=sandbox
		false; __vibepit_shell_prompt; result=$?
		printf "\nstatus=%s\n" "$result"
	'
	[ "$status" -eq 0 ]
	[[ "$output" == *$'\e]133;D;1\a'* ]]
	[[ "$output" == *$'\e]7;file://sandbox/tmp/a%20b/%E4%B8%96%E7%95%8C/%25%1B%0A\a'* ]]
	[[ "$output" == *$'\e]133;A\a'* ]]
	[[ "$output" == *'status=1'* ]]
}

@test "source twice preserves existing hooks prompts and DEBUG traps" {
	run env VIBEPIT_SHELL_INTEGRATION=1 TERM=xterm-256color bash --noprofile --norc -ic '
		PS0=before; PS1="custom> "
		PROMPT_COMMAND=("printf existing" "true")
		trap : DEBUG
		before=$(trap -p DEBUG)
		source "$INTEGRATION"; source "$INTEGRATION"
		[[ $(trap -p DEBUG) == "$before" ]] || exit 1
		[[ ${#PROMPT_COMMAND[@]} == 3 && ${PROMPT_COMMAND[0]} == __vibepit_shell_prompt && ${PROMPT_COMMAND[1]} == "printf existing" ]] || exit 1
		[[ $PS0 == *before && $PS1 == "custom> "* ]] || exit 1
		[[ $PS0 == *"133;C"* && $PS1 == *"133;B"* ]] || exit 1
	'
	[ "$status" -eq 0 ]
}

@test "existing scalar PROMPT_COMMAND still observes the previous exit status" {
	run env VIBEPIT_SHELL_INTEGRATION=1 TERM=xterm-256color bash --noprofile --norc -ic '
		PROMPT_COMMAND='"'"'printf "prior=%s\n" "$?"'"'"'
		source "$INTEGRATION"
		false; __vibepit_shell_prompt; eval "${PROMPT_COMMAND[1]}"
	'
	[ "$status" -eq 0 ]
	[[ "$output" == *'prior=1'* ]]
}
