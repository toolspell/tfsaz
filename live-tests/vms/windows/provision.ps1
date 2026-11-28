# Provisions the Windows base build after setup.ps1: PowerShell 7 for the
# tool and SSH sessions, WireGuard for the tunnel to the router, the tool's
# state directory, and no updates or sleep during tests. Packer runs it over
# SSH as tester, an administrator.
$ErrorActionPreference = 'Stop'
$ProgressPreference = 'SilentlyContinue'

# Microsoft ships PowerShell 7 for Windows ARM64 only as a zip. The hash
# pins it; Authenticode must name Microsoft.
$zip = Join-Path $env:TEMP 'pwsh.zip'
Invoke-WebRequest -UseBasicParsing -OutFile $zip `
    -Uri 'https://github.com/PowerShell/PowerShell/releases/download/v7.7.0/PowerShell-7.7.0-win-arm64.zip'
if ((Get-FileHash -Algorithm SHA256 $zip).Hash -ne '5dc7440cd98f0ba786a44d7db9205df5058474fd4db143caf7171e8375f34f75') {
    throw 'PowerShell zip does not match its pinned SHA-256'
}
$pwshDir = 'C:\Program Files\PowerShell\7'
Expand-Archive -Force $zip $pwshDir
Remove-Item $zip
$signature = Get-AuthenticodeSignature (Join-Path $pwshDir 'pwsh.exe')
if ($signature.Status -ne 'Valid' -or $signature.SignerCertificate.Subject -notmatch 'O=Microsoft Corporation') {
    throw "pwsh.exe is not signed by Microsoft: $($signature.Status)"
}
$path = [Environment]::GetEnvironmentVariable('Path', 'Machine')
if ($path -notlike "*$pwshDir*") {
    [Environment]::SetEnvironmentVariable('Path', "$path;$pwshDir", 'Machine')
}

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

$explorer = 'HKLM:\SOFTWARE\Policies\Microsoft\Windows\Explorer'
New-Item -Force $explorer | Out-Null
Set-ItemProperty $explorer -Name NoNewAppAlert -Value 1 -Type DWord

$msi = Join-Path $env:TEMP 'google-chrome.msi'
Invoke-WebRequest -UseBasicParsing -OutFile $msi `
    -Uri 'https://dl.google.com/dl/chrome/install/googlechromestandaloneenterprise_arm64.msi'
$signature = Get-AuthenticodeSignature $msi
if ($signature.Status -ne 'Valid' -or $signature.SignerCertificate.Subject -notmatch '(?:^|,\s*)O=Google LLC(?:,|$)') {
    throw "Chrome MSI is not signed by Google LLC: $($signature.Status)"
}
$install = Start-Process msiexec.exe -Wait -PassThru -ArgumentList "/i `"$msi`" /qn /norestart"
if ($install.ExitCode -ne 0) { throw "Chrome installation failed: $($install.ExitCode)" }
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
