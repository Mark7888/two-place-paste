# Plan — Persistent store

**Status:** done. Every step of §10 has landed: spec, vectors and client crypto
(#40), header v2 (#42), the wire schema and store links on rekey (#43), the relay
(#44), the Go client core (#45), the desktop Store tab (#46), and the TypeScript core
with the Android Store screen and the cross-client interop suite (#47). §9 records the
answered questions; §11 keeps the server-side copy as a later option.

---

## 1. Goals

- A per-group **store** of items that do not expire: a small shelf of things the
  user often needs on the clipboard, not bulk storage.
- History does not change. Saving an entry **copies** it; the history entry stays
  in history and is still removed by its 24-hour expiry or by eviction when
  history storage is full (SPEC §4.3, §4.5). Nothing is pinned or extended.
- The store never evicts. A save that does not fit is refused, and the user is
  told so in the save dialog, with what is free.
- The store is **off by default** and turned on per group by the admin. Clients
  show that it is off instead of a Store tab that fails.
- Every store item is a blob, however small.
- Saving from history is done by the client: it fetches and opens the entry,
  then seals and uploads it again as a store item. A server-side copy is kept
  possible (§11) but not built now.
- Sources: a history entry, the current clipboard, or a file (desktop: drop zone
  or file browser in a modal; Android: the system file picker).
- A **Store** tab on desktop and Android: list, text search over name and
  description, filter by several tags, sort by creation date or by name, copy to
  clipboard or save as a file, edit, delete.
- History and store have **separate** limits per group.
- The server learns nothing new: not content, type or filename, and also not
  names, descriptions or tags.
- Items stay readable across a rekey; a revoked device cannot read items saved
  after its revocation.
- History rows show whether an entry is already saved, without fetching it.

## 2. What exists today

| Piece | Where | Relevance |
|---|---|---|
| Group key per epoch, wrapped per device | `spec/crypto.md` §4 | §7 forbids keeping old epochs' keys. |
| Entry container | `spec/crypto.md` §5.1–5.4 | Becomes the store's blob format too (D2). |
| Sealed header, 297 bytes, enforced by the server | `spec/crypto.md` §5.5, `server/internal/entries/entries.go` `HeaderSize` | Gets a v2 with a content hash (D6). |
| Rekey: one atomic request | SPEC §3.3, `RekeyRequest` | Carries the store link (D3). |
| History blobs `blobs/<YYYYMMDDHH>/<entry_id>.bin` | `server/internal/blob/disk.go`, `s3.go` | Filed by **expiry hour, not by group**. The sweep skips names that are not hour buckets. |
| Chunked upload and fetch | SPEC §4.3, `entries/uploads.go` | Reused for uploads from clipboard or file, and for reading items. |
| Quota with group/tag overrides, eviction | `server/internal/quota` | Store gets its own, without eviction. |
| Group delete → `PurgeGroup`, entry by entry, in the request | `admin/groups.go`, `entries/service.go` | Fine for 24 h of history; the store needs async reclaim (§6.4). |
| `minio-go` `CopyObject` | already a dependency | Would allow the later server-side copy on S3 with no new module (§11). |

## 3. Decisions

| # | Decision | Why |
|---|---|---|
| D1 | Store is **off** per group by default; the admin enables it on the group page. Calls to a disabled store fail with a new `STORE_DISABLED` error code, and `StoreLimitsResponse.enabled` lets a client show "The store is turned off for this group" before trying. | Operator control over disk; explicit opt-in. |
| D2 | A store blob is an **entry container** (`spec/crypto.md` §5.2), byte for byte. The item record keeps the container's `content_id` and `content_epoch`, the two values its AAD binds. In v1 the client always seals a fresh container, so they are the item's own id and the epoch it was saved at. | No second blob format to specify and test, and it keeps the server-side copy of §11 open without a format change: a copied entry would simply carry the entry's id and epoch instead. |
| D2a | **Saving from history is download + upload** in v1: the client fetches the entry (it already does for Copy), opens it, and uploads it as a new item through the chunked flow. | Simplest path; the metadata comes from the frame the client actually opened, so it is verified rather than taken from the header. |
| D3 | The key to a blob is its **content key** (§5.1), wrapped under the **store key** of the epoch it was saved in. Each rekey uploads a **link**: the previous store key sealed under the new one, forming a backward chain. | Items survive a rekey with O(1) work per rekey, sent in the same atomic request. |
| D4 | A revoked device keeps nothing new: links point only backwards, and it never learns a later store key. | Keeps the "rekey is a hard break" property for everything saved after a revocation. |
| D5 | Name, description, tags, content type, filename, size and hash live in a **sealed, fixed-size metadata record**. Search, filter and sort run on the client over the decrypted records. | SPEC §2.3. Fixed size so names leak no length. |
| D6 | **Header v2** adds `sha256(body)` and room for a 255-byte filename: 512 bytes encoded, 553 sealed. Duplicates are found by the client against the hashes in its decrypted store index; no hash is ever visible to the server. | History rows can show "saved" and the save dialog can warn, without fetching. A server-visible hash would let the server confirm guesses. |
| D7 | A duplicate is a **warning**, not a refusal: the dialog names the existing item(s) and offers "Save anyway". | Same content under another name or tags is useful. |
| D8 | Store blobs live in their own namespace, `store/<group_id>/<item_id>.bin`, on the same volume (disk) or bucket (S3) as history blobs. | Group deletion becomes a prefix delete; the same volume or bucket keeps §11 possible (hard link, `CopyObject`). |
| D9 | The store never evicts; a full store is `QUOTA_EXCEEDED` with the free space in the error details. | Persistent means persistent. |
| D10 | Group deletion reclaims the store **asynchronously** (§6.4). | No admin request stalls on a big store. |
| D11 | A newly paired device can read the whole store. | The store exists to outlive devices. Amends SPEC §3.2 "new devices start empty" for the store only. |
| D12 | Tags are free-text strings inside each item's metadata, several per item; the tag list is their union. | Simple; renaming a tag rewrites its items' metadata, which is small. |
| D13 | Losing every device loses the store. Documented now; a recovery key can be added later without a format change (it would wrap a store key). | Out of scope for v1. |

## 4. Cryptography

Specified in `spec/crypto.md` §5.5.1 (header v2) and §7a (store), revision
1.3, with vectors in `header-v2.json`, `store-chain.json`, `store-item.json` and
`failure.json`. No new primitive; seven new domain strings. In summary:

| Piece | Construction | Size |
|---|---|---|
| Store key | `HKDF(group_key[E], <empty>, "tpp/v1/store" ‖ E)` | 32 |
| Link into E | the store key of E-1, sealed under a key derived from the store key of E, zero nonce, both epochs in the AAD | 49 |
| Item keys | the blob's content key and a **random metadata key**, wrapped together under the store key of the save epoch W, item id and W in the AAD | 105 |
| Blob | an entry container (§5.2), `content_id = item_id`, `content_epoch = W` | body + 41 |
| Metadata record | fixed record: body length, SHA-256, type, filename, name, description, tags | 2048, sealed 2089 |

Where the build differs from the first draft of this plan, and why:

- **Two item keys, not one.** The draft derived the metadata key from the content
  key, which would have used one key both as an AEAD key and as an HKDF input.
  A separate random metadata key, wrapped with it, keeps every key to one use.
- **A 2048-byte record, not 1024.** The limits agreed for the dialog (name 200
  bytes, description 800, 16 tags of 32, a 255-byte filename) need up to 1961
  bytes. The record no longer carries a client timestamp: the relay's `saved_at`
  orders and dates items.
- **The link key binds its epoch** (`"tpp/v1/store-link" ‖ E`), not only the AAD.

Reading an item wrapped at epoch W by a client at epoch E ≥ W: derive
`store_key[E]`, open `link[E]`, `link[E-1]`, … down to W; unwrap the content
key; open the container with the record's `content_id` and `content_epoch`.

**Chain size.** One 49-byte link per rekey, downloaded once per session, and
opening one costs a hash and a decryption: microseconds. At the two revocations
a year SPEC §2.2 expects, a group rekeyed a hundred times carries 4.9 KB and opens
the chain in well under a millisecond on a phone. It is never noticeable. If it
ever matters, a client can re-wrap old items to the current epoch (105 bytes each)
and the old links become unused.

**Exception to §7.** "Never keep old keys" stays the rule for history. For the
store, old store keys are reachable only from the newest one, which a revoked
device never gets. The spec section says so explicitly.

**Where the metadata comes from.** In v1 the saving device has opened the frame,
so type, filename, size and hash in the metadata come from the frame itself. The
header's hash is used only to show "saved" on history rows and to warn early;
the save dialog checks again with the hash of the frame it fetched.

### 4.1 Header v2 (`spec/crypto.md` §5.5)

```
encoded_header_v2 =                                  exactly 512 bytes
    u8(0x02)
 || u16be(len(content_type)) || content_type         ≤ 128 bytes, as v1
 || u16be(len(filename))     || filename             ≤ 255 bytes, cut at a UTF-8 boundary
 || u64be(body_len)
 || sha256(body)[32]
 || zero[...]
header_container_v2 = 553 bytes, sealed exactly as v1
```

- 255 bytes fits any real filename, so listings no longer cut names short (and
  §11 would get full names without a fetch).
- The server accepts headers of 0, 297 or 553 bytes. The header version is inside
  the ciphertext, so its size reveals only which version wrote it.
- Readers decode v1 and v2. A client that predates v2 rejects the unknown version
  and shows the type as unknown until fetched, the behaviour §5.5 already
  specifies. Writers switch to v2 once the server reports support (§7).
- An entry with no header or a v1 header carries no hash, so its row shows no
  "saved" mark; the save dialog still finds duplicates from the fetched frame.

## 5. Save flows

Every source ends in the same upload: the client seals an entry container with
`content_id = item_id` at its current epoch, wraps its content key, seals the
metadata, and uploads through the chunked flow with a store target, sending the
wrapped key and metadata at begin. Small text goes the same way and becomes a
blob like everything else.

**From history:**

1. The row already shows "saved" if the v2 header's hash is in the store index.
2. On **Save to store**, the client fetches and opens the entry (as Copy does) and
   hashes the body; a match in the index shows the duplicate warning with
   **Save anyway**.
3. The dialog asks for name (pre-filled from the filename or the first line of
   text), description and tags, and shows store space left. The client checks
   the size against it before uploading; the server checks again at begin.
4. Upload as above. The history entry is not touched.

If the entry expires or is evicted before the fetch, the dialog says it is gone.
Once fetched, eviction no longer matters.

**From the clipboard or a file:** the same, starting from the local bytes.

## 6. Server

### 6.1 Records (Redis persistent zone, no TTL)

```
group:<id>                  hash   + store_enabled
group:<id>:store            zset   item ids scored by saved_at (server time)
store:<item_id>             hash   group_id, content_id, content_epoch, wrap_epoch,
                                   size, saved_at, wrapped_key, sealed_meta,
                                   meta_rev, state (pending | committed)
group:<id>:store:links      hash   epoch -> link
store:pending               zset   item ids in flight, by start time
store:reclaim               set    group ids whose store is being deleted
```

About 2.3 KB per item (the sealed record is 2089 bytes). Redis memory grows with the store, so the operator docs say
so, and the item count per group is limited (§6.5).

### 6.2 Write ordering

1. Begin: write the record as `pending`, add it to `store:pending`, reserve room.
2. Bytes land: uploaded through `.uploads/` staging and moved into place.
3. Commit: mark the record `committed`, add it to the group zset, drop it from
   `store:pending`.

The sweep deletes the blob and record of any `store:pending` item older than an
hour. The list is only as long as what is in flight, so no reconciliation ever
lists the whole store. A user deletion removes the record first, then the blob; a
failed blob delete goes to a small retry set the sweep drains.

### 6.3 Enabling and disabling

Only the **group page** has the switch (no tag-level or token-level setting).
Enabling is immediate. Disabling blocks every store call with `STORE_DISABLED`
but keeps the items and their records; enabling again brings them back as they
were. Deleting them is the separate **Purge store** button (§6.6). The disable
confirmation says how much the store still holds. Rekeys keep requiring the
link while items exist, enabled or not, so a disabled store survives a
revocation. Connected devices get `StoreStateChanged` either way.

### 6.4 Group deletion

- `deleteGroup` already removes the group record atomically, which cuts devices
  off. In the same step it adds the group id to `store:reclaim`, then returns.
- A background reaper, owned by the sweeper goroutine, deletes the group's store
  records in pages and the prefix `store/<group_id>/`: on disk one `RemoveAll`,
  on S3 a listing plus `DeleteObjects` in batches of 1000 (about 10 requests per
  10 000 items).
- Backstop: each sweep lists the group prefixes under `store/` (one name per
  group, not per item) and reclaims any whose group record is gone, covering a
  crash between delete and reaper.
- The admin UI shows "store being deleted" until the reaper is done; **Purge
  store** on the group page uses the same reaper.

### 6.5 Limits

| Setting | Default | Notes |
|---|---|---|
| `TPP_STORE_MAX_TOTAL_BYTES` | 0 (off) | Separate from the blob total; history and store never evict each other. |
| `TPP_STORE_MAX_GROUP_BYTES` | 100 MiB | Override per tag and per group, same precedence as the blob limit (SPEC §4.4). |
| `TPP_STORE_MAX_ITEM_BYTES` | = entry cap (50 MiB) | |
| `TPP_STORE_MAX_GROUP_ITEMS` | 1 000 | Bounds Redis memory and the index a client downloads (~2 MB). |

Counted: blob size plus the metadata record.

### 6.6 Admin UI

Group page: Store switch, store bytes and items against its limits (separate from
history), store limit overrides, **Purge store**. Tag page: store limit. Dashboard:
total store usage. Sizes and counts only.

## 7. Wire

`proto/tpp/v1/store.proto`, with numbers 70–85 for requests and 56–59 for
events (#43). A store upload mirrors an entry's, so the relay reuses that code:

| Message | Carries |
|---|---|
| `StoreLimitsRequest/Response` | enabled; byte limit, used, available now; item limit, used; max item size. Answered when the store is off too. |
| `StoreListRequest/Response` | newest first, paged by an opaque cursor (a time cursor could skip items saved in the same millisecond, and the client needs every item) |
| `StoreLinksRequest/Response` | every `(epoch, link)`, oldest first |
| `StoreUploadBeginRequest/Response` | item id, epoch, size → handle and chunk size. Chunks and aborts are `EntryUploadChunkRequest` / `EntryUploadAbortRequest`. |
| `StorePutRequest/Response` | item id, epoch, size, wrapped item keys (105), sealed record (2089), and either the blob or the upload handle |
| `StoreFetchChunkRequest/Response` | a range of the blob |
| `StoreUpdateMetaRequest/Response` | a new sealed record against `expected_meta_rev` |
| `StoreDeleteRequest/Response` | item id |
| Events `StoreItemAdded`, `StoreItemUpdated` | the item's metadata |
| Events `StoreItemDeleted`, `StoreStateChanged` | the item id; whether the store is on |

`StoreItemMeta` is what a listing returns per item: id, `saved_at`, size,
`wrap_epoch`, `content_id`, `content_epoch`, wrapped item keys, sealed record,
`meta_rev`.

Changes to existing messages:

- `RekeyRequest.store_link` (field 4). Both clients send it on every rekey from
  #43 on, and refuse to rekey from a key older than the relay's epoch, which
  would seal a wrong link. The relay will refuse a rekey without it while the
  group has store items.
- `ErrorCode`: `STORE_DISABLED` (13), `CLIENT_OUTDATED` (14) for that rekey,
  and `REVISION_CONFLICT` (15) for an edit against an old revision.
- `EntryPutRequest.header` and `EntryLimitsResponse.header_v2`: landed in #42.

## 8. Clients

`pkg/tppclient` and `mobile/src/core`: header v2 codec, store key and chain,
wrap/unwrap, metadata codec, the store calls, and an in-memory index of decrypted
metadata with search, tag filter and sort.

Desktop (`desktop/ui`) and Android:

- **Store tab.** When the store is off: a message saying the admin has not turned
  it on for this group. Otherwise: search box, tag chips (several), sort by date
  or name, space bar, and per item Copy / Save as file / Edit / Delete.
- **Add to store** on the Store tab: a modal with a drop zone and file browser on
  desktop; the system file picker on Android. Also **Save clipboard to store**.
- **Save to store** on each History row and on the current entry on the main page;
  a "saved" mark when the hash is in the index. Hidden when the store is off.
- **Save dialog:** name, description, tags (pick existing or type new), space left,
  duplicate warning with **Save anyway**, and the refusal message when it does not fit.

The docs site gains a Store page (user), and the configuration, admin and
upgrading pages (operator), with screenshots, in the same PRs.

## 9. Answered questions

| # | Question | Answer |
|---|---|---|
| Q1 | What does **disabling** a store that has items do? | It only disables: items are kept, every store call is refused. **Purge store** is a separate button (§6.3, §6.6). |
| Q2 | Can a **tag** turn the store on for its groups? | No, the group page only. |
| Q3 | Should deleting or editing an item ask for confirmation? | Delete asks; edit does not. |
| Q4 | Server-side copy from history? | Not now: download + upload (D2a). Kept as a later option (§11). |
| Q5 | Rollout order for header v2. | One release for the server and both clients; clients write v2 only when `StoreLimitsResponse` says the server accepts it. |
| Q6 | Default limits. | 100 MiB and 1 000 items per group (§6.5), adjustable per group and tag. |

## 10. Order of work (one PR each)

1. **Spec** — SPEC store sections and the §3.2, §3.3, §4.5 amendments;
   `spec/crypto.md` store section and header v2, with vectors, and the crypto
   functions in `pkg/tppclient/tppcrypto` and `mobile/src/core/crypto` that the
   vectors hold them to: `spec/crypto.md` §12 has a contract change land with
   both implementations, and each client's coverage test fails on a suite it
   does not run. Readers accept header v2 from here on; writers stay on v1.
2. **Proto** — `store.proto`, `RekeyRequest.store_link`, `STORE_DISABLED`,
   `make proto`.
3. **Server** — header v2 acceptance, store records, namespace and quota, upload
   and fetch, reaper and sweep, rekey check, enable switch, config, admin API and UI.
4. **Go client core** — header v2, store crypto, calls and index; interop tests.
5. **Desktop** — Store tab, save flows, header v2 writing, docs and screenshots.
6. **Android** — the same, plus `npm run interop` coverage.
7. ROADMAP status update.

## 11. Later: server-side copy from history

Not built in v1; recorded so the v1 formats do not rule it out.

Saving a large entry from a phone costs a download and an upload of up to the
entry cap. Because a store blob is an entry container (D2), the server could copy
the entry's bytes unread instead:

1. `StoreCopyBegin{entry_id, item_id}`: the server checks the store is enabled,
   the entry is the caller's group's and at the current epoch, and that it fits;
   copies the bytes to `store/<group_id>/<item_id>.bin` — S3 `CopyObject` inside
   the bucket, a hard link on disk (a file copy across volumes), or the inline
   bytes from Redis — and writes a `pending` record. It answers with the entry's
   24-byte nonce and its epoch.
2. The client derives the content key from its group key and that nonce, wraps
   it, and commits with the sealed metadata. The record's `content_id` and
   `content_epoch` are the entry's.

What it costs, and why it waits:

- The saving device never opens the content, so the metadata comes from the
  header v2 (advisory, any member can write it) rather than the frame; the first
  device that opens the item would have to check and correct it.
- The store must read that entry format for as long as the item lives, and a
  history-format change would need a migration story for copied items.
- A hard-linked copy is counted twice by the quotas while the history entry
  lives (at most 24 hours).
- Two more wire messages, a copy path per backend, and their tests.

None of it changes a v1 byte layout: header v2 already carries the full filename
and the hash, and the item record already carries `content_id`/`content_epoch`.
