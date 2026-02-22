#!/bin/bash

set -e

# shellcheck source=/dev/null
source /etc/vibepit/lib.sh
# shellcheck source=/dev/null
source /etc/vibepit/entrypoint-lib.sh

# Move legacy Homebrew installs from /home/code to /home/linuxbrew.
migrate_linuxbrew_volume

if [ ! -f "$HOME/.vibepit-initialized" ]; then
	vp_status "Initializing $HOME"
	rsync -aHS "/opt/vibepit/home-template/" "$HOME/"
	date > "$HOME/.vibepit-initialized"
fi

vp_status "Welcome to the pit!"
vp_status ""

exec /bin/bash --login
