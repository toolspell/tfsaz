# Last step of the Windows base build, Packer's shutdown command: makes the
# image safe to clone, then shuts it down. sshd creates missing host keys
# when it starts, so every clone gets its own; the tool sets the computer
# name and the login key of each clone.
$ErrorActionPreference = 'Stop'
Remove-Item -Force C:\ProgramData\ssh\ssh_host_*
# Commands over SSH run in PowerShell 7, which agents know best. TFSaz must
# not rely on it: users may have only Windows PowerShell 5.1 and cmd.
New-ItemProperty -Force -Path HKLM:\SOFTWARE\OpenSSH -Name DefaultShell `
    -Value 'C:\Program Files\PowerShell\7\pwsh.exe' | Out-Null
Remove-Item -Force $PSCommandPath
shutdown.exe /s /t 5 /f
