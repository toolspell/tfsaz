# First logon of the Windows base build, started by Autounattend.xml from
# the seed ISO: the network driver and OpenSSH Server with the build key.
# Packer connects once sshd runs and does the rest. `base build windows`
# fills in the build key.
$ErrorActionPreference = 'Stop'
$ProgressPreference = 'SilentlyContinue'
Start-Transcript -Path C:\Windows\Temp\tfsaz-setup.log

# Windows has no inbox driver for QEMU's virtio network card; the virtio-win
# ISO on another drive has it.
foreach ($driver in 'NetKVM\w11\ARM64\netkvm.inf') {
    $inf = Get-PSDrive -PSProvider FileSystem |
        ForEach-Object { Join-Path $_.Root $driver } |
        Where-Object { Test-Path $_ } |
        Select-Object -First 1
    if (-not $inf) { throw "no drive has $driver" }
    pnputil.exe /add-driver $inf /install
    if ($LASTEXITCODE -notin 0, 3010) { throw "pnputil failed for ${driver}: $LASTEXITCODE" }
}

# OpenSSH Server comes from Windows Update, so retry until the network is up.
$name = (Get-WindowsCapability -Online -Name 'OpenSSH.Server*').Name
for ($try = 1; (Get-WindowsCapability -Online -Name $name).State -ne 'Installed'; $try++) {
    try {
        Add-WindowsCapability -Online -Name $name | Out-Null
    } catch {
        if ($try -ge 20) { throw }
        Start-Sleep -Seconds 30
    }
}

# sshd writes its config and host keys at its first start. Administrators
# log in with the keys in administrators_authorized_keys only.
Set-Service sshd -StartupType Automatic
Start-Service sshd
Stop-Service sshd
$config = 'C:\ProgramData\ssh\sshd_config'
(Get-Content $config) -replace '^#?PasswordAuthentication .*', 'PasswordAuthentication no' |
    Set-Content -Encoding ascii $config
$keys = 'C:\ProgramData\ssh\administrators_authorized_keys'
Set-Content -Encoding ascii -Path $keys -Value '${ssh_public_key}'
icacls.exe $keys /inheritance:r /grant '*S-1-5-32-544:F' /grant '*S-1-5-18:F'
if ($LASTEXITCODE -ne 0) { throw "icacls failed: $LASTEXITCODE" }
New-NetFirewallRule -Name tfsaz-live-ssh -DisplayName 'SSH for TFSaz live tests' `
    -Protocol TCP -LocalPort 22 -Action Allow -Profile Any | Out-Null
Start-Service sshd
