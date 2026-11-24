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
3. **atomically and durably** writes that file into the folder the on-site
   gate software watches (e.g. `C:\PTI\UPDATE.DAT`): a random temp file,
   flushed to disk, then renamed into place,
4. runs the vendor's consume command if one exists (e.g. `Ptisend.bat`,
   PTI-MI's import trigger): the plain name of a `.bat`, `.cmd` or `.exe`
   file in that same folder, run as the file itself with no arguments, never
   as a command line, and captures its output,
5. reports applied-state + a heartbeat, which drive the console's
   online/offline banner. The heartbeat also runs on its own clock every
   60-90 seconds, so an agent polling once an hour still reads online.

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
internal/update/        person-run, signature-checked self-update + log-only update watcher
internal/fsutil/        the one durable atomic file write (temp + fsync + rename)
internal/mask/          keeps gate codes out of the dashboard, logs and reports
tools/sign-release/     signs a release's SHA256SUMS (and makes the key pair)
internal/logging/       console + size-capped file log
scripts/                install.ps1 / uninstall.ps1 (Windows)
```

## Build

```
make build          # host binary → dist/
make release        # windows/amd64 (ONE exe: installer+agent+tray+dashboard,
                    #   -H=windowsgui, version resource) + darwin arm64/amd64
                    #   + linux/amd64 + install scripts + SHA256SUMS → dist/
make test           # go vet + unit tests
make release VERSION=x.y.z RELEASE_SIGNING_KEY=<path>   # + SHA256SUMS.sig
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
restart. The endpoint is guarded against hostile web pages (JSON-only so
browsers preflight, Origin and Host pinned to loopback), and an unproven
config is never written.

The dashboard has no sign-in, so it can do only what is safe for any account
on the PC (agent 0.6.0): a first pairing goes to UnitRise's own server
(`https://api.unitrise.com`); on a machine that is already paired it can
replace only the access key and secret ("Update credentials"), proven against
the server it is paired with. A different facility, server or save folder
needs an Administrator: `unitrise-gate pair ... --endpoint ... --save ...`
from an Administrator prompt writes the protected config itself and restarts
the service onto it. `--secret -` reads the secret from standard input
(`type secret.txt | unitrise-gate pair --secret - --key K --facility F --save C:\PTI`)
so it stays out of the shell history. A developer can let the dashboard pair
with another server by running the agent with `UNITRISE_GATE_DEV=1`.

## The control window

