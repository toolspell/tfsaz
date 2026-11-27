#!/bin/sh
# Provisions the Ubuntu base build after the installer: the test user's
# desktop session as on a developer's machine. Packer runs it with sudo.
set -eu

# The first boot can start apt-daily before Packer takes the APT lists lock.
systemctl disable --now apt-daily.timer apt-daily-upgrade.timer
while systemctl show -p ActiveState --value apt-daily.service apt-daily-upgrade.service | grep -Eq '^(activating|active|deactivating)$'; do
    sleep 5
done

# The desktop: live tests run T3, browsers and link handlers as a user
# would. It goes onto the booted system, where snapd runs; in the
# installer's chroot, the snaps it brings, such as Firefox, never finish.
export DEBIAN_FRONTEND=noninteractive
apt-get update
apt-get -y -o DPkg::Lock::Timeout=600 install ubuntu-desktop-minimal
systemctl set-default graphical.target
# The desktop hands the network to NetworkManager; networkd's wait for its
# own links would hold every boot for its full two-minute timeout.
systemctl disable systemd-networkd-wait-online.service
# A welcome wizard in front of the desktop is in the way of every test.
apt-get -y -o DPkg::Lock::Timeout=600 purge gnome-initial-setup
# Test tooling: secret-tool reads and writes Secret Service entries.
apt-get -y -o DPkg::Lock::Timeout=600 install libsecret-tools

apt-get -y -o DPkg::Lock::Timeout=600 install gpg
chrome_key=$(mktemp)
trap 'rm -f "$chrome_key"' EXIT
curl -fsSL https://dl.google.com/linux/linux_signing_key.pub -o "$chrome_key"
keys=$(gpg --batch --show-keys --with-colons "$chrome_key")
fingerprint=$(printf '%s\n' "$keys" | awk -F: '$1 == "pub" { primary=1 } $1 == "fpr" && primary { print $10; primary=0 }')
if [ "$fingerprint" != EB4C1BFD4F042F6DDDCCEC917721F63BD38B4796 ]; then
    echo 'Unexpected Google Linux package signing key' >&2
    exit 1
fi
install -d -m 755 /etc/apt/keyrings
install -m 644 "$chrome_key" /etc/apt/keyrings/google-chrome.asc
# Keep the scoped repository when Chrome's package tries to add its own.
printf 'repo_add_once="false"\nrepo_reenable_on_distupgrade="false"\n' >/etc/default/google-chrome
printf 'deb [signed-by=/etc/apt/keyrings/google-chrome.asc] https://dl.google.com/linux/chrome/deb/ stable main\n' >/etc/apt/sources.list.d/google-chrome.list
apt-get update
apt-get -y -o DPkg::Lock::Timeout=600 install google-chrome-stable

# Log tester in to GNOME at boot.
cat >/etc/gdm3/custom.conf <<'EOF'
[daemon]
AutomaticLoginEnable=true
AutomaticLogin=tester
EOF

# A user who logs in with a password finds the login keyring unlocked;
# without a password at login, a keyring without one stays unlocked too.
# Otherwise Secret Service asks for a password on first use.
keyrings=/home/tester/.local/share/keyrings
mkdir -p "$keyrings"
cat >"$keyrings/login.keyring" <<'EOF'
[keyring]
display-name=Login
ctime=0
mtime=0
lock-on-idle=false
lock-after=false
EOF
echo login >"$keyrings/default"
chmod 700 "$keyrings"
chmod 600 "$keyrings/login.keyring" "$keyrings/default"

# No screen lock or blank screen in the middle of a test.
chown -R tester:tester /home/tester/.local
mkdir -p /etc/dconf/profile /etc/dconf/db/local.d
printf 'user-db:user\nsystem-db:local\n' >/etc/dconf/profile/user
cat >/etc/dconf/db/local.d/00-tfsaz-live <<'EOF'
[org/gnome/desktop/session]
idle-delay=uint32 0

[org/gnome/desktop/screensaver]
lock-enabled=false

[org/gnome/settings-daemon/plugins/power]
sleep-inactive-ac-type='nothing'
EOF
dconf update
