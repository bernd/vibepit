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
paths+=("$HOME/go/bin")

for path in "${paths[@]}"; do
	export PATH="$path:$PATH"
done

echo "+ Welcome to the pit!"

exec /bin/bash --login
