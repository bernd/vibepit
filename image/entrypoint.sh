#!/bin/bash

set -e

if [ ! -f "$HOME/.vibepit-initialized" ]; then
	echo "+ Initializing $HOME"
	rsync -aHS "/home/.${CODE_USER}.template/" "/home/$CODE_USER/"
	date > "$HOME/.vibepit-initialized"
fi

echo "+ Welcome to the pit!"

exec /bin/bash --login
