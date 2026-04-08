#!/bin/bash

set -e

# shellcheck source=/dev/null
source /etc/vibepit/lib.sh
# shellcheck source=/dev/null
source /etc/vibepit/entrypoint-lib.sh

# Move legacy Homebrew installs from /home/code to /home/linuxbrew.
migrate_linuxbrew_volume

init_home

vp_status "Welcome to the pit!"
vp_status ""

exec /bin/bash --login