`unitrise-gate window` - the program's own app window (WebView2, cgo-free
binding), launched by the tray's "Open Gate Bridge", the desktop/Start-Menu
shortcut, or double-clicking the installed exe. It frames the local
dashboard (status, activity, pairing, updates, and the read-only **Gate
codes** roster with search, each code masked to its last two digits) and adds the one thing the dashboard can't do:
a service bar that **starts and stops the Windows service** (each action
elevates via UAC; the window itself stays unelevated - the agent IS the
service, so control has to live outside it). When the service is stopped
the window shows an honest offline panel with a Start button instead of a
dead page. The roster is read-only on purpose: the local page has no auth,
so it holds no write power over access - codes are managed in the UnitRise
console. No WebView2 runtime (rare) → falls back to the browser dashboard
with an explanation. macOS gets this as a real .app in the Mac release pass.

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
# unattended: the secret comes from the environment, never an option
$env:UNITRISE_GATE_SECRET = "<secret>"; .\install.ps1 -Key <key> -Facility <id> -SavePath C:\PTI
```

It copies the agent into `Program Files\UnitRise Gate Bridge`, locks down the
settings folder (below) BEFORE the service exists, adds the agent to the
system PATH, installs and starts the Windows service (auto-start, delayed;
crash-restart recovery actions set) and the login tray, then pairs: it opens
the Gate Bridge window and waits until the credentials pasted there prove
out, or, with `-Key`/`-Facility`/`-SavePath` (and `-Endpoint` when support
says so), pairs with those values, passing the secret to
`unitrise-gate pair --secret -` on standard input. Last it proves the
credentials and the save folder work. `-NoService` skips the service and
pairs only with the options. Re-running either installer updates the binary
in place and keeps a trusted pairing. `uninstall.ps1` (or Add/Remove
Programs) reverses it (`-PurgeConfig` also removes the pairing).

**The settings folder is locked down before the service exists (0.6.0).**
Any account may create a folder in `%ProgramData%`, and the service (SYSTEM)
reads its pairing from `%ProgramData%\UnitRiseGateBridge`. Both installers
(the exe's own and `install.ps1`, through the hidden `unitrise-gate
secure-config`) make that folder Administrators' own, with SYSTEM and
Administrators full control and inheritance off, before the service is
installed or started. The service does the same at every start and refuses
to trust a `config.json` (or a roster, the own-files record or the log) unless
the folder and the file are owned by SYSTEM or Administrators and nobody else
can change them, and the folder is a real folder, not a link. A folder that
fails is emptied of those files (a link is removed, never followed), the
agent says so in its log and on the dashboard, and it runs unpaired until it
is paired again. A folder only SYSTEM or an Administrator could have written
keeps its pairing.

Manual equivalent - or on macOS (launchd; run with sudo):

1. In UnitRise: Properties → **Gate** on the property's row → provider +
   **Generate bridge credentials** (Access Key / Secret / Facility ID). The
   card can bring the current pair up again with **Show credentials**
   (generating and showing both need the Properties AND the Customers &
   leads permissions, and the server records who looked); rotating replaces
   the pair and the agent must be paired again.
2. On the gate PC: `unitrise-gate pair` (paste the three values + the folder
   the gate software watches), then `unitrise-gate test`.
3. `unitrise-gate service install && unitrise-gate service start`
4. Verify in the console: the Gate card shows the agent online and the last
   applied state. `unitrise-gate ui` opens the local dashboard
   (http://127.0.0.1:47810).
5. Choose the gate software on the Gate card and press **Save**. Until a
   format is saved the agent writes only `UNITRISE-GATE-TEST.txt` (the server
   sends no file name in that mode, so the test output never lands under the
   gate software's own file name) and no codes reach the keypad. The gate
   file name and the consume command are plain file names in the save folder
   (`UPDATE.DAT`, `Ptisend.bat`): no folder path, no arguments, no second
   command. The console refuses anything else at Save and the agent refuses
   it too, its own `config.json` overrides included (see
   [docs/API_CONTRACT.md](docs/API_CONTRACT.md)). A trigger that needs
   arguments goes in a one-line `.bat` in the save folder, and that file's
   name is the consume command.
6. Test it: suspend a test code and check the keypad turns it away. A red
   **Not reaching the gate** on the card means the agent is running but its
   last write or consume command failed; the card shows the agent's own
   detail.

## Production behaviors worth knowing

- **One agent per machine, enforced** - a second `run`/`force` while the
  service holds the lock (Windows named mutex / Unix flock) refuses with
  instructions, because two agents corrupt delta rosters.
- **Secrets**: `config.json` holds the access secret; the config directory is
  owned by Administrators and ACL-restricted to SYSTEM + Administrators on
  Windows (0700/0600 elsewhere), and a pairing in a folder anyone else could
  have written is refused (see "The settings folder" above).
  Endpoints must be https:// (loopback http allowed for testing only).
- **Failure**: cycles retry on a 30s→4m backoff with jitter instead of
  waiting out the poll interval; a panic in a cycle logs and retries rather
  than killing the service; a drifted PC clock gets its own diagnosis
  ("fix Windows time sync") instead of a misleading "re-pair". A failed
  sync (`ok: false` on the heartbeat, or a nonzero consume exit) turns the
  console card red with the agent's detail, and the owner is emailed once
  per outage.
- **Updates**: log-only detection (hourly until the first successful check,
  then daily); the swap is always a person (`unitrise-gate update`, or Install
  update on the dashboard). Since 0.6.0 the agent installs only a release from
  `https://github.com/tron-stack/unitrise-gate-bridge/releases/download/v<version>/`
  whose `SHA256SUMS` carries a valid `SHA256SUMS.sig` from the release key
  compiled into it (`release-signing.pub`); the server can no longer name a
  binary. `unitrise-gate version` says whether this build carries the key; a
  build without one refuses every self-update.
- **Delta formats wait for the gate software to take the file** (0.6.0): the
  next diff is against what the gate software has CONFIRMED (the consume
  command exited 0, or, with no consume command, the vendor removed the file),
  so a failed import is resent as a superset instead of lost. Removals are
  written before additions.
- **The system account never runs an import file ordinary users can change**
  (0.6.0): run by the service with nobody signed in, the consume file runs as
  LocalSystem, so the agent first checks that neither the file nor its folder
  can be changed by Everyone, Authenticated Users, Users and similar groups.
  When they can (C:\PTI often inherits "Authenticated Users: Modify"), it
  doesn't run, and the Gate card turns red: "The import file's folder can be
  changed by any user on this PC, so the Bridge won't run it as the system
  account. Limit the folder to Administrators."
