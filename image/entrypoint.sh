#!/bin/bash

set -e

if [ ! -f "$HOME/.home-dir-initialized" ]; then
	echo "+ Initializing $HOME"
	rsync -aHS "/home/.${CODE_USER}.template/" "/home/$CODE_USER/"
	date > "$HOME/.home-dir-initialized"
fi

export HOME="/home/$CODE_USER"
export NVM_DIR="$HOME/.nvm"

paths=()
paths+=("$HOME/.deno/bin")
paths+=("$HOME/.bun/bin")
paths+=("$HOME/.local/node_modules/.bin")

for path in ${paths[*]}; do
	if [ -d "$path" ]; then
		export PATH="$path:$PATH"
	fi
done

exec /bin/bash --login
