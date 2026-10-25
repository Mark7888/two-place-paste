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
- **Store** — a group's shelf of items that do not expire, beside its 24-hour
  history (§6b). Off unless the admin turns it on for the group.
- **Store item** — one thing saved to the store: a copy of an entry, the clipboard
  or a file, with a name, an optional description and tags.

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
- Each epoch also has a **store key**, derived from its group key. A rekey seals the
  previous store key under the new one, so store items stay readable across
  revocations while a revoked device never learns a later key (`spec/crypto.md` §7a).

Per-message keys wrapped per device were considered and rejected: for a single user
with 2–4 devices and roughly two revocations a year, the added roster-trust
requirements are not worth the marginal revocation benefit.

### 2.3 What the server sees

Ciphertext, byte sizes, timestamps, device identifiers, wrapped key blobs, group
membership, and the labels the admin gives groups (a display name and a tag). It does
**not** see clipboard content, content type, or plaintext size (padding is not required
for MVP but the format should not preclude it).

Each entry may carry a **sealed header**: content type, filename and plaintext size,
encrypted under the group key, so a history listing can say what an entry is without
fetching it (`spec/crypto.md` §5.5). Every header is 297 bytes, whatever the entry, so
the server learns nothing from it; it stores it and hands it back with the entry's
metadata. A version 2 header is 553 bytes and adds the body's hash (`spec/crypto.md` §5.5.1); its size
tells the server only which version wrote it.

For a store item the server sees its size, when it was saved, the epochs its keys were
sealed at, and how many items and bytes a group holds. Its name, description, tags,
content type and filename are inside a fixed-size sealed record (`spec/crypto.md`
§7a.5), and the content hash used to spot duplicates is too: the server never sees a
hash it could test guesses against.

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
and do not attempt to fetch them. The store is the exception: a new device reads every
store item, because the store exists to outlive devices (§6b).

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
   device using each device's stored public key. It also seals the previous epoch's
   store key under the new one: the **store link** (`spec/crypto.md` §7a.2).
4. It uploads all wrapped keys and the link **in a single request**. The server applies them and
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

A rekey that carries no link is refused while the group has store items: accepting it
would leave every item unreadable. A client that predates the store is told to update.

**Known limitation:** revocation requires a device holding the current group key. A
user with only one remaining device that has lost access should leave and create a new
group. Nothing in history is lost given the 24-hour TTL; the **store is lost**, since
its keys exist only on devices.

### 3.4 Deletion by the admin

The admin can delete a whole group from the admin UI (§4.4). There is no rekey — there
is nothing left to re-key for — so deletion is a server operation:

1. One atomic operation removes the group record, its roster, every device record and
   every wrapped key. Each former device id is kept as a **tombstone** for 30 days. The
   creation token is kept and marked as having had its group deleted.
2. The server closes the group's open sockets with WebSocket close code **4410**.
3. The group's entries and blobs are deleted. A blob whose delete fails is reclaimed by
   the bucket sweep (§4.5) like any other.
4. The group's store is reclaimed in the background (§4.6): step 1 also queues the
   group for it, and the admin request does not wait.

A device of a deleted group that connects later is refused with **410 Gone** while its
tombstone lasts, and with 401 after that, exactly like a revoked device (§3.3 step 5).
Clients tell the user the group was deleted, and the way back is the same as for a
revoked device: leave the group, then create or join another.

The admin cannot remove a single device: that would leave the removed device holding the
current group key, which only a rekey by a member (§3.3) takes away.

---

## 4. Server

### 4.1 Deployment

Docker Compose: one Go server container, one Redis container. Configuration via `.env`.

TLS is expected to be terminated by a reverse proxy in front of the server.

### 4.2 Redis usage

Redis is the only datastore, but it holds **two zones with different rules**:

**Persistent zone** — group records, device records, public keys, wrapped group keys,
epochs, creation tokens, store links and store item records. No TTL. Must survive
restarts; requires AOF persistence. Store records grow with the store (about 2 KB per
item), which the per-group item limit bounds (§4.6).

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
group:<group_id>:store              zset    store item ids scored by saved_at
group:<group_id>:store:links        hash    epoch -> store link
store:<item_id>                     hash    group_id, size, saved_at, wrap_epoch,
                                            content_id, content_epoch, wrapped keys,
                                            sealed record, meta_rev, state
