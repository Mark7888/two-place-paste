# Plan — protocol compatibility between relays and apps

**Status:** in progress. PR 1 (proto) adds the schema and SPEC §5.3.
**Raised by:** an upcoming change to pairing that hardens its security and
cannot be made backwards compatible. Before it lands, apps and relays need a
way to tell each other, and the user, that they no longer fit.
**Touches:** `/proto/**`, `/spec/**` (SPEC §5), `/server/**`,
`/pkg/tppclient/**`, `/desktop/**`, `/mobile/**`, `/docs/site/**`.

---

## 1. The rule this plan serves

Production relays run releases; `beta` relays are for testing
(`docs/site/.../self-hosting/docker.mdx`, *Image tags*). People may run Beta
apps against a released relay to get fixes sooner. So:

| Pairing | Promise |
|---|---|
| **Newer app, older relay** | Always works. The app finds out what the relay offers and does without what it lacks. |
| **Older app, newer relay** | Works as far as possible. A change that cannot stay compatible only ships in a Stable release, and the old app is told plainly to update. |
| Older app, `beta` relay | Not promised. |

Almost every change stays compatible in both directions, as `header_v2`
already does (`EntryLimitsResponse`, crypto.md §5.5.1). A break is rare and
deliberate: a few times in the project's life. This plan is the machinery for
those few times, and the feature detection that keeps the rest compatible.

## 2. What happens today

Nothing is negotiated. An app dials `GET /ws?device_id=…`, the socket
upgrades, and frames flow. Neither side knows the other's version.

| When the relay | Desktop (Go core) | Android |
|---|---|---|
| refuses the upgrade with 401 | `ErrRevoked`: stops for good, says *revoked* | cannot see the status; re-probes with a plain GET; *probably revoked* after two |
| refuses with 410 | *group deleted*, stops for good | *group deleted* |
| refuses with any other status | generic error, retries every 0.5–30 s | still ends at *probably revoked* |
| accepts, then closes | backoff resets on every connection (`client.go` `supervise`): about 2 dials a second | similar |
| answers a request with `ERROR_CODE_CLIENT_OUTDATED` | *the server needs a newer version of this app* — **store requests only** (`service/store.go`); everything else, pairing included, says *the service could not …* | *The server needs a newer version of this app.* (`errors.ts`) |
| sends a frame type the app does not know | ignored (`onEvent` switch, `conn.deliver`) | ignored (`client.ts` `onEvent`) |
| lacks a message the app sends | answers `ERROR_CODE_UNSUPPORTED_MESSAGE` (Envelope contract) | same |

Two things follow. A break in the *connection step* would make today's apps
report a revocation that never happened. A break in *one kind of request*,
answered with `ERROR_CODE_CLIENT_OUTDATED` on an open socket, is handled
almost gracefully already. The pairing change is the second kind, which is
why it can land gently.

How the user updates:
- **Desktop:** the signed auto-updater (Stable/Beta/Nightly, about every 6
  hours). It talks to GitHub, not the relay, so it works while the relay is
  incompatible.
- **Android:** no updater; builds are on the releases page.

## 3. Mechanism

### 3.1 A protocol level, plus capabilities

- **Protocol level:** one integer per build, starting at **1** for everything
  shipped so far. It goes up only for a break. Each relay accepts levels
  `[min, max]`; `min` is compiled in and rises only in a Stable release that
  needs it. There is no setting to lower it: a break exists for a reason,
  usually security.
- **Capabilities:** a set of relay features, for the compatible changes. A
  new app checks for a capability before using a feature, and does without it
  (or explains why it cannot) when the relay lacks it. Numbers are permanent,
  like every enum in the schema. `header_v2` stays where it is; new features
  use this set.

The level is not the app's version. Stable, Beta, Nightly and `0.0.0-local`
builds of the same code all share one level.

### 3.2 App → relay: the level on the URL

The app adds `proto=<level>` to the `/ws` URL, on authenticated and
unauthenticated (pairing) sockets alike. No parameter means level 1, which
covers every app shipped before this plan.

