# Last step of the Windows base build, Packer's shutdown command: makes the
# image safe to clone, then shuts it down. sshd creates missing host keys
# when it starts, so every clone gets its own; the tool sets the computer
# name and the login key of each clone.
$ErrorActionPreference = 'Stop'
Remove-Item -Force C:\ProgramData\ssh\ssh_host_*
# Commands over SSH run in PowerShell, as in a terminal on Windows.
New-ItemProperty -Force -Path HKLM:\SOFTWARE\OpenSSH -Name DefaultShell `
    -Value "$env:SystemRoot\System32\WindowsPowerShell\v1.0\powershell.exe" | Out-Null
Remove-Item -Force $PSCommandPath
shutdown.exe /s /t 5 /f
