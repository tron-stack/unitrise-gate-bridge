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
- **`delta`** - op-coded change lines against the roster this agent last
  applied (`addedLine`/`changedLine`/`removedLine`, e.g. WinSen's A/E-prefixed
  update.txt). The roster persists at `<config dir>/last-roster.json` and is
  committed only after the vendor file lands on disk. A suspension that the
  format can't express (`suspendedLine` blank) emits as a REMOVE op, and the
  code leaves the roster so a later restore re-emits as an add. A blank
  `removedLine` means removals are inexpressible and are skipped.

Every line - including the last - ends with the configured line ending
(`crlf` default; DOS-lineage importers require the trailing carriage return).

**Force semantics:** the state carries `forceNonce`. When it differs from the
roster's remembered nonce (console "Force full update"), or the agent's local
Force button was pressed, delta mode treats the roster as empty and re-emits
the entire roster as adds; full mode simply rewrites. This is also the recovery
path when a consume run fails after the file was written.

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

`{ "latestVersion": "0.3.0", "downloadUrl": "<base>/<file>|null", "sha256Url":
"<base>/SHA256SUMS|null" }`. `latestVersion` comes from the backend's
`GATE_AGENT_LATEST_VERSION` env (bumped when binaries are published); URLs are
null until `GATE_BRIDGE_DOWNLOAD_BASE` is set. Platform keys match the
download endpoint. The query string is NOT part of the signature base (both
sides sign the bare path).

The agent NEVER self-replaces on its own: the run loop checks daily and only
logs availability (the console shows the same chip from heartbeat versions);
the swap is `unitrise-gate update`, run by a person on the site PC. That
command downloads the platform binary, verifies it against `SHA256SUMS` when
published (mismatch = hard abort), renames the running binary to `.old`, moves
the new one in place, and prints the service-restart step.

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
