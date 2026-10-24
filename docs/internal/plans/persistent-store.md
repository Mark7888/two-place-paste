# Plan — Persistent store

**Status:** draft, second round. Decisions from the first review are folded in;
§9 lists what is still open.
**Touches:** `/spec/**` (store section, header v2, vectors), `/proto/**`,
`/server/**`, `/pkg/tppclient/**`, `/desktop/**`, `/mobile/**`, `/deploy/**`,
`/docs/**`.

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
- **Copying from history happens on the server.** The content does not travel
  to the client and back; on S3 it does not even leave the bucket.
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
| `minio-go` `CopyObject` | already a dependency | Server-side copy on S3 with no new module. |

## 3. Decisions

| # | Decision | Why |
|---|---|---|
| D1 | Store is **off** per group by default; the admin enables it on the group page. Calls to a disabled store fail with a new `STORE_DISABLED` error code, and `StoreLimitsResponse.enabled` lets a client show "The store is turned off for this group" before trying. | Operator control over disk; explicit opt-in. |
| D2 | A store blob is an **entry container** (`spec/crypto.md` §5.2), byte for byte. The item record keeps the container's `content_id` and `content_epoch`, the two values its AAD binds. Copied from history, they are the entry's id and epoch; uploaded directly, the item's own id and the current epoch. | This is what makes a server-side copy possible: the server copies bytes it cannot read, and nothing needs re-encrypting. One blob format for both paths. |
| D3 | The key to a blob is its **content key** (§5.1), wrapped under the **store key** of the epoch it was saved in. Each rekey uploads a **link**: the previous store key sealed under the new one, forming a backward chain. | Items survive a rekey with O(1) work per rekey, sent in the same atomic request. |
| D4 | A revoked device keeps nothing new: links point only backwards, and it never learns a later store key. | Keeps the "rekey is a hard break" property for everything saved after a revocation. |
| D5 | Name, description, tags, content type, filename, size and hash live in a **sealed, fixed-size metadata record**. Search, filter and sort run on the client over the decrypted records. | SPEC §2.3. Fixed size so names leak no length. |
| D6 | **Header v2** adds `sha256(body)` and room for a 255-byte filename: 512 bytes encoded, 553 sealed. Duplicates are found by the client against the hashes in its decrypted store index; no hash is ever visible to the server. | History rows can show "saved" and the save dialog can warn, without fetching. A server-visible hash would let the server confirm guesses. |
| D7 | A duplicate is a **warning**, not a refusal: the dialog names the existing item(s) and offers "Save anyway". | Same content under another name or tags is useful. |
| D8 | Store blobs live in their own namespace, `store/<group_id>/<item_id>.bin`, on the same volume (disk) or bucket (S3) as history blobs. | Group deletion becomes a prefix delete; same volume allows hard links. |
| D9 | The store never evicts; a full store is `QUOTA_EXCEEDED` with the free space in the error details. | Persistent means persistent. |
| D10 | Group deletion reclaims the store **asynchronously** (§6.4). | No admin request stalls on a big store. |
| D11 | A newly paired device can read the whole store. | The store exists to outlive devices. Amends SPEC §3.2 "new devices start empty" for the store only. |
| D12 | Tags are free-text strings inside each item's metadata, several per item; the tag list is their union. | Simple; renaming a tag rewrites its items' metadata, which is small. |
| D13 | Losing every device loses the store. Documented now; a recovery key can be added later without a format change (it would wrap a store key). | Out of scope for v1. |

## 4. Cryptography

New `spec/crypto.md` section, plus a header revision. No new primitive. New
domain strings: `tpp/v1/store`, `tpp/v1/store-link`, `tpp/v1/store-link-aad`,
`tpp/v1/store-wrap`, `tpp/v1/store-wrap-aad`, `tpp/v1/store-meta`,
`tpp/v1/store-meta-aad`.

