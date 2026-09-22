# TwoPlacePlace — Specification

**Version:** 0.1 (draft)
**Status:** Design complete for MVP, not yet implemented
**Last updated:** 2026-09-07

A self-hosted, end-to-end encrypted shared clipboard. Copy on one device, paste on
another. The server relays ciphertext and never sees clipboard content.

---

## 1. Overview

### 1.1 Components

| Component | Stack | Purpose |
|---|---|---|
| Server | Go, Redis | Relay, group/device registry, admin UI |
| Mobile app | React Native (Android) | Manual sync, quick-settings tile, history browser |
| Desktop service | Go | Background tray service, localhost web UI |

### 1.2 Platform scope

**In scope for MVP:** Windows, macOS, Android.
**Deferred:** iOS (React Native structure should anticipate it), Linux desktop.

### 1.3 Terminology

- **Group** — a set of devices belonging to one person that share a clipboard.
  Groups are fully isolated; there is no cross-group interaction.
- **Device** — one installation of a client, holding a keypair and the current group key.
- **Creation token** — a one-time admin-issued token required to create a group.
  Once consumed, the token value becomes the group's identifier.
- **Entry** — a single clipboard item (text, image, or file) stored as ciphertext.
- **Epoch** — a monotonically increasing counter identifying the current group key
  generation. Incremented on every rekey.

---

## 2. Security model

### 2.1 Threat model

The server is **untrusted with content** and **trusted with metadata**. It stores and
relays ciphertext, enforces group membership and size limits, and knows device counts
and timestamps. It cannot decrypt any clipboard entry.

Since the deployment is self-hosted and single-user, a malicious server is a low
priority, but the design does not require trusting it for confidentiality.

### 2.2 Key hierarchy

- Each **device** generates an asymmetric keypair on first launch. The private key
  never leaves the device.
- Each **group** has one symmetric key, shared by all member devices. It is rotated
  on every device revocation.
- Each **entry** is encrypted with AEAD using a fresh random nonce, keyed from the
  group key. The group key is never used directly as a content key.
- The group key is stored server-side only in **wrapped** form: one copy per device,
  encrypted to that device's public key.

Per-message keys wrapped per device were considered and rejected: for a single user
with 2–4 devices and roughly two revocations a year, the added roster-trust
requirements are not worth the marginal revocation benefit.

### 2.3 What the server sees

Ciphertext, byte sizes, timestamps, device identifiers, wrapped key blobs, group
membership. It does **not** see clipboard content, content type, or plaintext size
(padding is not required for MVP but the format should not preclude it).

---

## 3. Group lifecycle

### 3.1 Creation

1. Admin generates a named creation token in the admin UI.
2. Admin sends the user a URL: `https://tpp.example.com/<token>`
3. User selects **New Group** on any client and provides that URL.
4. Client generates its keypair and a fresh group key, wraps the group key to its
   own public key, and `POST`s to create the group.
5. The token is consumed and becomes the group identifier. The token record is
   marked used and cannot create another group.

`GET /<token>` returns **404**. The token is only consumed by the creating `POST`, so
link previews and crawlers cannot burn it.

One token creates exactly one group. There is no token reuse and no leash: after
creation the group is independent of the token that made it.

### 3.2 Pairing a new device

Pairing works identically for every direction (desktop→mobile, mobile→desktop,
desktop→desktop). Transport of the payload differs — QR code or copy-pasted token
string — but the payload and protocol are the same.

**Payload contents:**
- Server URL
- Short-lived pairing token
- Inviter's ephemeral public key

**Flow:**

1. An already-paired device (**inviter**) generates an ephemeral keypair and a
   pairing token, registers the token with the server, and displays the payload as a
   QR code and as a copyable string.
2. The **joiner** scans or pastes it, generates its own device keypair, and sends its
   public key to the server referencing the pairing token.
3. The server notifies the inviter over its open WebSocket.
4. The inviter wraps the **current** group key to the joiner's public key and uploads
   the wrapped blob. The server relays it; it cannot unwrap it.
5. The joiner is now a group member at the current epoch.

Pairing tokens are short-lived (suggested: 5 minutes) and single-use.

**New devices start empty.** They cannot decrypt entries created before they joined
and do not attempt to fetch them.

#### Joiner-emitted pairing

