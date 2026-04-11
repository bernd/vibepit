#!/bin/bash
# vibed-init.sh — sandbox initialization called by vibed before accepting
# SSH sessions. Runs the same home-directory and linuxbrew setup as
# entrypoint.sh so the environment is ready regardless of entry path.

set -e

# shellcheck source=/dev/null
source /etc/vibepit/lib.sh
# shellcheck source=/dev/null
source /etc/vibepit/entrypoint-lib.sh

# Move legacy Homebrew installs from /home/code to /home/linuxbrew.
migrate_linuxbrew_volume

init_home
