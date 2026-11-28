#!/bin/sh
# Last step of the base build: make the image safe to clone. Every clone gets
# a new MAC address from Tart; these steps give it its own machine ID, SSH
# host keys and DHCP lease too. The tool sets the hostname and the login key
# of each clone.
set -eu

# The tool configures clones over SSH; cloud-init has done its one job.
touch /etc/cloud/cloud-init.disabled

# Clones must not update themselves during tests; base update does that.
# An update started on this first boot also blocks shutdown, so wait for it.
systemctl disable --now apt-daily.timer apt-daily-upgrade.timer
while systemctl is-active --quiet apt-daily.service apt-daily-upgrade.service; do
	sleep 5
done

# DHCP on any wired interface, whatever its name or MAC address. The MAC
# address as DHCP client ID lets Tart find the clone's address.
rm -f /etc/netplan/*.yaml
cat >/etc/netplan/01-dhcp.yaml <<'EOF'
network:
  version: 2
  ethernets:
    wired:
      match:
        name: "en*"
      dhcp4: true
      dhcp-identifier: mac
EOF
chmod 600 /etc/netplan/01-dhcp.yaml

# New SSH host keys on the first boot of each clone, before sshd starts.
cat >/etc/systemd/system/ssh-host-keys.service <<'EOF'
[Unit]
Description=Generate the SSH host keys of a cloned VM
DefaultDependencies=no
After=local-fs.target
Before=ssh.socket ssh.service
ConditionPathExists=!/etc/ssh/ssh_host_ed25519_key

[Service]
Type=oneshot
ExecStart=/usr/bin/ssh-keygen -A

[Install]
WantedBy=sockets.target
EOF
systemctl enable ssh-host-keys.service
rm -f /etc/ssh/ssh_host_*

# systemd creates a new machine ID on first boot; DHCP client IDs derive from it.
truncate -s 0 /etc/machine-id
rm -f /var/lib/dbus/machine-id

sync
