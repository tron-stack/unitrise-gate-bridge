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
cmd/unitrise-gate/      the single binary (pair / test / run / force / service)
internal/api/           signed HTTP client        → docs/API_CONTRACT.md
internal/config/        machine-scoped config.json (ProgramData / Library)
internal/render/        vendor file renderers      → internal/render/README.md
internal/syncer/        the loop: pull → render → atomic write → consume → report
internal/service/       Windows SCM service + macOS launchd
internal/logging/       console + size-capped file log
```

## Build

```
make build          # host binary → dist/
make release        # windows/amd64 + darwin arm64/amd64 + linux/amd64 → dist/
make test           # go vet + unit tests
```

Pure Go, one tiny dependency (`golang.org/x/sys` for the Windows service).
Binaries are static; nothing to install on the site machine but the exe.

## Site install (operator instructions)

1. In UnitRise: yard → Gate hardware → provider + **Generate bridge
   credentials** (Access Key / Secret / Facility ID - shown once).
2. On the gate PC: `unitrise-gate pair` (paste the three values + the folder
   the gate software watches), then `unitrise-gate test`.
3. `unitrise-gate service install && unitrise-gate service start`
   (macOS: run with sudo - it writes a LaunchDaemon).
4. Verify in the console: the Gate hardware card shows the agent online and
   the last applied state.

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
