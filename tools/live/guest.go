package main

// guest holds the commands the tool runs in a VM, per guest OS: sh for
// Ubuntu and macOS, PowerShell 7 for Windows.
type guest struct {
	powershell bool
	authorize  string // reads a public key on stdin and makes it the only login key
	hostname   string // sets the hostname; %[1]s is the clone's name
	restart    string // restarts the VM; empty when the hostname applies without one
	bootID     string // prints a value that changes with every boot, to confirm a restart
	gateway    string // prints the default gateway, which is the host
	wgKey      string // writes the tunnel's private key in the VM and prints its public key
	wgLines    string // extra [Interface] lines: private key and DNS
	allowedIPs string // what the tunnel routes: all of IPv4
	wgUp       string // reads wg0.conf on stdin and brings the tunnel up now and after a reboot
	resolves   string // succeeds when devops.mock.test resolves to the mock
	reaches    string // succeeds when the mock's port 8080 answers
	rest       string // succeeds when the mock's REST API accepts the PAT %[1]s
	tunneled   string // succeeds when internet traffic goes through the tunnel
	internet   string // succeeds when the VM reaches the internet
	session    string // succeeds when the test user has a GUI session; empty when the OS has none
	gui        string // runs %s, a quoted sh command, in the desktop session; empty when there is none or for PowerShell
	tempDir    string // where vm t3 copies the installer, with a trailing slash
	t3Install  string // installs the T3 Code desktop app from the installer %[1]s and deletes it
	sync       string // flushes the disk cache
	update     string // upgrades the packages of the base; empty when a rebuild is the update
	poweroff   string
}

const connectionData = "http://devops.mock.test:8080/DefaultCollection/_apis/connectionData?api-version=5.0-preview.1"

// posix holds the commands Ubuntu and macOS share.
var posix = guest{
	authorize:  "umask 077 && cat >~/.ssh/authorized_keys",
	allowedIPs: "0.0.0.0/0",
	rest:       `test "$(curl -s -u :%[1]s -o /dev/null -w '%%{http_code}' '` + connectionData + `')" = 200`,
	internet:   "curl -s -o /dev/null --max-time 15 https://github.com",
	sync:       "sync",
}

var ubuntuGuest = guest{
	authorize:  posix.authorize,
	allowedIPs: posix.allowedIPs,
	rest:       posix.rest,
	internet:   posix.internet,
	sync:       posix.sync,
	gateway:    "ip -4 route show default | awk '{print $3; exit}'",
	hostname:   "sudo hostnamectl set-hostname %[1]s && sudo sed -i 's/^127\\.0\\.1\\.1 .*/127.0.1.1 %[1]s/' /etc/hosts",
	wgKey:      "sudo sh -c 'umask 077 && wg genkey >/etc/wireguard/private.key && wg pubkey </etc/wireguard/private.key'",
	wgLines: "PostUp = wg set %i private-key /etc/wireguard/private.key\n" +
		"PostUp = resolvectl dns %i " + routerAddress.String() + "; resolvectl domain %i '~.'\n",
	wgUp:     "sudo sh -c 'umask 077 && cat >/etc/wireguard/wg0.conf' && sudo systemctl enable --now wg-quick@wg0",
	resolves: "getent hosts devops.mock.test | grep -q '^172.30.53.10 '",
	// gnome-shell runs before its compositor accepts clients; connect to
	// its Wayland socket to see that it does. -gui takes the display from
	// the user's systemd manager, which GNOME tells once it is up.
	session: "pgrep -u \"$USER\" -x gnome-shell >/dev/null && python3 -c 'import os, socket; socket.socket(socket.AF_UNIX).connect(\"/run/user/%d/wayland-0\" % os.getuid())' && systemctl --user show-environment | grep -q '^WAYLAND_DISPLAY='",
	// GNOME hands its session's environment to the user's systemd manager.
	// An SSH session lacks the display, the desktop's name and the snap
	// directory in XDG_DATA_DIRS, without which links open in no browser.
	gui:       `bash -c 'set -a; eval "$(systemctl --user show-environment)"; set +a; exec sh -c "$1"' tfsaz %s`,
	tempDir:   "/tmp/",
	t3Install: "sudo DEBIAN_FRONTEND=noninteractive apt-get install -y -o DPkg::Lock::Timeout=600 %[1]s && rm -f %[1]s",
	reaches:   "timeout 5 bash -c '</dev/tcp/devops.mock.test/8080'",
	tunneled:  "ip route get 1.1.1.1 | grep -q ' dev wg0 '",
	update:    "sudo DEBIAN_FRONTEND=noninteractive sh -c 'apt-get update && apt-get -y -o DPkg::Lock::Timeout=600 full-upgrade && apt-get -y autoremove'",
	poweroff:  "sudo systemctl poweroff",
}

