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
cmd/unitrise-gate/      the single binary (pair / test / run / force / ui / service)
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
make release        # windows/amd64 (+ version resource) + darwin arm64/amd64
                    #   + linux/amd64 + install scripts + SHA256SUMS → dist/
make test           # go vet + unit tests (template engine, config, lock)
```

Pure Go, one tiny dependency (`golang.org/x/sys` for the Windows service,
named-mutex lock, and config-dir ACLs). Binaries are static; nothing to
install on the site machine but the exe. The Windows exe carries a proper
version resource (ProductName "UnitRise Gate Bridge", version, company) so
Explorer and Task Manager identify it.

## Windows install (the normal path)

From an **Administrator PowerShell** in the folder holding the downloaded
`unitrise-gate-windows-amd64.exe` and `install.ps1`:

```
powershell -ExecutionPolicy Bypass -File .\install.ps1
```

The installer copies the agent into `Program Files\UnitRise Gate Bridge`,
adds it to the system PATH, walks through pairing (credentials from the
console's Gate hardware card), proves the credentials and the save folder
work, then installs and starts the Windows service (auto-start, delayed;
crash-restart recovery actions set). Re-running updates the binary in place
and keeps the pairing. `uninstall.ps1` reverses it (`-PurgeConfig` also
removes the pairing).

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
