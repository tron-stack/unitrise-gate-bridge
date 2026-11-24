# UnitRise Gate Bridge - Windows installer.
#
# Run from an ADMINISTRATOR PowerShell in the folder holding the downloaded
# agent (unitrise-gate-windows-amd64.exe):
#
#   powershell -ExecutionPolicy Bypass -File .\install.ps1
#
# What it does, in order: copies the agent into Program Files, LOCKS DOWN the
# settings folder (%ProgramData%\UnitRiseGateBridge: owned by Administrators,
# SYSTEM and Administrators only, nothing inherited) before the service
# exists, puts the agent on the system PATH, installs + starts the Windows
# service, then pairs: it opens the Gate Bridge window and waits while you
# paste the credentials from the UnitRise console's Gate card (or, with the
# -Key/-Facility/-SavePath options below, pairs with the values you pass),
# and finally proves the credentials and the save folder work. Re-running is
# safe - it updates the binary in place and keeps a pairing that only SYSTEM
# or an Administrator could have written (a settings folder anybody else
# could have planted is emptied, and the script says so).
#
# Options:
#   -ExePath <path>     the downloaded agent exe (default: auto-detect beside
#                       this script)
#   -NoService          don't install or start the service (pair only with
#                       the options below; without them, pair later)
#   -Key <access key>   scripted pairing (RMM / unattended): the access key,
#   -Facility <id>      the facility ID and the save folder from the Gate card.
#   -SavePath <folder>  The secret is NEVER an option (it would sit in your
#                       history and in the process list): it is read from the
#                       UNITRISE_GATE_SECRET environment variable, or asked for.
#   -Endpoint <url>     a different UnitRise server (only when support says so)

param(
    [string]$ExePath = "",
    [switch]$NoService,
    [string]$Key = "",
    [string]$Facility = "",
    [string]$SavePath = "",
    [string]$Endpoint = ""
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

# The exe is a GUI-subsystem image (one binary = installer + agent + tray),
# so PowerShell doesn't wait for it on its own - Start-Process -Wait does,
# and -NoNewWindow shares this console so the agent's messages show here.
function Invoke-Agent {
    param([string[]]$AgentArgs)
    $p = Start-Process -FilePath $exe -ArgumentList $AgentArgs -Wait -NoNewWindow -PassThru
    return $p.ExitCode
}

# A running service or tray holds the exe open - stop both for the copy.
$svc = Get-Service -Name "UnitRiseGateBridge" -ErrorAction SilentlyContinue
$svcWasRunning = $svc -and $svc.Status -eq "Running"
if ($svcWasRunning) {
    Write-Host "  Stopping the running service for the update..." -ForegroundColor DarkGray
    Stop-Service -Name "UnitRiseGateBridge" -Force
}
Get-CimInstance Win32_Process -Filter "Name='unitrise-gate.exe'" -ErrorAction SilentlyContinue |
    ForEach-Object { Stop-Process -Id $_.ProcessId -Force -ErrorAction SilentlyContinue }
Start-Sleep -Milliseconds 400

Copy-Item -Force $ExePath $exe
Write-Host "  Installed  $exe"

# ── The settings folder, BEFORE the service exists ───────────────────────────
# Any user may create a folder in %ProgramData%, and one created before the
# service could hand it (it runs as SYSTEM) a planted pairing. The agent's own
# secure-config makes %ProgramData%\UnitRiseGateBridge Administrators' own,
# with SYSTEM and Administrators full control and inheritance off. An existing
# folder that only they could have written keeps its pairing; anything else is
# emptied, and the agent says what it deleted.
$dataDir = Join-Path $env:ProgramData "UnitRiseGateBridge"
if ((Invoke-Agent @("secure-config")) -ne 0) {
    Fail "couldn't lock down $dataDir - see the message above. An Administrator can delete that folder and run this again."
}

# ── Tray icon (the same exe, `tray` mode) at login for every user ────────────
$runKey = "HKLM:\SOFTWARE\Microsoft\Windows\CurrentVersion\Run"
Set-ItemProperty -Path $runKey -Name "UnitRiseGateBridgeTray" -Value "`"$exe`" tray"
Start-Process -FilePath $exe -ArgumentList "tray"
Write-Host "  Tray icon registered (starts at login)"

# ── System PATH (so `unitrise-gate` works in any admin console) ──────────────
$machinePath = [Environment]::GetEnvironmentVariable("Path", "Machine")
if ($machinePath -notlike "*$dest*") {
    [Environment]::SetEnvironmentVariable("Path", "$machinePath;$dest", "Machine")
    Write-Host "  Added to the system PATH (new terminals pick it up)"
}
$env:Path = "$env:Path;$dest"

# ── Agent helpers ─────────────────────────────────────────────────────────────
# The running agent answers on 127.0.0.1:47810 (or the next few ports).
function Find-Agent {
    foreach ($port in 47810..47814) {
        try {
            return Invoke-RestMethod -Uri "http://127.0.0.1:$port/api/status" -TimeoutSec 2 -UseBasicParsing
        } catch { }
    }
    return $null
}

function Wait-Agent([int]$Seconds) {
    $deadline = (Get-Date).AddSeconds($Seconds)
    while ((Get-Date) -lt $deadline) {
        $s = Find-Agent
        if ($s) { return $s }
        Start-Sleep -Milliseconds 500
    }
    return $null
}

# One argument for the agent's command line. Start-Process joins its argument
# list with plain spaces, so a value with a space (C:\Program Files\PTI) must
# be quoted here, with any trailing backslashes doubled so the closing quote
# isn't read as an escaped one.
function Quote-Arg([string]$a) {
    if ($a -ne "" -and $a -notmatch '\s') { return $a }
    $trail = [regex]::Match($a, '\\*$').Value
    return '"' + $a.Substring(0, $a.Length - $trail.Length) + ($trail * 2) + '"'
}

# Scripted pairing: `unitrise-gate pair --secret -`, with the secret handed
# over on standard input from a file in this Administrator's own temp folder
# that is deleted straight after - never on a command line.
function Invoke-AgentPair([string]$Secret) {
    $agentArgs = @("pair", "--key", (Quote-Arg $Key), "--secret", "-", "--facility", (Quote-Arg $Facility), "--save", (Quote-Arg $SavePath))
    if ($Endpoint) { $agentArgs += @("--endpoint", (Quote-Arg $Endpoint)) }
    $secretFile = New-TemporaryFile
    try {
        [System.IO.File]::WriteAllText($secretFile.FullName, $Secret + "`n")
        $p = Start-Process -FilePath $exe -ArgumentList $agentArgs -Wait -NoNewWindow -PassThru -RedirectStandardInput $secretFile.FullName
        return $p.ExitCode
    } finally {
        Remove-Item -Force $secretFile.FullName -ErrorAction SilentlyContinue
    }
}