```
store_key[E]  = HKDF(ikm = group_key[E], salt = <empty>,
                     info = "tpp/v1/store" || u64be(E), L = 32)

-- the blob: an entry container (§5.2), sealed by whoever wrote it
content_key   = HKDF(group_key[C], entry_nonce, "tpp/v1/entry" || u64be(C))   -- §5.1
                where C = content_epoch and the AAD binds content_id (§5.3)

-- per item, made by the saving device at its current epoch W (W = C in practice)
wrapped_key   = u8(1) || wrap_nonce[24] || XChaCha20-Poly1305(
                  key   = HKDF(store_key[W], wrap_nonce, "tpp/v1/store-wrap" || u64be(W)),
                  nonce = wrap_nonce,
                  aad   = "tpp/v1/store-wrap-aad" || u8(1) || u64be(W)
                          || len32(item_id) || item_id,
                  pt    = content_key)                                   -- 73 bytes

sealed_meta   = u8(1) || meta_nonce[24] || XChaCha20-Poly1305(
                  key   = HKDF(content_key, meta_nonce, "tpp/v1/store-meta"),
                  nonce = meta_nonce,
                  aad   = "tpp/v1/store-meta-aad" || u8(1) || len32(item_id) || item_id
                          || u64be(meta_rev),
                  pt    = 1024-byte record: version, name, description, tags,
                          content type, filename, body length, sha256(body), saved_at)

-- on rekey E → E+1, by the revoking device, which holds both keys
link[E+1]     = u8(1) || XChaCha20-Poly1305(
                  key   = HKDF(store_key[E+1], <empty>, "tpp/v1/store-link"),
                  nonce = 24 zero bytes,                       -- key used once
                  aad   = "tpp/v1/store-link-aad" || u8(1) || u64be(E) || u64be(E+1),
                  pt    = store_key[E])                                  -- 49 bytes
```

Reading an item wrapped at epoch W by a client at epoch E ≥ W: derive
`store_key[E]`, open `link[E]`, `link[E-1]`, … down to W; unwrap the content
key; open the container with the record's `content_id` and `content_epoch`.

**Chain size.** One 49-byte link per rekey, downloaded once per session, and
opening one costs a hash and a decryption: microseconds. At the two revocations
a year SPEC §2.2 expects, a group rekeyed a hundred times carries 4.9 KB and opens
the chain in well under a millisecond on a phone. It is never noticeable. If it
ever matters, a client can re-wrap old items to the current epoch (73 bytes each)
and the old links become unused.

**Exception to §7.** "Never keep old keys" stays the rule for history. For the
store, old store keys are reachable only from the newest one, which a revoked
device never gets. The spec section says so explicitly.

**What a copy trusts.** In a server-side copy the saving device never opens the
container, so the metadata (type, filename, hash, size) comes from the entry's
sealed header. The header is advisory (§5.5) and any group member can write one,
the same trust as today. The first device that opens the item checks the frame
against the metadata and corrects the metadata if they differ.

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

- 255 bytes fits any real filename, so the store gets the full name from a copy.
- The server accepts headers of 0, 297 or 553 bytes. The header version is inside
  the ciphertext, so its size reveals only which version wrote it.
- Readers decode v1 and v2. A client that predates v2 rejects the unknown version
  and shows the type as unknown until fetched, the behaviour §5.5 already
  specifies. Writers switch to v2 once the server reports support (§7).
- An entry with no header or a v1 header carries no hash. Saving it falls back to
  fetching it first, which also checks for duplicates; the copy itself is still
  done on the server.

## 5. Save flows

**From history (server-side copy):**

1. The client has the entry's v2 header from the history listing: hash, type,
   filename, size. It looks the hash up in its store index and warns if found.
2. The dialog asks for name (pre-filled from the filename or the first line of
   text), description and tags, and shows store space left.
3. `StoreCopyBegin{entry_id, item_id}`: the server checks that the store is
   enabled, the entry belongs to the caller's group and is at the current epoch,
   and that it fits; then it copies the bytes into `store/<group_id>/<item_id>.bin`
   and writes a `pending` record. It answers with the entry's 24-byte nonce
   (container bytes 1–25) and its epoch.
   - S3: `CopyObject`, inside the bucket.
   - Disk: a hard link (`os.Link`), falling back to a file copy. A hard link is
     safe because the history sweep only unlinks its own name.
   - Inline entry (≤ 256 KB, in Redis): the server writes those bytes to the store
     path.
4. The client derives the content key from its group key and that nonce, wraps it,
   seals the metadata and sends `StoreCopyCommit{item_id, wrapped_key, sealed_meta}`.

The entry may be evicted between 1 and 3: that is not-found, and the dialog says the
entry is gone. Once step 3 has copied it, eviction no longer matters.

