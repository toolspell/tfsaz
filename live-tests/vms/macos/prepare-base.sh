#!/bin/sh
# Last step of base build and base update: make the image safe to clone.
# macOS's sshd creates missing host keys when it starts, so every clone gets
# its own; Tart gives every clone a new MAC address. The tool sets the
# hostname and the login key of each clone.
set -eu

rm -f /etc/ssh/ssh_host_*
sync