- **No full code is shown or sent outside the gate file**: the dashboard shows codes masked to
  their last two digits, and the consume command's output has every run of
  three or more digits masked before it reaches the log, the dashboard or
  UnitRise.
- **The agent's own test file is removed** once a gate format takes over
  (UNITRISE-GATE-TEST.txt and the GATECODES.TXT fallback both carry every
  code); only a file the agent wrote, unchanged since, by its exact name.
- **The consume command is a file, not a command line** (2026-11-13, first
  released in 0.6.0; no 0.5.9 was ever published): the
  agent runs only a `.bat`, `.cmd` or `.exe` that sits directly in the save
  folder, by its own path, with no arguments and never as a command line (a
  batch file runs in `cmd.exe` the way Windows runs one, handed only that
  path), and writes the gate file only directly into that folder. Anything else is refused in
  words, nothing is written or run, and the card turns red with the reason.
- **A property that moves to another organization** stops syncing: the old
  pair is refused (401) from the next request, and the new owner pairs the
  agent again from their own Gate card.

## Releasing (signed updates, 0.6.0)

Agents install a self-update only when `SHA256SUMS` is signed by the release
key, so a release needs the key pair once and the private key at every
release:

1. Once: make the key pair OUTSIDE the repository and commit only the public
   half:
   `go run ./tools/sign-release keygen -out ~/unitrise-release-key` writes
   `release-signing.key` (secret: keep it in a password manager and add its
   contents as the GitHub repository secret `RELEASE_SIGNING_KEY`) and
   `release-signing.pub` (copy it to the repository root and commit it). An
   OpenSSL key works too:
   `openssl genpkey -algorithm ed25519 -out release-signing.key`, then
   `go run ./tools/sign-release pubkey -key release-signing.key > release-signing.pub`.
   The public key must be committed BEFORE the first signed release is
   tagged: every agent built without it refuses every later self-update and
   has to be reinstalled by hand.
2. Each release: tag `v<x.y.z>`. CI builds every platform with the public key
   compiled in, signs `SHA256SUMS` into `SHA256SUMS.sig` (and fails if the
   public key is committed but the secret is missing), and publishes both. By
   hand: `make release VERSION=x.y.z RELEASE_SIGNING_KEY=<path to the key>`.
   `go run ./tools/sign-release verify -version x.y.z dist/SHA256SUMS` checks
   a signature.
3. Then set `GATE_AGENT_LATEST_VERSION=x.y.z` on the backend. Its update-check
   must hand the agent the versioned release folder
   (`.../releases/download/v<x.y.z>/<asset>` and `.../SHA256SUMS`).

## Vendor formats - one template renderer

Every vendor layout renders through the template renderer
(`internal/render/template.go`). With a gate software format saved on the
property's Gate card, the server sends `provider: "template"` and the format
itself (`settings.format`): the console's presets for PTI Falcon 2000, PTI
StorLogix, WinSen/Sentinel, Digigate, QuikStor and DoorKing Remote Account
Manager, a generic CSV, or a custom layout (the presets are data in
`truckpark-backend/util/gateFilePresets.js`). A layout fix is a console edit
picked up on the next poll; the site PC is never touched. Until a format is
saved the server sends `unitrise_test`, the verification renderer: a
human-readable file that proves the whole loop with no hardware involved, and
no codes reach the keypad.

The rule that a layout is proven against a **sample export from a live
customer site** still holds (formats drift between vendor versions;
documentation is not sufficient): the console asks the operator to confirm
the rendered line against the gate PC's current file before go-live
(`MyTruckYards/docs/runbooks/gate-bridge-onsite-pti.md` for PTI).

The old per-vendor provider names (`pti_falcon`, `pti_storlogix`, `doorking`,
`digigate`, `quikstor`, `rcs`, `spiderdoor`, `stor_guard`, `winsen`) are still
recognized by `internal/render/render.go` as named stubs that fail with
instructions, but the server never sends them.

## Server counterpart

`truckpark-backend` implements the endpoints in
[docs/API_CONTRACT.md](docs/API_CONTRACT.md) (G1 of
`MyTruckYards/docs/GATE_INTEGRATION_PLAN.md`). Contract changes update both
sides in lockstep.
