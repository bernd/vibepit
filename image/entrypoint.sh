#!/bin/bash

set -e

# shellcheck source=/dev/null
source /etc/vibepit/lib.sh
# shellcheck source=/dev/null
source /etc/vibepit/entrypoint-lib.sh

# Migrate old-style volume layout.
# Old volumes were mounted at /home/code, so their root contains user files
# (.bashrc, .vibepit-initialized, etc.) directly. Now the volume is mounted at
# /home, so those files appear at /home/ and need to move into /home/code/.
migrate_home_volume /home

if [ ! -f "$HOME/.vibepit-initialized" ]; then
	vp_status "Initializing $HOME"
	rsync -aHS "/opt/vibepit/home-template/" "$HOME/"
	date > "$HOME/.vibepit-initialized"
fi

vp_status "Welcome to the pit!"
vp_status ""

exec /bin/bash --login
