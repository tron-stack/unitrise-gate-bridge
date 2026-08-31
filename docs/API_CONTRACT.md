# UnitRise Gate Bridge - API Contract (v1)

_The shared source of truth between this agent and the UnitRise backend
(truckpark-backend G1 work implements the server side of exactly this).
Version every breaking change; the agent sends its contract version._

## Credentials (three-part, storEDGE-familiar)

Minted per facility in the UnitRise console (Gate hardware card → "Generate
bridge credentials"), revocable there at any time:

- **Access Key** - public identifier (`urgk_` prefix)
- **Access Secret** - HMAC secret, shown once (`urgs_` prefix)
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
bad signature, revoked credentials. All bridge endpoints are additionally
rate-limited per key.

## Endpoints

### `GET /api/gate-bridge/v1/state`

Headers: signing headers + optional `If-None-Match: "<stateHash>"`.

- `304` - unchanged (the common case; poll is cheap)
- `200`:

```json
{
  "stateHash": "sha256-of-canonical-state",
  "facility": { "name": "...", "address": "...", "phone": "..." },
  "provider": "template",
  "settings": {
    "generatedFileName": "UPDATE.DAT",
    "consumeCommand": "Ptisend.bat",
    "defaultTimeZone": 1,
    "pollSeconds": 300,
    "format": {
      "preset": "pti_falcon",
      "mode": "full",
      "header": "",
      "line": "{code},{unit},{tenant},{tz}",
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
      "timeZoneGroup": 1
    }
  ]
}
```

`provider` selects the agent's renderer: `"template"` (the server carries a
renderable `settings.format`) or `"unitrise_test"` (the verification file -
file_bridge's default until a format is configured).

### `settings.format` - the server-driven vendor template

Vendor gate-file layouts are per-site artifacts (specs are partner-only paper
that drifts between versions), so the layout is DATA the console edits, not
agent code. A format fix reaches the site on the next poll; the agent binary
never changes for it.

Placeholders, usable in every template string:

| Placeholder | Meaning |
|---|---|
| `{code}` `{unit}` `{tenant}` `{tz}` `{status}` | per-credential fields |
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
formats that can't express it simply omit suspended codes (removal = lockout).
Revoked/expired codes are never present.

`stateHash` is computed server-side over the canonical credential list +
settings, so any change (issue, suspend, restore, revoke, rename, settings
edit) changes the hash and wakes the agent's next poll.

Poll cadence: the server's `settings.pollSeconds` (console-tunable, 60–3600,
default 300) sets the agent's cycle - unless the agent's LOCAL config pins its
own `pollSeconds`, which outranks the server (site-specific override for
support techs). Worst-case suspension latency = one poll interval.

### `POST /api/gate-bridge/v1/heartbeat`

Body: `{ "agentVersion": "1.0.0", "os": "windows/amd64", "lastApplyAt": "...",
"lastApplyHash": "...", "ok": true, "detail": "" }`

Server stamps `agentHeartbeatAt` on the facility's gate link; the console's
red offline banner + the N-hours-offline alert key off it.

### `POST /api/gate-bridge/v1/applied`

Body: `{ "stateHash": "...", "wroteFile": "C:\\PTI\\UPDATE.DAT",
"consumeExit": 0, "consumeOutput": "<first 2KB>" }`

Audit trail: the console shows "last applied" + consume output per facility.

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
  surfaces it; agent retries next cycle (writes are atomic: temp file + rename,
  never a half-written UPDATE.DAT).