The flow above is member-emitted: the device that holds a group key shows the
code. The same exchange also runs with the roles swapped, so that the device
*without* a key can be the one that shows something — a phone generating a code
a paired desktop pastes, or a desktop showing a QR a paired phone scans. Which
way round the code travels is a question of which screen the user is looking
at, not of the protocol, and both directions end in the same `PairingComplete`.

**Offer contents:** server URL · short-lived offer code · joining device's
public key · joining device's name.

**Flow:**

1. The **joiner** learns the relay URL (it has no other way to know one: it
   holds no group), dials an unauthenticated socket and asks the server to hold
   an offer. The server answers with a single-use code and keeps the socket
   open.
2. The joiner displays the offer as a QR code and as the same copyable string.
3. A **member** scans or pastes it. Before anything is wrapped, its UI shows the
   joining device's name and a fingerprint of its public key, and the user
   confirms — the rule of §3.3 step 2, applying here for the same reason: this
   step admits a device to the group.
4. The member wraps the **current** group key to the public key *from the code*
   and sends it with the offer code. The server checks that the key matches the
   stored offer byte for byte, registers the device in the member's group,
   consumes the offer atomically and pushes the wrapped key to the joiner's
   waiting socket.
5. The joiner is now a group member at the current epoch, exactly as in step 5
   above.

The byte-for-byte check is what keeps the server outside the trust path: the
member wraps to a key it read out of band, and a server that substituted one
would have to make it match an offer it did not create.

Offers are short-lived and single-use on the same terms as pairing tokens, and
one offer admits exactly one device. Accepting an offer is legal only on an
authenticated connection.

**The risk moves.** A hostile *pairing token* costs a joiner nothing: it holds
no key to lose. A hostile *offer* is accepted by a member, who hands over the
group key — so the confirmation in step 3 is not advisory. A client MUST NOT
wrap anything until the user has confirmed a dialog naming the device and
showing a fingerprint of its public key.

### 3.3 Revocation and rekey

Any device in a group may revoke any other device. There are no permission levels —
all devices belong to the same person.

1. The revoking device requests the current device roster from the server.
2. The client **displays a confirmation listing the devices it is about to re-key
   for**, by name. The user confirms.
3. The revoking device generates a new group key and wraps it once per remaining
   device using each device's stored public key.
4. It uploads all wrapped keys **in a single request**. The server applies them and
   increments the epoch **atomically** (Redis `MULTI` or a Lua script) — either every
   device receives the new epoch, or none do. A client dying mid-upload leaves the
   group at the old epoch, unchanged.
5. The revoked device's session is terminated and its credentials invalidated.

Remaining devices do **not** need to be online. Their wrapped key waits for them; they
pick it up on next connect.

**Old entries are not deleted.** Clients skip any entry whose epoch is older than
their current one, and those entries expire naturally within 24 hours.

**The local clipboard is never touched by a rekey.** If a device has `asd123` on its
OS clipboard, it stays there.

**Known limitation:** revocation requires a device holding the current group key. A
user with only one remaining device that has lost access should leave and create a new
group. Nothing of value is lost given the 24-hour TTL.

---

## 4. Server

### 4.1 Deployment

Docker Compose: one Go server container, one Redis container. Configuration via `.env`.

TLS is expected to be terminated by a reverse proxy in front of the server.

### 4.2 Redis usage

Redis is the only datastore, but it holds **two zones with different rules**:

**Persistent zone** — group records, device records, public keys, wrapped group keys,
epochs, creation tokens. No TTL. Must survive restarts; requires AOF persistence.

**Ephemeral zone** — clipboard entries and inline blobs. 24-hour TTL from creation.

Set `maxmemory-policy` to **`volatile-lru`**. `allkeys-lru` would silently evict group
records under memory pressure and destroy pairings.

Suggested key layout (illustrative, not binding):

```
group:<group_id>                    hash    epoch, created_at
group:<group_id>:devices            set     device ids
device:<device_id>                  hash    group_id, name, pubkey, created_at, last_seen
device:<device_id>:wrapped_key      string  group key wrapped to this device
group:<group_id>:entries            zset    entry ids scored by created_at
entry:<entry_id>                    hash    epoch, size, created_at, storage_ref   [TTL 24h]
entry:<entry_id>:blob               string  inline ciphertext if ≤256KB           [TTL 24h]
token:<token>                       hash    name, created_at, used, group_id
pairing:<pairing_token>             hash    group_id, inviter_device_id           [TTL 5m]
```