```

### 4.3 Storage of blobs

- Ciphertext **≤ 256 KB** is stored inline in Redis.
- Larger payloads go to a **blob backend** behind a Go interface — roughly
  `Put(entryID, expiresAt, r) (ref, error)`, `Get(ref)`, `Delete(ref)`, `Sweep(now)`.
  `expiresAt` is passed in at write time because reclamation depends on it (§4.5).
  - Local disk (the default).
  - S3-compatible object storage — MinIO, AWS S3 or any other — chosen explicitly in
    configuration. The server verifies the bucket and its credentials at startup and
    refuses to start if either is wrong; it never falls back to disk. All bytes pass
    through the relay, so the object store can stay on a private network.
- The Redis entry holds only a reference.

**Per-entry cap: 50 MB by default, configurable, measured on the ciphertext** — the
size the server actually receives. The server has no knowledge of plaintext size.

**No frame carries more than 10 MB of ciphertext.** An entry up to that size travels
in one frame (`EntryPutRequest`, `EntryFetchResponse`). A larger one is **chunked**:

1. `EntryLimitsRequest` reports the entry cap, the single-frame cap, the chunk size
   (8 MiB by default) and the inline threshold.
2. `EntryUploadBeginRequest` declares the entry id, epoch and exact size. Room is
   reserved against the storage limits here — evicting older entries if needed — so a
   full relay refuses before any chunk is sent.
3. `EntryUploadChunkRequest` carries chunk 0, 1, 2, … in order, each acknowledged
   before the next; every chunk but the last is exactly the chunk size.
4. `EntryPutRequest` with the `upload_handle` commits. The entry is created — and
   becomes the group's latest — only now; the epoch is checked again, and a re-key in
   between ends the upload.

`EntryUploadAbortRequest` abandons an upload. An upload idle for 10 minutes, or older
than an hour, is abandoned by the server; either way its reserved room is released.
Upload handles are bound to the device that began them. A large entry is read back with
`EntryFetchChunkRequest`, one range at a time.

Chunking is transport only: the client seals the whole entry as one container
(`/spec/crypto.md` §5) and opens it once all ranges are back.

Upload sessions live in the server's memory (§4.1: one instance). The blob being
written is staged in the backend — a file under `.uploads/` on disk, a multipart upload
on S3 — and the sweep removes what a crash abandoned. At startup the server releases
every storage reservation still pending, since no upload can have survived the
restart.

Redis expiry removes the entry record but does not touch the stored blob. Blob
reclamation is specified in §4.5.

**Storage limits.** Blob storage is capped in total and per group (configurable;
the per-group default is 1 GiB). Only blobs are counted. When a new entry would not
fit, the oldest committed entries are evicted — the writing group's own first, down
to the group limit, then the oldest anywhere, down to the total limit — and every
online device of each affected group receives `EntryEvicted`. Uploads still in
progress are never evicted; when only they hold the room, the write is refused with
`QUOTA_EXCEEDED`. An entry larger than a limit on its own is `TOO_LARGE`.

The store has limits of its own and never evicts (§4.6); history and store never evict
each other.

The accounting is kept in Redis, outside the ephemeral zone's TTLs. Every accounted
entry carries its expiry and is forgotten by the sweep after it, so a crash between
reserving room and storing the entry costs that room for at most one lifetime.

### 4.4 Admin UI

Served on the public domain. Credentials from `.env`.

**Scope** (docs/internal/plans/admin-group-management.md):

- **Dashboard** — what the relay can know without knowing any content: blob storage
  used against its limits, entries evicted in the last day, group, device, unused-token
  and stale-group counts, the storage backend's configuration and last sweep, and the
  health of the deployment (build, uptime, Redis eviction policy, AOF, memory). A sweep
  can be run on demand.
- **Groups** — every group, filterable by tag: the device count, last activity, live
  entry count, age of the oldest entry and blob storage used against the group's limit.
  Sizes and counts only: no entry id, content type or filename is shown, and none is
  available to show. Groups idle longer than the stale threshold are marked; a group is
  deleted from its own page, one at a time.
- **Group** — one page per group: rename it, put it under a tag, set its own storage
  limit, list its devices (name, paired, last seen, online), purge its entries early, and
  delete it (§3.4). Purge and delete go through a confirmation page. The same page turns
  the group's **store** on or off, shows its store bytes and items against their own
  limits, sets store limit overrides, and has a separate **Purge store** (§4.6).
- **Tags** — create and delete tags; a group has at most one. A tag may carry a storage
  limit for its groups, and a store limit. It cannot turn the store on: that is decided
  per group. A group's blob limit is its own override if enabled, else its
  tag's if enabled, else `TPP_BLOB_MAX_GROUP_BYTES`.
- **Tokens** — creation tokens with name, note, starting tag, created date, optional
  expiry (1, 7 or 30 days), status, and the resulting group if used. Token generation,
  and revocation of a token that has not been used yet (its URL then returns 404, like
  any unknown token). An expired token is treated as unknown. A used token cannot be
  revoked: its group already exists, and is removed by deleting it (§3.4).
- **Settings** — runtime settings stored in Redis: the stale threshold.

Every list (groups, tags, tokens, the audit log) is searched, sorted and paged by the
server, and its view is kept in the URL. A token is named in the UI — in rows, URLs and
requests — by an opaque handle derived from it, never by its value, and so is a group,
whose id is its token; a token's creation URL is sent only when the admin asks for it.
- **Audit log** — the last 1000 admin actions and sign-ins, with time and client
  address. It never holds a token value, a password, a cookie or a key.

No content access, by construction.

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

A chunked upload fixes its blob's expiry when it begins and its entry's when it
commits. The blob is filed one hour (the longest an upload may take) later than the
entry could expire, so its bucket always outlives the entry.

Write the blob **before** writing the Redis entry. If the server crashes in between,
the resulting orphan sits in a bucket that will be deleted within 24 hours regardless.
No crash-recovery logic is required.

**Explicitly deleted entries**

An entry may be deleted before its expiry: by a storage-limit eviction (§4.3), or when
a read finds its blob missing from the store — deleted outside the relay — in which
case the reader gets not-found and the entry is retired at once, its group told as for
an eviction, rather than failing every later reader for the rest of its lifetime. That shortens a lifetime and never lengthens one, so the invariant above
holds. Its record is deleted first and then its blob; a blob whose delete fails stays
on disk until its bucket expires.

A blob whose entry is deleted early remains on disk until its bucket expires. This is
bounded by the 24-hour TTL and the per-entry cap, so the waste is small. An
optional fast-path unlink on explicit delete may be added; because the bucket sweep is
the backstop, that fast path is permitted to fail silently.

**S3 backend**

The S3 backend uses the same layout under an optional key prefix,
`<prefix>/<YYYYMMDDHH>/<entry_id>.bin`, and the same sweep: list the hour prefixes and
delete everything under the ones that have fully passed. It deletes actively rather
than relying on a bucket lifecycle rule, because lifecycle rules work in whole days and
the storage limits (§4.3) would count dead objects for up to a day. A lifecycle rule
expiring objects after two days remains a sensible backstop for a relay that is down.
Writes carry `If-None-Match: *`, so the store itself refuses to replace an existing
blob, as `O_EXCL` does on disk.

**Manual verification**

A `tpp gc --verify` command performs a full mark-and-sweep cross-check between stored
blobs and Redis entries and reports orphans. This is a **diagnostic tool for use after
Redis data loss or migration**, run by hand. It is never scheduled and is not part of
the normal reclamation path.

**The store is outside this scheme.** Store items do not expire, so they are never in an
hour bucket and nothing here touches them; §4.6 reclaims them. The invariant above is
about entries, and the store does not weaken it: saving an entry to the store copies
it, and the entry keeps its own lifetime.

### 4.6 Store storage and reclamation

The store (§6b) is a separate namespace beside the hour buckets:

```
store/<group_id>/<item_id>.bin              disk, under the blob root's volume
<prefix>/store/<group_id>/<item_id>.bin     S3, in the same bucket
```

Every store item is stored there, however small: none is inline in Redis.

**Limits.** A group's store has its own byte limit and item limit, apart from its blob
limit, with the same precedence: the group's override, else its tag's, else the
server default. There is an optional total limit for every store together. **The store
never evicts.** A save that would not fit is refused with `QUOTA_EXCEEDED`, and the
client tells the user how much room is left. An item larger than the limit on its own
is `TOO_LARGE`.

**Write ordering.** An upload writes the item's record as *pending* first and lists it
in a small in-flight set, then writes the blob, then marks the record committed and
adds it to the group's list. The sweep (§4.5's schedule) deletes the blob and record of
any pending item older than an hour, so a crash costs nothing permanent, and
reconciliation never lists the whole store. A deleted item loses its record first,
then its blob; a blob whose delete fails goes to a retry set the sweep drains.

**Group deletion and Purge store.** Removing a group's store is a prefix delete, not
one delete per item: `RemoveAll` on disk, a listing and batched multi-object deletes on
S3. It runs in the background, from a queue that group deletion (§3.4) and **Purge
store** add to and that survives a restart. As a backstop, each sweep lists the group
prefixes under `store/` — one name per group, not per item — and reclaims any whose
group no longer exists and whose creation token is marked as having had its group
deleted; a group missing without that mark is left alone, so a relay pointed at the
wrong or an empty Redis deletes nothing. Unlike the hour-bucket sweep, this one reads Redis, once per
group.

**Turning a store off** blocks every store request with `STORE_DISABLED` and keeps the
items; turning it on again brings them back as they were. Deleting them is **Purge
store**. A disabled store still holds its link chain, and a rekey still has to carry a
link while the group has items (§3.3).

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
- **Conflict resolution:** last write to reach the server wins. An entry's time is when
  its upload completed, not when it was copied. A client applying entries automatically
  follows that rule while transfers overlap:
  - an entry announced while one of its own uploads is in flight is held, not applied:
    the upload will reach the server later and win. It is applied after all only if the
    upload fails, or if it reached the server after the upload did;
  - an automatic download is abandoned when a newer entry is announced, or when the user
    copies something locally, and it never overwrites a local copy made while it ran;
  - transfers have no overall deadline. Each request is bounded by the bytes it moves,
    so a large entry on a slow link is slow rather than failed.
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

## 6b. Store

The store is a group's shelf of items that do not expire: the few things a user wants
on the clipboard again and again, kept apart from the 24-hour history. It is not bulk
storage, and its default limits are small.

- **Off by default.** The admin turns it on per group (§4.4). A client asks the relay
  whether the store is on and, when it is not, says so in place of the Store tab and
  hides every **Save to store** action.
- **Saving copies.** A history entry, the current clipboard, or a file (desktop: a drop
  zone or file browser; Android: the system file picker) is saved as a new item with a
  name, an optional description and any number of tags up to the limit. Saving from
  history fetches and opens the entry, then seals and uploads it again; the entry is not
  touched, and still leaves history when it expires or is evicted.
- **Duplicates warn.** A client compares the content's hash with those of the items it
  holds. A match names the existing items and offers **Save anyway**: the same content
  under another name or tags is legitimate. History rows whose header carries a hash
  (`spec/crypto.md` §5.5.1) are marked as saved without being fetched.
- **The relay cannot search.** Opening the Store tab fetches the list of items — sealed
  records, not content — and the client decrypts it and does all searching (name and
  description), tag filtering and sorting (by date saved or by name) itself. An item's
  content is fetched when the user copies or saves it, as in history (§6).
- **Edits and deletes** are open to every device of the group, as revocation is (§3.3).
  Deleting asks for confirmation; editing does not. An edit names the revision it
  replaces, so two devices editing at once cannot silently lose one.
- **Rekeys keep items readable** (§3.3 step 3) and a newly paired device reads them all
  (§3.2). Items saved before a revocation stay exposed to the revoked device, which held
  their keys; a rekey does not re-encrypt the store (`spec/crypto.md` §11.4).
- **Header v2 waits for the relay.** A client writes version 2 entry headers only once
  the relay reports it accepts them; until then it writes version 1, which every relay
  accepts.

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
- History browser, listed when the tab is opened, with a preview of entries at the
  current epoch: text and images, and files of a type in the desktop's preview table
  (`desktop/internal/preview`) — images and text drawn inline, audio and video played
  from bytes the page fetches with its token. A file whose bytes do not match its name
  is not previewed.
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
