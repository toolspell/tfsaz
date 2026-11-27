# Provisions the Windows base build after setup.ps1: WireGuard for the
# tunnel to the router, the tool's state directory, and no updates or sleep
# during tests. Packer runs it over SSH as tester, an administrator.
$ErrorActionPreference = 'Stop'
$ProgressPreference = 'SilentlyContinue'

# The hash pins the MSI; its Authenticode signature must name WireGuard LLC.
$msi = Join-Path $env:TEMP 'wireguard.msi'
Invoke-WebRequest -UseBasicParsing -OutFile $msi `
    -Uri 'https://download.wireguard.com/windows-client/wireguard-arm64-1.1.1.msi'
if ((Get-FileHash -Algorithm SHA256 $msi).Hash -ne '8336335c738d4aff040fa9d11b752da65a80b6f41d3262d6ba5e82f97ebd1bd4') {
    throw 'wireguard.msi does not match its pinned SHA-256'
}
$signature = Get-AuthenticodeSignature $msi
if ($signature.Status -ne 'Valid' -or $signature.SignerCertificate.Subject -notmatch 'O=WireGuard LLC') {
    throw "wireguard.msi is not signed by WireGuard LLC: $($signature.Status)"
}
$install = Start-Process msiexec.exe -Wait -PassThru -ArgumentList "/i `"$msi`" /qn DO_NOT_LAUNCH=1"
if ($install.ExitCode -ne 0) { throw "msiexec failed: $($install.ExitCode)" }
Remove-Item $msi

# The tool keeps the tunnel's key and config here; only administrators and
# SYSTEM may read them.
$state = 'C:\ProgramData\tfsaz-live'
New-Item -ItemType Directory -Force $state | Out-Null
icacls.exe $state /inheritance:r /grant '*S-1-5-32-544:(OI)(CI)F' /grant '*S-1-5-18:(OI)(CI)F'
if ($LASTEXITCODE -ne 0) { throw "icacls failed: $LASTEXITCODE" }

# A test must not meet an update restart or a sleeping VM. A rebuild from a
# newer ISO is the update.
$au = 'HKLM:\SOFTWARE\Policies\Microsoft\Windows\WindowsUpdate\AU'
New-Item -Force $au | Out-Null
Set-ItemProperty $au -Name NoAutoUpdate -Value 1 -Type DWord
powercfg.exe /change standby-timeout-ac 0
powercfg.exe /change monitor-timeout-ac 0
powercfg.exe /hibernate off