**From the clipboard or a file:** the client seals an entry container with
`content_id = item_id` at its current epoch, and uploads it through the chunked flow
with a store target, sending the wrapped key and metadata at begin.

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

About 1.2 KB per item. Redis memory grows with the store, so the operator docs say
so, and the item count per group is limited (§6.5).

### 6.2 Write ordering

1. Begin: write the record as `pending`, add it to `store:pending`, reserve room.
2. Bytes land: copied, linked, or uploaded through `.uploads/` staging and moved.
3. Commit: mark the record `committed`, add it to the group zset, drop it from
   `store:pending`.

The sweep deletes the blob and record of any `store:pending` item older than an
hour. The list is only as long as what is in flight, so no reconciliation ever
lists the whole store. A user deletion removes the record first, then the blob; a
failed blob delete goes to a small retry set the sweep drains.

### 6.3 Enabling and disabling

The group page gets a **Store** switch and the store limits. Enabling is
immediate. Disabling: see Q1.

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
| `TPP_STORE_MAX_GROUP_ITEMS` | 1 000 | Bounds Redis memory and the index a client downloads (~1 MB). |

Counted: blob size plus the metadata record. A hard-linked copy is counted in
full against the store even though the disk holds it once while the history
entry lives; the overcount lasts at most 24 hours.

### 6.6 Admin UI

Group page: Store switch, store bytes and items against its limits (separate from
history), store limit overrides, **Purge store**. Tag page: store limit. Dashboard:
total store usage. Sizes and counts only.

## 7. Wire

New `proto/tpp/v1/store.proto`:

- `StoreLimitsRequest/Response` — enabled, limits, usage, and whether the server
  accepts header v2.
- `StoreCopyBegin/Commit` — server-side copy from history (§5).
- `StoreUploadBegin/Chunk/Commit/Abort` — upload from clipboard or file.
- `StoreListRequest/Response` — paged by `saved_at`; per item: id, saved_at, size,
  content_id, content_epoch, wrap_epoch, wrapped_key, sealed_meta, meta_rev.
- `StoreLinksRequest/Response` — the link chain.
- `StoreFetchRequest` — chunked range read.
- `StoreUpdateMetaRequest` — new sealed metadata with `expected_meta_rev`, so two
  devices editing at once cannot silently lose an edit.
- `StoreDeleteRequest`.
- Events `StoreItemAdded/Updated/Deleted` (ids only), and `StoreStateChanged`
  when the admin turns the store on or off.

Changes to existing messages:

- `RekeyRequest` gains `bytes store_link`. The server refuses a rekey without it
  when the group has store items, so a client that predates the store cannot
  orphan them ("update the app" error). Clients always send it.
- `EntryPutRequest.header` and the upload begin accept 553 bytes.
- `ErrorCode` gains `STORE_DISABLED` (a new enum number, permanent).

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

## 9. Open questions

| # | Question | Proposal |
|---|---|---|
| Q1 | What does **disabling** a store that has items do? | Keep the items but block every store call; re-enabling brings them back. Deleting them is a separate **Purge store**. The admin confirmation says how much stays on disk. |
| Q2 | Should a **tag** be able to turn the store on for its groups (like tag storage limits), or only the group page? | Group page only, as asked. A creation token's starting tag could carry it later. |
| Q3 | Rollout order: header v2 needs the new server first. | One release for server and both clients; clients write v2 only when `StoreLimitsResponse` says the server accepts it. |
| Q4 | Should editing metadata or deleting an item need a confirmation? | Delete: yes. Edit: no. |
| Q5 | Default store limits (100 MiB, 1 000 items per group)? | As in §6.5. |

## 10. Order of work (one PR each)

1. **Spec** — SPEC store sections and the §3.2, §3.3, §4.5 amendments;
   `spec/crypto.md` store section and header v2, with vectors.
2. **Proto** — `store.proto`, `RekeyRequest.store_link`, `STORE_DISABLED`,
   `make proto`.
3. **Server** — header v2 acceptance, store records, namespace and quota, copy and
   upload, reaper and sweep, rekey check, enable switch, config, admin API and UI.
4. **Go client core** — header v2, store crypto, calls and index; interop tests.
5. **Desktop** — Store tab, save flows, header v2 writing, docs and screenshots.
6. **Android** — the same, plus `npm run interop` coverage.
7. ROADMAP status update.
