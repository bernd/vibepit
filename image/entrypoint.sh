#!/bin/bash

set -e

if [ ! -f "$HOME/.vibepit-initialized" ]; then
	echo "+ Initializing $HOME"
	rsync -aHS "/home/.${CODE_USER}.template/" "/home/$CODE_USER/"
	date > "$HOME/.vibepit-initialized"
fi

export HOME="/home/$CODE_USER"

paths=()
paths+=("$HOME/.local/node_modules/.bin")
paths+=("$HOME/.deno/bin")
paths+=("$HOME/.bun/bin")
paths+=("$HOME/.local/bin")

for path in ${paths[*]}; do
	export PATH="$path:$PATH"
done

exec /bin/bash --login
