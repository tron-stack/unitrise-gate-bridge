# UnitRise Gate Bridge

The on-site agent that connects UnitRise to legacy gate software. It is
**deliberately separate from the UnitRise codebase** - it ships to customers'
site computers, so its failures, dependencies, and release cadence must never
entangle with the web platform.

## What it does (and all it does)

Every ~5 minutes (server-tunable), the agent:

1. pulls the facility's **full** gate-code list from UnitRise (ETag-cheap when
   nothing changed),
2. renders it into the gate vendor's file format,
3. **atomically** writes that file into the folder the on-site gate software
   watches (e.g. `C:\PTI\UPDATE.DAT`),
4. runs the vendor's consume command if one exists (e.g. `Ptisend.bat`,
   PTI-MI's import trigger) and captures its output,
5. reports applied-state + a heartbeat, which drive the console's
   online/offline banner.

All policy - who has access, suspensions, code generation - lives in the
cloud. The agent is a courier. If it dies, the gate keeps admitting from its
last imported list (entry degrades gracefully); **suspensions stop
propagating**, which is why the console alarms on a stale heartbeat.

## Layout

```
cmd/unitrise-gate/      THE binary: installer + agent + tray + dashboard in one
                        (pair / test / run / force / ui / tray / service /
                         install / uninstall)
internal/trayicon/      generated tray icons       → tools/gen-trayicons
internal/api/           signed HTTP client        → docs/API_CONTRACT.md
internal/config/        machine-scoped config.json (ProgramData / Library) + Windows ACLs
internal/render/        vendor file renderers      → internal/render/README.md
internal/syncer/        the loop: pull → render → atomic write → consume → report
internal/service/       Windows SCM service (with recovery actions) + macOS launchd
internal/lock/          single-instance guard (named mutex / flock)
internal/ui/            the local 127.0.0.1 dashboard (Rise-branded)
internal/update/        person-run self-update + log-only update watcher
internal/logging/       console + size-capped file log
scripts/                install.ps1 / uninstall.ps1 (Windows)
```

## Build

```
make build          # host binary → dist/
make release        # windows/amd64 (ONE exe: installer+agent+tray+dashboard,
                    #   -H=windowsgui, version resource) + darwin arm64/amd64
                    #   + linux/amd64 + install scripts + SHA256SUMS → dist/
make test           # go vet + unit tests (template engine, config, lock)
```

Pure Go throughout (`golang.org/x/sys` for the Windows service, registry,
named-mutex lock, config-dir ACLs; `fyne.io/systray` for the tray - its
Windows backend is syscall-only, so the ubuntu release runner cross-compiles
everything). The Windows exe is a GUI-subsystem image so double-click and the
login tray never flash a console; CLI commands re-attach to the parent
terminal (`console_windows.go`). It carries a proper version resource
(ProductName "UnitRise Gate Bridge", version, company) so Explorer and Task
Manager identify it.

## The built-in installer (Windows)