The relay keeps the level with the connection. For a known device it also
stores the last level it connected with, next to last-seen. That is the only
new thing the relay learns about a device: a small number, no app version,
no platform.

The level is on the URL rather than in a frame so the relay can act on it
**before the upgrade**. That matters only if a future break is in the
connection step itself (§3.4).

### 3.3 Relay → app: `HelloRequest` / `HelloResponse`

A new request, sent first on every socket:

```proto
message HelloRequest {}
message HelloResponse {
  uint32 protocol_min = 1;
  uint32 protocol_max = 2;
  repeated Capability capabilities = 3;
}
enum Capability {
  CAPABILITY_UNSPECIFIED = 0;
  // CAPABILITY_PAIRING_V2 = 1; added by the pairing change itself
}
```

It is a request rather than a frame the relay pushes, because an old relay's
answer is then **definite**: it answers `ERROR_CODE_UNSUPPORTED_MESSAGE`,
which the app reads as *level 1, no capabilities*. A pushed frame would leave
the app guessing how long to wait for something an old relay never sends. It
is legal unauthenticated: a joiner needs it before it pairs. The app sends it
together with its first real requests; it does not have to wait for the
answer except before a feature that depends on a capability.

`HelloResponse` carries nothing secret, so answering it on an unauthenticated
socket gives an anonymous caller nothing beyond the relay's level and feature
list. An admin's `/admin/version.json` already discloses more.

### 3.4 Refusing an old app

There are two places to refuse, from mild to severe:

1. **One request.** The relay answers a request the app's level may no
   longer make with `ERROR_CODE_CLIENT_OUTDATED`. The socket stays open and
   everything else keeps working. This is what the pairing change uses.
   No new error code is needed: code 14's meaning ("the request is one this
   client's version cannot make correctly any more") already fits.
2. **The connection.** If the level is below `min`, the relay refuses the
   upgrade with **426 Upgrade Required** and a small JSON body
   `{"protocol_min":2,"protocol_max":3}`. The Go core reads it directly.
   Android's existing plain-GET probe (`probeRefusal`) hits the same URL and
   gets the same 426, so it can treat it as definite, as it does 410. Either
   app then stops redialling on the normal backoff, shows *update the app*,
   and tries again hourly or when the user asks. **Never** report it as
   revoked.

   Today's apps send no level and so count as level 1. If `min` ever goes
   above 1, they get the 426 and react as the table in §2 says: the desktop
   retries with a generic error, and Android says *probably revoked*. Nothing
   can fix that for builds already installed. It is the reason connection
   breaks should be the rarest of all, and why the pairing change does not
   need one.

### 3.5 A relay that is too old

When `HelloResponse` lacks a capability the user is trying to use, or
`protocol_max` is below the app's own minimum, the app says the relay needs
updating. Such a message names the operator's website (the group-info link
added in #69) when there is one, because the user usually cannot update the
relay themselves. The rest of the app keeps working; that is the "newer app,
older relay" promise.

### 3.6 Pairing codes

Codes travel from device to device, outside the relay, so the relay cannot
version them. Today's apps decode a `PairingCode` and, finding no member they
know, fall back to reading the bytes as a bare `PairingPayload`. A code of a
new kind then decodes into an empty URL and token and fails somewhere later,
with an unhelpful message.

From this plan on:
- **A new kind of code is a new `oneof` member** of `PairingCode`, never a
  change to an existing one.
- **An app that decodes a `PairingCode` with no member it knows, but with
  unknown fields** (both protobuf libraries keep them), says *this code was
  made by a newer version of TwoPlacePaste; update this app*, instead of
  falling back.

### 3.7 Messages

| Situation | Desktop | Android |
|---|---|---|
| Relay refused this request (`CLIENT_OUTDATED`) | *The relay needs a newer version of this app.* + **Check for updates** | same text |
| Relay refused the connection (426) | same, as a banner | same, as a banner |
| Relay lacks a capability | *This relay is too old for …; ask its operator to update it.* + operator's website | same |
| Code from a newer app | *This code was made by a newer version …* | same |