### 4.3 Storage of blobs

- Ciphertext **≤ 256 KB** is stored inline in Redis.
- Larger payloads go to a **blob backend** behind a Go interface — roughly
  `Put(entryID, expiresAt, r) (ref, error)`, `Get(ref)`, `Delete(ref)`, `Sweep(now)`.
  `expiresAt` is passed in at write time because reclamation depends on it (§4.5).
  - MVP implementation: local disk.
  - Planned: S3-compatible object storage. The interface exists from day one so this
    is a new implementation, not a refactor.
- The Redis entry holds only a reference.

**Hard cap: 10 MB per entry, measured on the ciphertext** — the size the server
actually receives. The server has no knowledge of plaintext size.

Redis expiry removes the entry record but does not touch the stored blob. Blob
reclamation is specified in §4.5.

### 4.4 Admin UI

Served on the public domain. Credentials from `.env`.

**Scope:** a single screen listing creation tokens — name, created date, whether used,
and the resulting group if used. Token generation. Nothing else.

No content access, by construction. No usage statistics in MVP.

**Requirements:**
- Session cookie: `HttpOnly`, `SameSite=Strict`.
- Rate limiting on the login endpoint. This UI is publicly reachable.
- Each token rendered as a QR code containing the full `https://host/<token>` URL, so
  first-device onboarding is a scan rather than typing a URL on a phone keyboard.

**Password handling:** MVP reads a plaintext password from `.env`. The config loader
should accept a hash if one is supplied, so moving to hashed storage is a config
change rather than a code change. *(Tracked as deferred work.)*

### 4.5 Blob garbage collection

Blobs are reclaimed by **expiry-bucketed paths**, not by reference counting.

Because the TTL is fixed at 24 hours and is never extended, a blob's expiry time is
fully determined the moment it is written. That expiry is encoded into the storage
path, which removes the need to ever ask whether a blob is still referenced.

> **Invariant:** entry lifetimes are immutable. There is deliberately no pin, favourite,
> or extend-TTL feature. Introducing one would invalidate this entire scheme — a pinned
> blob would have to be relocated between buckets, or reclamation would have to fall
> back to real reference tracking. Do not add such a feature without redesigning §4.5.

**Path layout**

```
blobs/<YYYYMMDDHH>/<entry_id>.bin
```

where `<YYYYMMDDHH>` is the **UTC hour bucket in which the blob expires**, computed as
`created_at + 24h` rounded up to the hour. All timestamps are UTC; local time and DST
must not be involved.

**Sweep algorithm**

1. List the immediate child directories of `blobs/`.
2. Parse each directory name as an hour bucket.
3. For any bucket whose hour has fully passed, `RemoveAll` the directory.

The sweep reads roughly 25 directory names regardless of how many blobs are stored. It
performs **no Redis queries** and never stats individual files. Cost is independent of
total blob count.

**Schedule**

- Once at server startup.
- Every 10 minutes thereafter, on a background goroutine.

The startup sweep means arbitrary downtime produces no accumulated garbage — every
bucket that expired while the server was down is removed in a single pass.

**Write ordering**

Write the blob **before** writing the Redis entry. If the server crashes in between,
the resulting orphan sits in a bucket that will be deleted within 24 hours regardless.
No crash-recovery logic is required.

**Explicitly deleted entries**

A blob whose entry is deleted early remains on disk until its bucket expires. This is
bounded by the 24-hour TTL and the 10 MB per-entry cap, so the waste is small. An
optional fast-path unlink on explicit delete may be added; because the bucket sweep is
the backstop, that fast path is permitted to fail silently.

**S3 backend**

Object stores handle this natively via lifecycle expiration rules configured on the
bucket. The S3 implementation of `Sweep()` is a **no-op**. Lifecycle granularity is one
day rather than one hour, so objects may persist somewhat past their TTL; this is
harmless, as Redis is the sole authority on entry visibility.

**Manual verification**

A `tpp gc --verify` command performs a full mark-and-sweep cross-check between stored
blobs and Redis entries and reports orphans. This is a **diagnostic tool for use after
Redis data loss or migration**, run by hand. It is never scheduled and is not part of
the normal reclamation path.

---

## 5. Transport

### 5.1 Protocol

**WebSocket carrying protobuf-encoded binary frames**, for all clients: Android,
desktop service, and the desktop's localhost web UI.