One download, one double-click. The exe detects it isn't installed, asks,
elevates (UAC), copies itself to `Program Files\UnitRise Gate Bridge`,
registers the service (auto-start + recovery actions) and the login tray
icon, writes the Add/Remove Programs entry, adds itself to PATH, starts
everything, and opens the dashboard - whose setup-mode pairing form finishes
the job. Re-running the installer over an existing install is the update
path (pairing untouched). Double-clicking an installed copy just opens the
dashboard. Removal: Add/Remove Programs, or `unitrise-gate uninstall`
(pairing kept; the gate's last code file is never touched).

## Setup mode + dashboard pairing

An agent with no (valid) pairing no longer exits - it comes up in **setup
mode**: the service stays healthy, the dashboard serves a pairing form, and
the tray shows "Not connected yet" pointing at it. Credentials entered there
are proven LIVE (API check + save-folder write probe - the same bar as
`pair` + `test`) before anything is saved, then syncing starts without a
restart. Re-pairing from a running dashboard works the same way and restarts
the sync loop on the new credentials in place. The endpoint is guarded
against hostile web pages (JSON-only so browsers preflight, Origin and Host
pinned to loopback), and an unproven config is never written. CLI `pair`
remains for scripted/admin installs.

## The tray (why the agent isn't invisible)

`unitrise-gate tray` is the program's visible face: a UnitRise hexagon by
the clock - amber when healthy, red when the agent reports a problem (the
detail is right in the menu), gray when the service isn't running or "Not
connected yet" before pairing. The menu shows facility, code count, and last
sync, with one-click **Open dashboard** and **Sync now**. Same binary,
separate process on purpose: the sync core runs headless in the service; the
tray only reads the localhost status API (plus the dashboard's one harmless
force action), and quitting the tray never touches the service. Windows-only
for now (`tray_windows.go` - the systray Windows backend is cgo-free); the
macOS menu-bar app lands with the Mac release pass as a proper .app.

## Windows install (the normal path)

Download `unitrise-gate-windows-amd64.exe` and **double-click it** - the
built-in installer (above) does the rest, and the dashboard's pairing form
finishes setup.

Scripted alternative (unattended installs, RMM tools): `install.ps1` next to
the exe, from an Administrator PowerShell:

```
powershell -ExecutionPolicy Bypass -File .\install.ps1
```

It copies the agent into `Program Files\UnitRise Gate Bridge`, adds it to
the system PATH, walks through pairing in the terminal, proves the
credentials and the save folder work, then installs and starts the Windows
service (auto-start, delayed; crash-restart recovery actions set) and the
login tray. Re-running either installer updates the binary in place and
keeps the pairing. `uninstall.ps1` (or Add/Remove Programs) reverses it
(`-PurgeConfig` also removes the pairing).

Manual equivalent - or on macOS (launchd; run with sudo):

1. In UnitRise: yard → Gate hardware → provider + **Generate bridge
   credentials** (Access Key / Secret / Facility ID - shown once).
2. On the gate PC: `unitrise-gate pair` (paste the three values + the folder
   the gate software watches), then `unitrise-gate test`.
3. `unitrise-gate service install && unitrise-gate service start`
4. Verify in the console: the Gate hardware card shows the agent online and
   the last applied state. `unitrise-gate ui` opens the local dashboard
   (http://127.0.0.1:47810).

## Production behaviors worth knowing

- **One agent per machine, enforced** - a second `run`/`force` while the
  service holds the lock (Windows named mutex / Unix flock) refuses with
  instructions, because two agents corrupt delta rosters.
- **Secrets**: `config.json` holds the access secret; the config directory is
  ACL-restricted to SYSTEM + Administrators on Windows (0700/0600 elsewhere).
  Endpoints must be https:// (loopback http allowed for testing only).
- **Failure**: cycles retry on a 30s→4m backoff with jitter instead of
  waiting out the poll interval; a panic in a cycle logs and retries rather
  than killing the service; a drifted PC clock gets its own diagnosis
  ("fix Windows time sync") instead of a misleading "re-pair".
- **Updates**: log-only detection (hourly until the first successful check,
  then daily); the swap is always a person running `unitrise-gate update`,
  and a published SHA256SUMS makes checksum verification mandatory.

## Vendor renderers - the iron rule

A renderer is only written against a **sample export from a live customer
site** (formats drift between vendor versions; documentation is not
sufficient). Until then the provider is a named stub that fails with
instructions. `unitrise_test` is the verification renderer - a human-readable
file that proves the whole loop with no hardware involved.

Known stubs (observed in the field, awaiting a customer sample): PTI/Falcon,
PTI/StorLogix, DoorKing, DigiGate, QuikStor, Revenue Control Systems,
SpiderDoor, Stor-Guard, WinSen.

## Server counterpart

`truckpark-backend` implements the endpoints in
[docs/API_CONTRACT.md](docs/API_CONTRACT.md) (G1 of
`MyTruckYards/docs/GATE_INTEGRATION_PLAN.md`). Contract changes update both
sides in lockstep.
