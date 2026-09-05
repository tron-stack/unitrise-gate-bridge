# UnitRise Gate Bridge - Windows installer.
#
# Run from an ADMINISTRATOR PowerShell in the folder holding the downloaded
# agent (unitrise-gate-windows-amd64.exe):
#
#   powershell -ExecutionPolicy Bypass -File .\install.ps1
#
# What it does, in order: copies the agent into Program Files, puts it on the
# system PATH, walks you through pairing (the credentials come from the
# UnitRise console's Gate hardware card), proves the credentials and the save
# folder work, then installs + starts the Windows service. Re-running is safe -
# it updates the binary in place and keeps your pairing.
#
# Options:
#   -ExePath <path>   the downloaded agent exe (default: auto-detect beside
#                     this script)
#   -NoService        stop after pair/test - don't install the service yet

param(
    [string]$ExePath = "",
    [switch]$NoService
)

$ErrorActionPreference = "Stop"

function Write-Banner {
    Write-Host ""
    Write-Host "  UnitRise " -NoNewline -ForegroundColor White
    Write-Host "Gate Bridge" -ForegroundColor DarkYellow
    Write-Host "  Syncs gate codes from UnitRise to your gate software" -ForegroundColor DarkGray
    Write-Host ""
}

function Fail([string]$msg) {
    Write-Host "  ERROR: $msg" -ForegroundColor Red
    exit 1
}

Write-Banner

# ── Elevation ────────────────────────────────────────────────────────────────
$identity = [Security.Principal.WindowsIdentity]::GetCurrent()
$principal = New-Object Security.Principal.WindowsPrincipal($identity)
if (-not $principal.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)) {
    Fail "run this from an Administrator PowerShell (right-click PowerShell > Run as administrator)."
}

# ── Locate the agent binary ──────────────────────────────────────────────────
if (-not $ExePath) {
    $here = Split-Path -Parent $MyInvocation.MyCommand.Path
    $candidates = @("unitrise-gate-windows-amd64.exe", "unitrise-gate.exe") |
        ForEach-Object { Join-Path $here $_ } | Where-Object { Test-Path $_ }
    if (-not $candidates) {
        Fail "couldn't find the agent exe next to this script. Download it from the console's Gate hardware card (Setup guide > Windows), put it beside install.ps1, and run again - or pass -ExePath."
    }
    $ExePath = $candidates[0]
}
if (-not (Test-Path $ExePath)) { Fail "no file at $ExePath" }

# ── Install into Program Files ───────────────────────────────────────────────
$dest = Join-Path $env:ProgramFiles "UnitRise Gate Bridge"
$exe  = Join-Path $dest "unitrise-gate.exe"
New-Item -ItemType Directory -Force -Path $dest | Out-Null

# A running service holds the exe open - stop it for the copy, remember to restart.
$svc = Get-Service -Name "UnitRiseGateBridge" -ErrorAction SilentlyContinue
$svcWasRunning = $svc -and $svc.Status -eq "Running"
if ($svcWasRunning) {
    Write-Host "  Stopping the running service for the update..." -ForegroundColor DarkGray
    Stop-Service -Name "UnitRiseGateBridge" -Force
}

Copy-Item -Force $ExePath $exe
Write-Host "  Installed  $exe"

# ── Tray companion (the agent's visible face) ────────────────────────────────
# unitrise-gate-tray-windows-amd64.exe next to this script installs alongside
# the agent and runs at login: a UnitRise icon by the clock showing sync
# health, with the dashboard one click away. Optional - older release
# bundles without it still install fine.
$trayExe = Join-Path $dest "unitrise-gate-tray.exe"
$traySrc = @("unitrise-gate-tray-windows-amd64.exe", "unitrise-gate-tray.exe") |
    ForEach-Object { Join-Path (Split-Path -Parent $ExePath) $_ } | Where-Object { Test-Path $_ } | Select-Object -First 1
if ($traySrc) {
    # A running tray holds its exe open - stop it for the copy.
    Get-Process -Name "unitrise-gate-tray" -ErrorAction SilentlyContinue | Stop-Process -Force -ErrorAction SilentlyContinue
    Start-Sleep -Milliseconds 300
    Copy-Item -Force $traySrc $trayExe
    # Run at login for every user of this machine (site PCs are shared).
    $runKey = "HKLM:\SOFTWARE\Microsoft\Windows\CurrentVersion\Run"
    Set-ItemProperty -Path $runKey -Name "UnitRiseGateBridgeTray" -Value "`"$trayExe`""
    Start-Process -FilePath $trayExe
    Write-Host "  Installed  $trayExe (tray icon, starts at login)"
} else {
    Write-Host "  Tray app not found beside the installer - skipping (download unitrise-gate-tray-windows-amd64.exe for the tray icon)." -ForegroundColor DarkGray
}

# ── System PATH (so `unitrise-gate` works in any admin console) ──────────────
$machinePath = [Environment]::GetEnvironmentVariable("Path", "Machine")
if ($machinePath -notlike "*$dest*") {
    [Environment]::SetEnvironmentVariable("Path", "$machinePath;$dest", "Machine")
    Write-Host "  Added to the system PATH (new terminals pick it up)"
}
$env:Path = "$env:Path;$dest"

# ── Pair (skipped when already paired) ───────────────────────────────────────
$configPath = Join-Path $env:ProgramData "UnitRiseGateBridge\config.json"
if (Test-Path $configPath) {
    Write-Host "  Already paired ($configPath) - keeping the existing pairing." -ForegroundColor DarkGray
} else {
    Write-Host ""
    Write-Host "  Pairing - have the console's Gate hardware card open (Generate bridge credentials):" -ForegroundColor White
    & $exe pair
    if ($LASTEXITCODE -ne 0) { Fail "pairing didn't complete - run `"$exe`" pair to retry." }
}

# ── Prove it works before leaving it unattended ──────────────────────────────
Write-Host ""
Write-Host "  Testing credentials and the save folder..." -ForegroundColor White
& $exe test
if ($LASTEXITCODE -ne 0) { Fail "the test failed - fix the issue above, then run `"$exe`" test again." }

# ── Service ──────────────────────────────────────────────────────────────────
if ($NoService) {
    Write-Host ""
    Write-Host "  Skipping the service (-NoService). When ready:" -ForegroundColor DarkGray
    Write-Host "    unitrise-gate service install; unitrise-gate service start"
} else {
    if (-not $svc) {
        & $exe service install
        if ($LASTEXITCODE -ne 0) { Fail "service install failed." }
    }
    & $exe service start
    if ($LASTEXITCODE -ne 0) { Fail "service start failed - check: Get-Service UnitRiseGateBridge" }
    Write-Host ""
    Write-Host "  Service running." -ForegroundColor Green
}

Write-Host ""
Write-Host "  Done. " -ForegroundColor Green -NoNewline
Write-Host "The console's Gate card flips to 'Agent online' on the first check-in (within a minute)."
Write-Host "  Local dashboard: " -NoNewline
Write-Host "unitrise-gate ui" -ForegroundColor DarkYellow -NoNewline
Write-Host "  (http://127.0.0.1:47810)"
Write-Host ""