// macOS: wireguard-tools come from Homebrew; the base's launchd daemon
// com.tfsaz.live.wg0 runs wg-quick on boot once wg0.conf exists.
var macosGuest = guest{
	authorize:  posix.authorize,
	allowedIPs: posix.allowedIPs,
	rest:       posix.rest,
	internet:   posix.internet,
	sync:       posix.sync,
	gateway:    "route -n get default | awk '/gateway:/{print $2; exit}'",
	hostname:   "sudo scutil --set HostName %[1]s && sudo scutil --set LocalHostName %[1]s && sudo scutil --set ComputerName %[1]s",
	wgKey:      "sudo sh -c 'umask 077 && /opt/homebrew/bin/wg genkey >/etc/wireguard/private.key && /opt/homebrew/bin/wg pubkey </etc/wireguard/private.key'",
	wgLines: "PostUp = wg set %i private-key /etc/wireguard/private.key\n" +
		"DNS = " + routerAddress.String() + "\n",
	wgUp:     "sudo sh -c 'umask 077 && cat >/etc/wireguard/wg0.conf' && sudo launchctl kickstart -k system/com.tfsaz.live.wg0",
	resolves: "dscacheutil -q host -a name devops.mock.test | grep -q 'ip_address: 172.30.53.10'",
	reaches:  "nc -z -G 5 devops.mock.test 8080",
	tunneled: "route -n get 1.1.1.1 | grep -q 'interface: utun'",
	session:  "test \"$(stat -f %Su /dev/console)\" = \"$USER\"",
	// launchctl asuser enters the user's GUI session, where the login
	// keychain is unlocked and apps open on the user's screen.
	gui:       `sudo launchctl asuser "$(id -u)" sudo -u "$(id -un)" sh -c %s`,
	tempDir:   "/tmp/",
	t3Install: "ditto -x -k %[1]s /Applications && rm -f %[1]s",
	update:    "eval \"$(/opt/homebrew/bin/brew shellenv)\" && brew update && brew upgrade",
	poweroff:  "sudo shutdown -h now",
}

// Windows: the base has WireGuard for Windows and C:\ProgramData\tfsaz-live,
// writable only by administrators and SYSTEM. Its tunnel service reads the
// private key from wg0.conf, and AllowedIPs avoids 0.0.0.0/0, which turns on
// WireGuard's kill switch and cuts off SSH through QEMU's NAT. Without the
// kill switch Windows also asks the NAT's DNS server, so the network card
// gets the router's resolver too.
var windowsGuest = guest{
	powershell: true,
	authorize:  `[Console]::In.ReadToEnd() | Set-Content -Encoding ascii C:\ProgramData\ssh\administrators_authorized_keys`,
	hostname:   `Rename-Computer -NewName '%[1]s' -Force -WarningAction SilentlyContinue`,
	restart:    "Restart-Computer -Force",
	bootID:     "(Get-CimInstance Win32_OperatingSystem).LastBootUpTime.ToFileTimeUtc()",
	gateway:    "(Get-NetRoute -DestinationPrefix 0.0.0.0/0 | Sort-Object RouteMetric | Select-Object -First 1).NextHop",
	wgKey: `$key = & 'C:\Program Files\WireGuard\wg.exe' genkey
Set-Content -NoNewline -Encoding ascii C:\ProgramData\tfsaz-live\private.key $key
$key | & 'C:\Program Files\WireGuard\wg.exe' pubkey`,
	wgLines:    "PrivateKey = __PRIVATE_KEY__\nDNS = " + routerAddress.String() + "\n",
	allowedIPs: "0.0.0.0/1, 128.0.0.0/1",
	wgUp: `$key = (Get-Content -Raw C:\ProgramData\tfsaz-live\private.key).Trim()
[Console]::In.ReadToEnd().Replace('__PRIVATE_KEY__', $key) | Set-Content -Encoding ascii C:\ProgramData\tfsaz-live\wg0.conf
Start-Process -Wait 'C:\Program Files\WireGuard\wireguard.exe' '/installtunnelservice C:\ProgramData\tfsaz-live\wg0.conf'
(Get-Service 'WireGuardTunnel$wg0').WaitForStatus('Running', '00:00:30')
Get-NetAdapter | Where-Object Name -ne 'wg0' | Set-DnsClientServerAddress -ServerAddresses ` + routerAddress.String(),
	resolves: "if ((Resolve-DnsName -Name devops.mock.test -Type A -DnsOnly).IPAddress -contains '172.30.53.10') { exit 0 }; exit 1",
	reaches:  "if ((New-Object Net.Sockets.TcpClient).ConnectAsync('devops.mock.test', 8080).Wait(5000)) { exit 0 }; exit 1",
	rest:     "if ((curl.exe -s -u ':%[1]s' -o NUL -w '%%{http_code}' '" + connectionData + "') -eq '200') { exit 0 }; exit 1",
	tunneled: "if ((Find-NetRoute -RemoteIPAddress 1.1.1.1).InterfaceAlias -contains 'wg0') { exit 0 }; exit 1",
	internet: "curl.exe -s -o NUL --max-time 15 https://github.com; exit $LASTEXITCODE",
	session:  `if ((Get-CimInstance Win32_ComputerSystem).UserName -like "*\$env:USERNAME") { exit 0 }; exit 1`,
	sync:     "Write-VolumeCache -DriveLetter C",
	tempDir:  "C:/Windows/Temp/",
	// The NSIS installer installs for the user that runs it, as a download
	// and double-click would.
	// Defender can hold the installer for a moment after it exits.
	t3Install: "$p = Start-Process -Wait -PassThru -FilePath '%[1]s' -ArgumentList '/S'\n" +
		"for ($try = 1; ; $try++) { try { Remove-Item '%[1]s'; break } catch { if ($try -ge 30) { throw }; Start-Sleep 1 } }\n" +
		"exit $p.ExitCode",
	poweroff: "shutdown /s /t 0 /f",
}
