# UnitRise Gate Bridge — API Contract (v1)

_The shared source of truth between this agent and the UnitRise backend
(truckpark-backend G1 work implements the server side of exactly this).
Version every breaking change; the agent sends its contract version._

## Credentials (three-part, storEDGE-familiar)

Minted per facility in the UnitRise console (Gate hardware card → "Generate
bridge credentials"), revocable there at any time:

- **Access Key** — public identifier (`urgk_` prefix)
- **Access Secret** — HMAC secret, shown once (`urgs_` prefix)
- **Facility ID** — the UnitRise facility ObjectId

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

- `304` — unchanged (the common case; poll is cheap)
- `200`:

```json
{
  "stateHash": "sha256-of-canonical-state",
  "facility": { "name": "...", "address": "...", "phone": "..." },
  "provider": "unitrise_test",
  "settings": {
    "generatedFileName": "UPDATE.DAT",
    "consumeCommand": "Ptisend.bat",
    "defaultTimeZone": 1,
    "pollSeconds": 300
  },
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

Semantics: **full state, every time.** The list is the complete set of codes
that should exist at the gate. Suspended codes are included with
`status: "suspended"` for renderers whose format expresses lockout;
formats that can't express it simply omit suspended codes (removal = lockout).
Revoked/expired codes are never present.

`stateHash` is computed server-side over the canonical credential list +
settings, so any change (issue, suspend, restore, revoke, rename, settings
edit) changes the hash and wakes the agent's next poll.

Poll cadence: the server's `settings.pollSeconds` (console-tunable, 60–3600,
default 300) sets the agent's cycle — unless the agent's LOCAL config pins its
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

### `GET /api/gate-bridge/v1/update-check` (stub in v1)

`{ "latestVersion": "1.0.0", "downloadUrl": null }` — reserved for
storEDGE-style auto-update; the agent only reports availability in v1,
it does not self-replace.

## Failure semantics (both sides must honor)

- Agent can't reach the API → keep the last written file untouched; the gate
  keeps admitting from its last list (entry degrades gracefully) — but
  **suspensions stop propagating**, which is why heartbeat visibility is part
  of the minimum product, not polish.
- Server sees no heartbeat for N hours (default 4) → red console banner +
  operator email.
- Render or consume failure → agent reports `ok: false` with detail; server
  surfaces it; agent retries next cycle (writes are atomic: temp file + rename,
  never a half-written UPDATE.DAT).
