# UnitRise Gate Bridge - API Contract (v1)

_The shared source of truth between this agent and the UnitRise backend
(truckpark-backend G1 work implements the server side of exactly this).
Version every breaking change; the agent sends its contract version._

## Credentials (three-part, storEDGE-familiar)

Minted per facility in the UnitRise console (Gate hardware card → "Generate
bridge credentials"), revocable there at any time:

- **Access Key** - public identifier (`urgk_` prefix)
- **Access Secret** - HMAC secret (`urgs_` prefix). The console can show the
  current secret again (**Show credentials** on the Gate card, manager-gated,
  and the server records who looked: the last person, and a history of
  everybody who has looked at this pair, one row per person with when they
  last did, the newest 50 kept, 2026-11-13); rotating replaces the pair and
  starts a new history
- **Facility ID** - the UnitRise facility ObjectId

## Request signing

Every request carries:

```
X-UR-Key:       <accessKey>
X-UR-Facility:  <facilityId>
X-UR-Timestamp: <unix seconds>
X-UR-Signature: hex(hmacSHA256(secret, "<METHOD>\n<PATH>\n<timestamp>\n<sha256hex(body)>"))
```

Server rejects: unknown key, key↔facility mismatch, timestamp skew > 5 min,
bad signature, revoked credentials, and (2026-11-13) a pair made for an
organization the facility no longer belongs to. All rejections are the same
`401`. All bridge endpoints are additionally rate-limited per key.

**A pair belongs to the facility's current organization.** When a property
moves to another organization, the pair the old organization made stops
working on the next request (it is refused even when its signature is
good), so that site PC stops syncing. The new owner pairs the agent again
from their own Gate card: their first Save, credential mint or remote-open
change there takes the link over, with the old pairing, the agent's state,
the consume command and the remote-open switches cleared, and the next pull
is a full one (`forceNonce` moves). The file format, the zones and the file
name stay, because they describe the gate software at the property.

## Endpoints

### `GET /api/gate-bridge/v1/state`

Headers: signing headers + optional `If-None-Match: "<stateHash>"`.

- `304` - unchanged (the common case). Since 2026-11-13 the server answers it
  WITHOUT building the state: it first reads a cheap probe (the facility's
  code rows by id, status and last change, its unit labels, and the link's
  settings), and when the probe matches the one it read for the last full
  build, the agent already holds that build's hash, and that build is less
  than 15 minutes old, it answers `304` without decrypting a single code.
  Otherwise it builds the state and compares the hash as before.
- `200`:

```json
{
  "stateHash": "sha256-of-canonical-state",
  "facility": { "name": "...", "address": "...", "phone": "..." },
  "provider": "template",
  "settings": {
    "generatedFileName": "UPDATE.DAT",
    "consumeCommand": "Ptisend.bat",
    "defaultTimeZone": "011A",
    "pollSeconds": 300,
    "format": {
      "preset": "pti_falcon",
      "mode": "full",
      "header": "",
      "line": "{code},{unit},{tenant},{tz}",
      "vacantLine": "",
      "suspendedLine": "",
      "addedLine": "",
      "changedLine": "",
      "removedLine": "",
      "footer": "",
      "lineEnding": "crlf",
      "sortBy": "code"
    }
  },
  "forceNonce": 0,
  "credentials": [
    {
      "code": "482913",
      "unitLabel": "A-14",
      "tenantName": "M. Torres",
      "status": "active",          // active | suspended  (revoked codes are ABSENT)
      "timeZoneGroup": "011A"
    }
  ]
}
```

`provider` selects the agent's renderer: `"template"` (the server carries a
renderable `settings.format`) or `"unitrise_test"` (the verification file -
file_bridge's default until a format is configured). In `unitrise_test` mode
the server sends `generatedFileName: ""`, so the agent writes its own
`UNITRISE-GATE-TEST.txt` and the test output never lands under the gate
software's file name (2026-10-31).

### `settings.generatedFileName` and `settings.consumeCommand` - plain file names (2026-11-13)

The agent writes the one and RUNS the other with the site PC's own rights,
so both are plain file names in the save folder, never paths or command
lines:

- letters, digits, spaces, dots, dashes and underscores only, starting with
  a letter or digit, at most 100 characters;
- no folder separator (`/` or `\`), no `..`, no trailing dot or space (Windows
  drops them, so the name would mean another file), and not a Windows device
  name (`CON`, `PRN`, `AUX`, `NUL`, `COM0`-`COM9`, `LPT0`-`LPT9`, with or
  without an extension);
- `consumeCommand` ends in `.bat`, `.cmd` or `.exe` (e.g. `Ptisend.bat`);
- `generatedFileName` is not a program or script file (`.bat`, `.cmd`,
  `.exe`, `.com`, `.ps1`, `.vbs`, `.js`, `.lnk`, `.reg` and the other
  runnable types in `internal/syncer/consume.go`), e.g. `UPDATE.DAT`.

The console refuses anything else at Save (`422`, saying which rule). A value
stored before the rule is never sent: the server sends `""` in its place (and
a refused file name withholds the consume command too, since that command
would import a file the agent no longer writes), so the agent writes its
renderer's default file (`GATECODES.TXT` for templates, which no gate
software reads) and runs nothing. The Gate card then reads **Not reaching
the gate** with the reason.

The agent holds every value to the same rule itself, the server's and its
own `config.json` overrides alike:

- the gate file must resolve to a file directly inside `savePath`, or nothing
  is written;
- the consume command must be a regular file directly inside `savePath`
  (not a link, not a folder), or nothing runs;
- it is run as the file itself, by its full path, with the save folder as its
  working directory, no arguments and no shell. On Windows a `.bat` or
  `.cmd` runs in `cmd.exe` the way Windows always runs a batch file, handed
  only the checked path; when the agent runs it in the signed-in user's
  session, it starts `cmd.exe` from the system folder with `/D /S /C`, the
  checked path and a temporary output file, nothing else;
- a refusal is reported as `ok: false` with the reason (and a refused
  consume command as `consumeExit: -2`), so the card turns red.

A site whose trigger needs arguments puts a one-line `.bat` in the save
folder that runs it, and names that file. Agents older than this rule run a
plain name the server sends through the shell as before; the server only
ever sends them a plain name now, but their own `config.json` override is
held to the rule only once they update.

### `settings.format` - the server-driven vendor template

Vendor gate-file layouts are per-site artifacts (specs are partner-only paper
that drifts between versions), so the layout is DATA the console edits, not
agent code. A format fix reaches the site on the next poll; the agent binary
never changes for it.

Placeholders, usable in every template string:

| Placeholder | Meaning |
|---|---|
| `{code}` `{unit}` `{tenant}` `{tz}` `{status}` | per-credential fields; `{tz}` is an alphanumeric zone/access STRING (e.g. PTI "011A" - was an int before 2026-09-06; agents decode both) |
| `vacantLine` (format field) | rendered per VACANT unit, full mode only. When set, the server includes every unit NOBODY RENTS as a `status:"vacant"`, empty-code credential row, so a full send rewrites the controller's per-unit records and clears stray codes on vacant units. A unit with a live rental and no UnitRise code is LEFT OUT (its keypad record is unchanged); a unit whose code staff revoked, or whose rental ended, is listed vacant; while any live rental is on no unit at all, no vacant rows are sent (2026-10-31). Blank = vacant units omitted (pre-2026-09-06 behavior). |
| `{facility}` `{date}` `{time}` `{count}` | file-level (header/footer; count = credential lines emitted) |
| `{code:pad10}` | zero-pad left to width |
| `{tenant:width20}` | space-pad right + truncate |
| `{unit:rwidth6}` | space-pad left + truncate |

Modes:

- **`full`** - the file is the complete roster each write. Suspended codes
  render via `suspendedLine`; when it's blank they are OMITTED (removal is the
  lockout for formats that can't express suspension).
- **`delta`** - op-coded change lines against the roster the gate software
  has CONFIRMED taking (`addedLine`/`changedLine`/`removedLine`, e.g. WinSen's
  A/E-prefixed update.txt). A suspension that the format can't express
  (`suspendedLine` blank) emits as a REMOVE op, and the code leaves the roster
  so a later restore re-emits as an add. A blank `removedLine` means removals
  are inexpressible and are skipped. Lines are written in this order:
  **removals, then changes, then additions**, so an importer keyed by unit
  never deletes the tenant it just added (2026-11-20).

  **Delta confirmation (agent 0.6.0, 2026-11-20).** The agent keeps two
  rosters beside `config.json`: `last-roster.json` (CONFIRMED: what the gate
  software has taken) and `last-roster.pending.json` (PENDING: what the file on
  disk tells it, with the file's path and a hash). Every render diffs against
  the confirmed roster. The pending roster is promoted only when the consume
  command exits 0, or, with no consume command, when the vendor has removed or
  renamed the file by the next cycle. A file still there unimported is replaced
  by a SUPERSET diff (every change since the confirmed roster), so a failed
  import never loses a change; at worst a change is sent twice. An agent before
  0.6.0 advanced the roster as soon as the file landed, so a failed consume
  lost the earlier lines.

Every line - including the last - ends with the configured line ending
(`crlf` default; DOS-lineage importers require the trailing carriage return).

**Force semantics:** the state carries `forceNonce`. When it differs from the
confirmed roster's nonce (console "Force full update"), or the agent's local
Force button was pressed, delta mode treats the roster as empty and re-emits
the entire roster as adds; full mode simply rewrites. A forced re-emit that is
not taken stays a full re-emit on the next write.

Semantics: **full state, every time.** The list is the complete set of codes
that should exist at the gate. Suspended codes are included with
`status: "suspended"` for renderers whose format expresses lockout;
formats that can't express it simply omit suspended codes (removal = lockout
only where the gate drops a code missing from the file).
Revoked/expired codes are never present. A suspended code whose rendered line
would be identical to an active code's (the same template and the same
`{tz}` value) is left out of the list by the server rather than sent to be
written as active, and the console refuses to save such a format
(2026-10-31).

`stateHash` is computed server-side over the canonical credential list +
settings, so any change (issue, suspend, restore, revoke, rename, settings
edit) changes the hash. A change the probe above sees (a code issued,
suspended, restored or revoked, a unit renamed, a settings edit, a forced
update) reaches the agent on its next poll. A change the probe cannot see (a
tenant's name, a transfer to another unit, which units are vacant) reaches
it on the first poll after the last full build is 15 minutes old, so the
worst case for those is 15 minutes plus one poll interval. Each server
process keeps its own memory of the last build; a restart or a second
process simply builds once more.

Poll cadence: the server's `settings.pollSeconds` (console-tunable, 60–3600,
default 300) sets the agent's cycle - unless the agent's LOCAL config pins its
own `pollSeconds`, which outranks the server (site-specific override for
support techs). Worst-case suspension latency = one poll interval.

### `POST /api/gate-bridge/v1/heartbeat`

Body: `{ "agentVersion": "1.0.0", "os": "windows/amd64", "lastApplyAt": "...",
"lastApplyHash": "...", "ok": true, "detail": "" }`

Cadence (agent 0.6.0): one heartbeat right after every sync cycle, plus one on
the agent's own clock every 60-90 seconds whatever the poll interval is, so an
agent polling every hour still reads online (the server's window is 10
minutes). The clocked heartbeats start once the first cycle has finished.
`lastApplyAt` is omitted until the agent has applied a state (an agent before
0.6.0 sent `"0001-01-01T00:00:00Z"` there).

Server records it as the link's `agent` state (`agent.lastHeartbeatAt` and
the fields above); the console's red offline banner + the N-hours-offline
alert key off it. Since 2026-11-13 this is one targeted write of the agent's
state, pinned to the access key the request was signed with, so a heartbeat
racing a credential rotation never marks the old agent online on the new
pairing. A `lastApplyAt` that is not a date keeps the last good one. The
console's Gate card re-reads only this status (every 30 seconds while a
paired agent is offline), never the whole code list.

### `POST /api/gate-bridge/v1/applied`

Body: `{ "stateHash": "...", "wroteFile": "C:\\PTI\\UPDATE.DAT",
"consumeExit": 0, "consumeOutput": "<first 2KB>" }`

`consumeOutput` (agent 0.6.0) has every run of three or more digits replaced
by asterisks (`result 0:258` reads `result 0:***`): a vendor tool can echo the
lines it imported, and those are gate codes. The server masks it again.
`consumeExit: -2` means the import file was refused or could not start; the
output is then the agent's own sentence (for example the permission refusal
below) and is not masked.

**The system account never runs a file ordinary users can change (agent
0.6.0).** When the Windows service runs the consume file itself (nobody signed
in, so it runs as LocalSystem in session 0), or the agent runs from an elevated
prompt, it first reads the permissions of the file and its folder. When a
broad group (Everyone, Authenticated Users, Users, INTERACTIVE, Guests, Domain
Users and the like) can write, add files to, delete in, or re-permission
either one, or the folder has no permission list at all, the file is NOT run:
`consumeExit: -2`, `ok: false`, and the detail reads, for example, "The import
file's folder (C:\\PTI) can be changed by any user on this PC (Authenticated
Users has write access), so the Bridge won't run Ptisend.bat as the system
account. Limit the folder to Administrators." Run in the signed-in user's
session, the file runs as that user, so no check is needed there.

Audit trail: the console shows "last applied" + consume output per facility.
Recorded the same way as the heartbeat: one targeted write, pinned to the
signing pair.

### `GET /api/gate-bridge/download/:platform` (public, unsigned)

Stable installer URLs for the setup email/console: platforms
`windows-amd64` · `darwin-arm64` · `darwin-amd64` · `linux-amd64`. 302s to the
deploy-configured hosting (`GATE_BRIDGE_DOWNLOAD_BASE`); 404 with a friendly
message until binaries are published. Mounted ABOVE the signing middleware -
an IT person clicks this before any credentials exist.

### `GET /api/gate-bridge/v1/update-check?platform=<key>` (signed)

`{ "latestVersion": "0.6.0", "downloadUrl": "<url>|null", "sha256Url":
"<url>|null" }`. `latestVersion` comes from the backend's
`GATE_AGENT_LATEST_VERSION` env (bumped when binaries are published); URLs are
null until binaries are published. Platform keys match the download endpoint.
The query string is NOT part of the signature base (both sides sign the bare
path).

**Signed releases (agent 0.6.0, 2026-11-20).** The agent no longer trusts the
server to name a binary. It installs an update only when:

- `latestVersion` is a plain `x.y.z`, newer than its own;
- `downloadUrl` is EXACTLY
  `https://github.com/tron-stack/unitrise-gate-bridge/releases/download/v<latestVersion>/<asset>`
  (the release base compiled into the agent; `<asset>` is this platform's
  `unitrise-gate-<os>-<arch>[.exe]`), and `sha256Url` is the `SHA256SUMS` in
  that same folder. A "latest" link, another host or another version's folder
  is refused before anything is downloaded;
- `SHA256SUMS.sig` (fetched beside `SHA256SUMS`) is a valid Ed25519 signature,
  by the release key whose public half is compiled into the agent
  (`release-signing.pub`), over the bytes
  `"unitrise-gate-bridge release v1\nversion <x.y.z>\n"` followed by
  `SHA256SUMS` byte for byte. The version is part of what is signed, so an
  old release can't be offered as a newer one;
- the binary's sha256 matches its line in the signed `SHA256SUMS`.

A build with no compiled-in key (`unitrise-gate version` says so) refuses every
self-update. Downloads are https only, redirects included (GitHub hands
release assets to its storage host). So the backend's update-check must build
`downloadUrl` and `sha256Url` from the versioned folder for
`GATE_AGENT_LATEST_VERSION`, not from the `latest/download` base the installer
links use. Agents before 0.6.0 accept any https URL and check only the
checksum.

The agent NEVER self-replaces on its own: the run loop checks daily and only
logs availability (the console shows the same chip from heartbeat versions);
the swap is `unitrise-gate update`, or the dashboard's Install update button,
run by a person on the site PC. That downloads and verifies as above (any
failure = hard abort, nothing installed), renames the running binary to
`.old`, moves the new one in place, and restarts the service onto it.

## Pairing on the site PC (agent 0.6.0, 2026-11-20)

The local dashboard (`http://127.0.0.1:47810`) has no sign-in, so what it may
do is limited by what an ordinary account on the PC may do:

- **A machine that is already paired** accepts a dashboard re-pair only to
  replace the access key and the access secret, proven live against the server
  it is paired with. A different facility, API endpoint or save folder is
  refused with the Administrator step.
- **An unpaired machine** pairs from the dashboard only with UnitRise's own
  server (`https://api.unitrise.com`), or with a server an Administrator already
  wrote into `config.json`. A developer running the agent with
  `UNITRISE_GATE_DEV=1` may pair it with another server (only an Administrator
  can set the service's environment).
- **The CLI** (`unitrise-gate pair`) sends a key rotation or a first pairing
  with UnitRise's server through the running agent, as before. A different
  facility, `--endpoint` or `--save` is written to the protected `config.json`
  directly, which needs an Administrator prompt, and the running service is
  restarted onto it. `--secret -` reads the secret from standard input
  (`type secret.txt | unitrise-gate pair --secret - ...`) so it stays out of the
  shell's history.
- The dashboard's Gate codes view shows each code masked to its last two
  digits; no local API answer carries a full code.

## The settings folder (agent 0.6.0, 2026-11-20)

The pairing lives in `%ProgramData%\UnitRiseGateBridge\config.json`, read by
the service as SYSTEM. Any account may create a folder in `%ProgramData%`, so:

- both installers create (or reset) that folder BEFORE the service is
  installed or started: owned by Administrators, SYSTEM and Administrators
  full control, inheritance off (`unitrise-gate secure-config`, which
  `install.ps1` runs);
- the service does the same at every start, before it reads anything there,
  the log included;
- `config.json`, the rosters, the own-files record and the log are trusted
  only when the folder is a real folder (not a link) and the folder and each
  file are owned by SYSTEM or Administrators with nobody else able to change
  them. Otherwise the agent removes a link in the folder's place (never what
  it points at), limits the folder, deletes those files, logs and shows why,
  and runs unpaired until it is paired again (the setup-mode rules above).
  A folder that passes keeps its pairing.

## Files the agent writes

Every file the agent replaces (the gate file, `config.json`, the delta
rosters) is written to a randomly named temporary file beside it (created
exclusively, so a planted link is never written through), flushed to disk,
then renamed into place (on Windows the rename writes through; elsewhere the
folder is flushed too). The agent's own default outputs (`UNITRISE-GATE-TEST.txt`
and `GATECODES.TXT`, names no gate software reads) are recorded with a hash,
and once the output moves to another file the agent removes its old one, only
when it is still a plain file holding exactly what the agent wrote. A test
file left by an older agent is recognised by its first line.

## Failure semantics (both sides must honor)

- Agent can't reach the API → keep the last written file untouched; the gate
  keeps admitting from its last list (entry degrades gracefully) - but
  **suspensions stop propagating**, which is why heartbeat visibility is part
  of the minimum product, not polish.
- Server sees no heartbeat for N hours (default 4) → red console banner +
  operator email.
- Render or consume failure → agent reports `ok: false` with detail; server
  surfaces it as a red "Not reaching the gate" state with that detail (also on
  a nonzero `consumeExit` from an older agent) and emails the owner once per
  outage; agent retries next cycle (writes are atomic: temp file + rename,
  never a half-written UPDATE.DAT).