function Read-Secret {
    if ($env:UNITRISE_GATE_SECRET) { return $env:UNITRISE_GATE_SECRET }
    $sec = Read-Host "  Access secret (from the Gate card)" -AsSecureString
    $bstr = [Runtime.InteropServices.Marshal]::SecureStringToBSTR($sec)
    try { return [Runtime.InteropServices.Marshal]::PtrToStringBSTR($bstr) }
    finally { [Runtime.InteropServices.Marshal]::ZeroFreeBSTR($bstr) }
}

$scripted = [bool]($Key -or $Facility -or $SavePath -or $Endpoint)
if ($scripted -and -not ($Key -and $Facility -and $SavePath)) {
    Fail "scripted pairing needs -Key, -Facility and -SavePath together (the secret comes from UNITRISE_GATE_SECRET, or is asked for)."
}

# ── Service (before pairing: the pairing window talks to the running agent) ──
if ($NoService) {
    Write-Host ""
    Write-Host "  Skipping the service (-NoService)." -ForegroundColor DarkGray
} else {
    if (-not $svc) {
        if ((Invoke-Agent @("service", "install")) -ne 0) { Fail "service install failed." }
    }
    if ((Invoke-Agent @("service", "start")) -ne 0) { Fail "service start failed - check: Get-Service UnitRiseGateBridge" }
    $status = Wait-Agent 30
    if (-not $status) {
        Fail "the service started but the agent isn't answering on 127.0.0.1:47810 - check Get-Service UnitRiseGateBridge and the log in $dataDir\agent.log"
    }
    Write-Host "  Service running." -ForegroundColor Green
}

# ── Pair ─────────────────────────────────────────────────────────────────────
$paired = $false
if ($scripted) {
    Write-Host ""
    Write-Host "  Pairing with the values you passed..." -ForegroundColor White
    $secret = Read-Secret
    if (-not $secret) { Fail "no access secret - set UNITRISE_GATE_SECRET or type it when asked." }
    $code = Invoke-AgentPair $secret
    $secret = $null
    if ($code -ne 0) { Fail "pairing failed - see the message above. Check the three values on the Gate card and the save folder, then run this again." }
    $paired = $true
} elseif ($NoService) {
    Write-Host "  Not paired. Pair later: start the service (unitrise-gate service install; unitrise-gate service start)" -ForegroundColor DarkGray
    Write-Host "  and use the Gate Bridge window, or rerun this script with -Key, -Facility and -SavePath."
} elseif ($status.paired -ne $false) {
    $where = if ($status.facilityName) { $status.facilityName } else { "its facility" }
    Write-Host "  Already paired to $where - keeping the existing pairing." -ForegroundColor DarkGray
    $paired = $true
} else {
    Write-Host ""
    Write-Host "  Pair this machine in the Gate Bridge window that is opening now:" -ForegroundColor White
    Write-Host "    paste the access key, access secret and facility ID from the property's Gate card in UnitRise"
    Write-Host "    (Properties > Gate > Generate bridge credentials, or Show credentials), the folder your gate"
    Write-Host "    software imports from (for PTI, usually C:\PTI), and press Verify & save."
    Start-Process -FilePath $exe -ArgumentList "window"
    Write-Host "  Waiting for the pairing (up to 30 minutes; Ctrl+C stops this wait, and the window keeps working)..." -ForegroundColor DarkGray
    $deadline = (Get-Date).AddMinutes(30)
    while ((Get-Date) -lt $deadline) {
        Start-Sleep -Seconds 3
        $s = Find-Agent
        if ($s -and $s.paired -eq $true) {
            $where = if ($s.facilityName) { $s.facilityName } else { "your facility" }
            Write-Host "  Paired to $where." -ForegroundColor Green
            $paired = $true
            break
        }
    }
    if (-not $paired) {
        Write-Host "  Not paired yet. Finish it in the Gate Bridge window any time - the service is waiting for it." -ForegroundColor Yellow
    }
}

# ── Prove it works before leaving it unattended ──────────────────────────────
if ($paired) {
    Write-Host ""
    Write-Host "  Testing credentials and the save folder..." -ForegroundColor White
    if ((Invoke-Agent @("test")) -ne 0) { Fail "the test failed - fix the issue above, then run `"$exe`" test again." }
}

Write-Host ""
Write-Host "  Done. " -ForegroundColor Green -NoNewline
Write-Host "The console's Gate card flips to 'Agent online' on the first check-in (within a minute)."
Write-Host "  Local dashboard: " -NoNewline
Write-Host "unitrise-gate ui" -ForegroundColor DarkYellow -NoNewline
Write-Host "  (http://127.0.0.1:47810)"
Write-Host ""
