#!/bin/bash

set -e

# shellcheck source=/dev/null
source /etc/vibepit/lib.sh

if [ ! -f "$HOME/.vibepit-initialized" ]; then
	vp_status "Initializing $HOME"
	rsync -aHS "/home/.${CODE_USER}.template/" "/home/$CODE_USER/"
	date > "$HOME/.vibepit-initialized"
fi

vp_status "Welcome to the pit!"
vp_status ""

exec /bin/bash --login