gRPC was evaluated and rejected: React Native cannot speak native gRPC without a
maintained native module per platform, and browser clients would require a grpc-web
proxy in front of the server — unacceptable complexity for a self-hosted single-binary
deployment. Protobuf message definitions are retained, so migrating the envelope later
costs nothing.

Binary frames also avoid the ~33% base64 overhead that a JSON transport would impose
on blobs.

### 5.2 Connection behaviour

- **Desktop:** persistent WebSocket while the service runs.
- **Android:** connected while the app is foregrounded. Sync outside that window is
  triggered by the quick-settings tile.

Push notification delivery (FCM / UnifiedPush / other) is **out of scope for MVP** and
remains an open design question — see §9.

---

## 6. Sync semantics

- Sync operates on the **latest entry only**.
- History is browsable in-app, and any historical entry can be explicitly copied to
  the local clipboard on demand.
- **Conflict resolution:** last write to reach the server wins.
- **Direction:** if the local clipboard is newer than the server's latest entry,
  upload; otherwise download. Where timestamp comparison isn't reliable on a platform,
  clients expose two explicit directional buttons instead.
- No client pulls anything on reconnect or in the background, with one opt-in exception
  below. History is fetched when the user opens the History tab — opening it *is* the
  request — and at no other time.
- When a device writes an entry, the relay tells the group's other connected devices
  (`EntryAdded`, metadata only). A client ignores it unless its user has turned on
  applying remote entries (desktop: off by default); then it fetches that entry and
  writes it to the local clipboard, unless the local clipboard is already newer. A
  device that was offline is not told afterwards.
  A listing is entry metadata only; an entry's body is fetched when the user asks for
  that entry, by copying it or by opening its preview.
- Where a client cannot make the timestamp comparison, it **asks** rather than reporting
  that it cannot decide: the two directions are put to the user as a choice, with what
  each one overwrites, and the sync then proceeds in the direction chosen. Refusing to
  act and refusing to ask are not the same thing.

---

## 6a. Pairing links

A pairing code is a base64url string. Shown as a QR code it is readable only by
this system's own clients: a general-purpose scanner — Google Lens, a phone
camera — renders it as text to copy and offers nothing to open.

Clients therefore show the code **wrapped in a link to the relay the code
already names**:

```
https://<that group's relay>/pair#<code>
tpp://pair#<code>            (the same code, straight into the Android app)
```

Three properties, each load-bearing:

- **The code is in the fragment.** A browser never sends a fragment, so the
  relay hosting the link does not receive the code its own page hands over, and
  it reaches no access log, proxy log or `Referer`.
- **No domain is pinned anywhere.** The host comes from the payload at run
  time, because every deployment is somebody's own relay. The Android
  intent-filter matches `tpp://pair` — a scheme, not a host. An `https`
  intent-filter is not usable here at all: Android honours `pathPrefix` only
  alongside a concrete `host`, so a host-less one would register the app as a
  handler for every web link on the device.
- **Every form still decodes.** Both clients strip the envelope before
  decoding, so a bare code, an `https` link, a `tpp://` link and any of those
  with whitespace around them are one input. A code from a build that predates
  this is still read, and a code this build shows is still pasteable into one.

The relay serves `GET /pair`: a static page that reads the fragment client-side
and offers "Open in TwoPlacePaste", plus the bare code to copy for anyone
without the app.

---

## 7. Clients

### 7.1 Android

**Clipboard access constraint:** Android 10+ blocks clipboard reads from apps that are
not focused and are not the default IME. Background clipboard watching is therefore
not possible without an accessibility service, which is not acceptable.

The **quick-settings tile** is the primary mechanism: tapping it brings a window of the
app forward, which is a legal moment to read the clipboard.

That window is a **transparent panel**, not the app. Focus is all the platform rule
asks for, so the tile opens a dedicated activity that dims the screen behind it and
draws a small panel in the middle — the user keeps their place in whatever they were
doing. The panel shows the sync running, asks which direction to take when §6's
comparison cannot be made, reports the outcome, and closes itself.

The panel and the app are two activities over **one** session: the client, the socket
and the tile handler are opened by the JavaScript bundle rather than by a screen, so a
tap is answered whether or not the app is mounted, and never by a second client.

**Features:**
- Quick-settings tile for one-tap sync, answered in a panel over the current screen
- Manual sync button in-app
- History tab, listed when the tab is opened; tap an entry to preview it — text
  and images, at the current epoch only — and copy it locally from there
