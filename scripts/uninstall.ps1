# UnitRise Gate Bridge - Windows uninstaller.
#
#   powershell -ExecutionPolicy Bypass -File .\uninstall.ps1 [-PurgeConfig]
#
# Stops and removes the service and the Program Files install. The pairing
# (config + roster under %ProgramData%\UnitRiseGateBridge) is KEPT unless you
# pass -PurgeConfig, so a reinstall picks up where it left off. The last gate
# file written into the gate software's folder is never touched - the gate
# keeps admitting from its current list.

param([switch]$PurgeConfig)

$ErrorActionPreference = "Stop"

$identity = [Security.Principal.WindowsIdentity]::GetCurrent()
$principal = New-Object Security.Principal.WindowsPrincipal($identity)
if (-not $principal.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)) {
    Write-Host "ERROR: run this from an Administrator PowerShell." -ForegroundColor Red
    exit 1
}

$dest = Join-Path $env:ProgramFiles "UnitRise Gate Bridge"
$exe  = Join-Path $dest "unitrise-gate.exe"

# Tray first: it holds its exe open, which would block removing Program Files.
Get-Process -Name "unitrise-gate-tray" -ErrorAction SilentlyContinue | Stop-Process -Force -ErrorAction SilentlyContinue
Remove-ItemProperty -Path "HKLM:\SOFTWARE\Microsoft\Windows\CurrentVersion\Run" -Name "UnitRiseGateBridgeTray" -ErrorAction SilentlyContinue

$svc = Get-Service -Name "UnitRiseGateBridge" -ErrorAction SilentlyContinue
if ($svc) {
    if ($svc.Status -eq "Running") { Stop-Service -Name "UnitRiseGateBridge" -Force }
    if (Test-Path $exe) { & $exe service uninstall } else { sc.exe delete UnitRiseGateBridge | Out-Null }
    Write-Host "Service removed."
}

if (Test-Path $dest) {
    Remove-Item -Recurse -Force $dest
    Write-Host "Removed $dest"
}

$machinePath = [Environment]::GetEnvironmentVariable("Path", "Machine")
if ($machinePath -like "*$dest*") {
    [Environment]::SetEnvironmentVariable("Path", ($machinePath -replace [Regex]::Escape(";$dest"), ""), "Machine")
}

if ($PurgeConfig) {
    $data = Join-Path $env:ProgramData "UnitRiseGateBridge"
    if (Test-Path $data) {
        Remove-Item -Recurse -Force $data
        Write-Host "Removed pairing + logs ($data)"
    }
} else {
    Write-Host "Pairing kept in $env:ProgramData\UnitRiseGateBridge (pass -PurgeConfig to remove)."
}

Write-Host "UnitRise Gate Bridge uninstalled." -ForegroundColor Green
