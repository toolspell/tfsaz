#!/bin/bash
# Turns Cirrus Labs' vanilla macOS image into the live-test base: SSH with
# the build key only, Homebrew with wireguard-tools, the tunnel's launchd
# daemon, no automatic updates. Runs once per build as admin; the image
# already logs admin in automatically and allows passwordless sudo.
set -euo pipefail

mkdir -p ~/.ssh
chmod 700 ~/.ssh
printf '%s\n' "$SSH_PUBLIC_KEY" >~/.ssh/authorized_keys
chmod 600 ~/.ssh/authorized_keys
# The image's password is public; keep it off SSH.
sudo tee /etc/ssh/sshd_config.d/100-tfsaz-live.conf >/dev/null <<'EOF'
PasswordAuthentication no
KbdInteractiveAuthentication no
EOF

# With NONINTERACTIVE set, the installer also installs the Command Line Tools.
NONINTERACTIVE=1 /bin/bash -c "$(curl -fsSL https://raw.githubusercontent.com/Homebrew/install/HEAD/install.sh)"
eval "$(/opt/homebrew/bin/brew shellenv)"
echo 'eval "$(/opt/homebrew/bin/brew shellenv)"' >>~/.zprofile
brew install wireguard-tools
brew install --cask google-chrome
chrome='/Applications/Google Chrome.app'
# Strict verification rejects Finder metadata and resource forks in the cask's app.
xattr -rd com.apple.FinderInfo "$chrome"
xattr -rd com.apple.ResourceFork "$chrome"
codesign --verify --deep --strict -R='anchor apple generic and certificate leaf[subject.OU] = "EQHXZ8M8AV"' "$chrome"
signature=$(codesign -dv "$chrome" 2>&1)
grep -qx 'TeamIdentifier=EQHXZ8M8AV' <<<"$signature"
# Homebrew's quarantine prompts on first launch, even in this VM.
xattr -rd com.apple.quarantine "$chrome"

# The tunnel: wg-quick on every boot once the tool has written wg0.conf.
# It waits for the default route, which the tunnel's endpoint needs.
sudo mkdir -p /etc/wireguard
sudo chmod 700 /etc/wireguard
sudo tee /Library/LaunchDaemons/com.tfsaz.live.wg0.plist >/dev/null <<'EOF'
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>com.tfsaz.live.wg0</string>
	<key>ProgramArguments</key>
	<array>
		<string>/bin/sh</string>
		<string>-c</string>
		<string>[ -f /etc/wireguard/wg0.conf ] || exit 0; until route -n get default >/dev/null 2>&amp;1; do sleep 1; done; wg-quick down wg0 2>/dev/null; exec wg-quick up wg0</string>
	</array>
	<key>EnvironmentVariables</key>
	<dict>
		<key>PATH</key>
		<string>/opt/homebrew/bin:/usr/bin:/bin:/usr/sbin:/sbin</string>
	</dict>
	<key>RunAtLoad</key>
	<true/>
	<key>StandardOutPath</key>
	<string>/var/log/tfsaz-live-wg0.log</string>
	<key>StandardErrorPath</key>
	<string>/var/log/tfsaz-live-wg0.log</string>
</dict>
</plist>
EOF
sudo launchctl bootstrap system /Library/LaunchDaemons/com.tfsaz.live.wg0.plist

# Clones must not update themselves during tests; base update upgrades
# Homebrew, and a new macOS comes with a new image pin.
sudo softwareupdate --schedule off >/dev/null 2>&1 || true
sudo defaults write /Library/Preferences/com.apple.SoftwareUpdate AutomaticDownload -bool false
sudo defaults write /Library/Preferences/com.apple.SoftwareUpdate AutomaticallyInstallMacOSUpdates -bool false
sudo defaults write /Library/Preferences/com.apple.SoftwareUpdate CriticalUpdateInstall -bool false
sudo defaults write /Library/Preferences/com.apple.commerce AutoUpdate -bool false