- Device management: list group devices, revoke (with the confirmation from §3.3)
- Pairing: show QR, scan QR, paste code, copy the code to the clipboard

Auto-sync is deferred and may prove infeasible on Android; if so, it is dropped rather
than worked around.

### 7.2 Desktop service (Windows, macOS)

A background service with a tray icon. All UI is React, prebuilt and embedded in the Go
binary, served from localhost and opened in the user's default browser.

**Features:**
- Tray menu: sync now, open UI, quit
- Optional clipboard auto-watch (toggle, off by default)
- Optional applying of entries other devices upload to the local clipboard (toggle, off
  by default; §6)
- Manual sync, with the direction asked for when it cannot be worked out
- History browser, listed when the tab is opened, with a preview of text and image
  entries at the current epoch
- Device management and pairing (show QR, scan not applicable — paste token)

**Autostart on login:** user setting, **default off**. Implemented via launchd plist on
macOS and a registry Run key on Windows.

**Localhost UI security.** Any website in the user's browser can issue requests to
`127.0.0.1`. Therefore:

- Bind explicitly to `127.0.0.1`, **never** `0.0.0.0`. Binding to all interfaces would
  also trigger a Windows firewall prompt.
- Fixed port. **Suggested: 47821** (dynamic/private range, low collision risk).
- On bind failure, the tray displays an error and allows the port to be overridden via
  a config file. The service must not fail silently.
- The tray opens `http://127.0.0.1:47821/app?token=<short-lived-token>`. The token is
  generated per launch and required on every request that reads or changes something —
  the API and the event stream. It is **not** required for the static shell (the page,
  its script, its stylesheet): a browser cannot attach a token to a `<script>` or a
  `<link>`, the bundle is the same bytes for every user, and requiring one there only
  stops the page from loading at all.
- Validate the `Origin` header on every request: a foreign one is refused everywhere.
  Do **not** require its presence. A browser attaches `Origin` to a cross-origin request
  and to any request whose method is neither `GET` nor `HEAD`; a same-origin `fetch` that
  reads attaches none, so requiring it refuses the app's own reads. Where `Origin` is
  absent, refuse anything that is not a read, and refuse a `Sec-Fetch-Site` that says the
  request came from elsewhere.

### 7.3 iOS (deferred)

Not in MVP scope. iOS forbids background clipboard reads entirely and surfaces a
"pasted from" banner on every read, so any future implementation is manual-only, likely
supplemented by a share-sheet extension. The React Native project structure should keep
this possible without restructuring.

### 7.4 Linux desktop (deferred)

Not in MVP scope. Requires materially more work than macOS/Windows:

- **Tray:** no universal standard. StatusNotifierItem over DBus covers KDE and most
  desktops; GNOME requires a user-installed extension. A CLI plus the localhost URL
  must exist as a fallback entry point.
- **Clipboard:** X11 and Wayland are separate implementations. On X11 the clipboard is
  owned by the source process and dies with it, so the service must hold the selection.
  On Wayland, unfocused clipboard access requires `wlr-data-control`, supported by
  wlroots compositors and KDE but not GNOME.
- **Packaging:** systemd user unit vs `.desktop` autostart; AppImage plus a `.deb`.

---

## 8. Build order

1. Protobuf schema
2. Server: Redis layer, WebSocket transport, admin UI, creation tokens
3. Desktop client (Windows or macOS) — easier to debug than mobile, and desktop↔desktop
   pairing exercises the entire crypto path
4. Android client

**MVP definition of done:** create group, pair additional devices, manual sync,
end-to-end encryption.
**Next milestone:** auto-sync where the platform permits.

---

## 9. Open questions

- **Push delivery for Android.** Needed for near-instant sync without battery drain,
  but self-hosters have no FCM credentials of their own. Candidate approaches:
  (a) ship FCM keys in the published build and relay wake-up-only pings through a
  first-party gateway that never sees content; (b) UnifiedPush / ntfy, self-host
  friendly but Android and Linux only; (c) WebSocket only, accepting that sync happens
  on app open or tile tap. Deferred past MVP.
- **Plaintext padding.** Not implemented; ciphertext length leaks approximate plaintext
  length to the server. Acceptable for a self-hosted single-user deployment.
- **Admin password hashing.** Deferred; config loader should accommodate it.
