package main

// guest holds the shell commands the tool runs in a VM, per guest OS.
type guest struct {
	gateway  string // prints the default gateway, which is the host
	hostname string // sets the hostname; %[1]s is the clone's name
	wgKey    string // writes the tunnel's private key in the VM and prints its public key
	wgLines  string // extra [Interface] lines: private key file and DNS
	wgUp     string // reads wg0.conf on stdin and brings the tunnel up now and after a reboot
	resolves string // succeeds when devops.mock.test resolves to the mock
	reaches  string // succeeds when the mock's port 8080 answers
	tunneled string // succeeds when internet traffic goes through the tunnel
	session  string // succeeds when the test user has a GUI session; empty when the OS has none
	update   string // upgrades the packages of the base
	poweroff string
}

var ubuntuGuest = guest{
	gateway:  "ip -4 route show default | awk '{print $3; exit}'",
	hostname: "sudo hostnamectl set-hostname %[1]s && sudo sed -i 's/^127\\.0\\.1\\.1 .*/127.0.1.1 %[1]s/' /etc/hosts",
	wgKey:    "sudo sh -c 'umask 077 && wg genkey >/etc/wireguard/private.key && wg pubkey </etc/wireguard/private.key'",
	wgLines: "PostUp = wg set %i private-key /etc/wireguard/private.key\n" +
		"PostUp = resolvectl dns %i " + routerAddress.String() + "; resolvectl domain %i '~.'\n",
	wgUp:     "sudo sh -c 'umask 077 && cat >/etc/wireguard/wg0.conf' && sudo systemctl enable --now wg-quick@wg0",
	resolves: "getent hosts devops.mock.test | grep -q '^172.30.53.10 '",
	reaches:  "timeout 5 bash -c '</dev/tcp/devops.mock.test/8080'",
	tunneled: "ip route get 1.1.1.1 | grep -q ' dev wg0 '",
	update:   "sudo DEBIAN_FRONTEND=noninteractive sh -c 'apt-get update && apt-get -y -o DPkg::Lock::Timeout=600 full-upgrade && apt-get -y autoremove'",
	poweroff: "sudo systemctl poweroff",
}

// macOS: wireguard-tools come from Homebrew; the base's launchd daemon
// com.tfsaz.live.wg0 runs wg-quick on boot once wg0.conf exists.
var macosGuest = guest{
	gateway:  "route -n get default | awk '/gateway:/{print $2; exit}'",
	hostname: "sudo scutil --set HostName %[1]s && sudo scutil --set LocalHostName %[1]s && sudo scutil --set ComputerName %[1]s",
	wgKey:    "sudo sh -c 'umask 077 && /opt/homebrew/bin/wg genkey >/etc/wireguard/private.key && /opt/homebrew/bin/wg pubkey </etc/wireguard/private.key'",
	wgLines: "PostUp = wg set %i private-key /etc/wireguard/private.key\n" +
		"DNS = " + routerAddress.String() + "\n",
	wgUp:     "sudo sh -c 'umask 077 && cat >/etc/wireguard/wg0.conf' && sudo launchctl kickstart -k system/com.tfsaz.live.wg0",
	resolves: "dscacheutil -q host -a name devops.mock.test | grep -q 'ip_address: 172.30.53.10'",
	reaches:  "nc -z -G 5 devops.mock.test 8080",
	tunneled: "route -n get 1.1.1.1 | grep -q 'interface: utun'",
	session:  "test \"$(stat -f %Su /dev/console)\" = \"$USER\"",
	update:   "eval \"$(/opt/homebrew/bin/brew shellenv)\" && brew update && brew upgrade",
	poweroff: "sudo shutdown -h now",
}