**Check for updates** runs the desktop updater immediately rather than at its
next scheduled check. If the user's update channel has nothing newer, the
message says so rather than just "update". The desktop's general
`relayError` mapping gains `ErrClientOutdated`, which today only the store
screens handle.

## 4. The pairing change, at level 2

The hardening changes how new devices pair; devices already in a group are
not affected. So the relay **does not** raise `min`. It refuses only the
pairing requests from level-1 sockets:

- `PairingStartRequest`, `PairingJoinRequest`, `PairingWrappedKeyUpload`,
  `PairingOfferRequest`, `PairingOfferAcceptRequest`, or whichever of them
  the change replaces;
- answered with `ERROR_CODE_CLIENT_OUTDATED`.

It also advertises the new pairing capability in `HelloResponse`.

| Member (shows or accepts the code) | Joiner | Relay | Result |
|---|---|---|---|
| new | new | new | new pairing |
| old | any | new | member's pairing request refused: *update this app* |
| any | old | new | joiner's request refused: *update this app* (an old joiner may fail earlier, decoding the new code; see §3.6) |
| new | new | old | see §6 |
| old | old | old | old pairing, as today |

Sync, history, store, rekey and revocation are unchanged at every
combination: an old device that is already paired keeps working with a new
relay.

Today's apps predate §3.7, so on a new relay their pairing fails with:
- desktop: *the service could not start pairing*;
- Android: *The server needs a newer version of this app.*

That is acceptable for the people currently using the app, who can be asked
to update directly.

## 5. Order of work

1. **Compatibility machinery** (shared contract, its own PR):
   - `proto/`: `HelloRequest`/`HelloResponse`, `Capability`, the message
     types; `make proto`.
   - SPEC §5.3 *Versions and compatibility*: §1's rule, §3.
   - Relay: `proto=` parsed and stored, `Hello`, the 426 path (unreachable
     while `min` is 1, but tested), the level on the admin device list.
   - Go core and TS client: `proto=1`, `Hello` on connect, capability
     lookups, `ErrRelayOutdated`, 426 treated as definite, the unknown-code
     message.
   - Desktop: `ErrClientOutdated` in `relayError`, the banner, *Check for
     updates*.
   - Android: the same messages.
   - Docs site: troubleshooting entries for each message.
2. **The pairing change** at level 2, its capability and the per-request
   refusal (§4), in one Stable release with both apps.

Step 1 can ship in the same release as step 2. Today's apps gain nothing
from it shipping earlier, because they predate it either way. What step 1
buys is the *next* break, so it must not be skipped.

## 6. Open question — a new app on an old relay, when pairing

§1 promises that a newer app works with an older relay. For pairing, the
only way to keep that promise is to **fall back to the old pairing**, and
that undoes the hardening: anyone who can make the relay look old (or who
runs an old relay) would get the weaker scheme. Whether that is acceptable
depends on what the change protects against:

- If it protects against the **relay itself** or the network, fallback
  defeats it. A new app should refuse to pair through an old relay
  (*update the relay to pair new devices*) while sync keeps working.
- If it protects against something an old relay cannot be used for, fallback
  may be fine for a transition period.

The recommendation is **no fallback**: pairing is the one place §1's promise
gives way, and the message says why.

## 7. Tests

- Relay: `proto=` absent, 1, 2, garbage; 426 body; `Hello` on authenticated
  and unauthenticated sockets; level-1 pairing requests refused, level-2
  allowed; last-seen level stored.
- Go core and TS client: `Hello` from a relay that predates it
  (`UNSUPPORTED_MESSAGE` → level 1); 426 → `ErrClientOutdated` and **not**
  revoked; Android's probe on 426; unknown-code decoding.
- `npm run interop` with an old-level and a new-level client against one
  relay, covering §4's table.
- Desktop: `relayError` maps `ErrClientOutdated` on every path, not just the
  store.
