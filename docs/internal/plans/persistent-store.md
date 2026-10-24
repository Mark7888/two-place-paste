# Plan — Persistent store

**Status:** draft for discussion. Nothing is built; §8 lists the questions that
must be answered before the first PR.
**Touches:** `/spec/**` (new crypto section + vectors), `/proto/**`, `/server/**`,
`/pkg/tppclient/**`, `/desktop/**`, `/mobile/**`, `/deploy/**`, `/docs/**`.

---

## 1. Goals

- A per-group **store** of items that do not expire. It is a separate feature
  beside history: the 24-hour lifetime of entries (SPEC §4.5, §6) does not
  change, and no history entry is ever pinned, extended or moved.
- Any history entry the client can read gets a **Save to store** action that asks
  for a name, an optional description and tags. Saving copies the content into
  the store; the history entry is untouched and still expires on time.
- Every store item is a blob, however small (no inline storage in Redis).
- A new **Store** tab on desktop and Android: list, text search over name and
  description, filter by tag, sort by creation date or by name, copy to
  clipboard or download/save as a file, edit, delete.
- Each group has **separate** storage limits for history and for the store.
- A store item lives until a device deletes it or the group is deleted.
- The server still learns nothing about content: not the bytes, not the content
  type or filename, and now also not the **name, description or tags**.
- Items stay readable across a rekey, and a revoked device cannot read items
  saved after its revocation.
- A client can tell whether a piece of content is already in the store.

## 2. What exists today

| Piece | Where | Relevance |
|---|---|---|
| Group key per epoch, wrapped per device | `spec/crypto.md` §4 | The only key material a device has. §7 forbids keeping old epochs' keys. |
| Entry container, header, frame | `spec/crypto.md` §5, §6 | The frame is reused as the store item's plaintext. |
| Rekey: one atomic request, epoch+1 | SPEC §3.3, `RekeyRequest` in `proto/tpp/v1/group.proto` | The place where store keys must be carried forward. |
| Blob backend, hour buckets, sweeper | `server/internal/blob` | The sweep skips names that are not hour buckets (`disk.go` `Sweep`), but `tpp gc --verify` would call store blobs orphans if they shared the root. |
| Chunked upload/fetch | SPEC §4.3, `server/internal/entries/uploads.go` | Reused for store uploads and downloads. |
| Quota: global + per group, overrides on group/tag, eviction | `server/internal/quota` | History evicts when full. The store must never evict. |
| Group delete → `PurgeGroup` | `server/internal/admin/groups.go`, `entries/service.go` | Deletes entry by entry inside the admin request. Fine for 24 h of history; not for an unbounded store. |

## 3. Decisions (proposed)

| # | Decision | Why |
|---|---|---|
| D1 | Every item has its own random **item key**. The item key is wrapped under the **store key of the epoch it was saved in**. | The blob is encrypted once and never again; only a 72-byte wrap ties it to an epoch. |
| D2 | Each epoch has a store key derived from its group key. On rekey, the revoking device also uploads a **link**: the previous epoch's store key sealed under the new one. The links form a chain current → oldest, stored by the server in the persistent zone. | O(1) work per rekey, atomic with the rekey itself, no re-upload of anything. Same idea as Keybase team key generations. |
| D3 | A revoked device keeps nothing new: it knows the store keys up to its revocation (it could have downloaded those items anyway) but never a later one, and links only point backwards. | Keeps the "rekey is a hard break" property of `spec/crypto.md` §4.1 for everything saved after the revocation. |
| D4 | Name, description, tags, content type, filename, plaintext size and a content hash live in a **sealed metadata** record of fixed size, encrypted under the item key. | SPEC §2.3. Fixed size so the server learns nothing from name lengths. |
| D5 | Search, tag filter and sort by name run **on the client** over the decrypted metadata. The server only lists and pages by item id and server time. | The server cannot index what it cannot read. A store of a few thousand items is a few MB of metadata. |
| D6 | Duplicate detection is a SHA-256 of the content **inside** the sealed metadata, checked by the client against its decrypted index. **No hash is visible to the server.** | A plaintext hash on the server would let it confirm guesses ("is this item the text `yes`?"). The client loads the index for search anyway. |
| D7 | Store blobs live under their own root: `store/<group_id>/<item_id>.bin` (disk) and the same under a prefix on S3. Never inside the hour buckets. | Keeps SPEC §4.5 and its invariant intact; one prefix per group makes group deletion a prefix delete. |
| D8 | The store never evicts. A save that does not fit is refused with `QUOTA_EXCEEDED`. | Persistent means persistent. |
| D9 | Group deletion removes the group's store **asynchronously**: the admin request queues the group, a background reaper deletes its prefix, and the sweep removes any `store/<group_id>/` whose group no longer exists. | Answers "won't one delete per item freeze?" — see §5. |
| D10 | A newly paired device **can** read the whole store. | Unlike history (SPEC §3.2 "new devices start empty"), the store exists to outlive devices. Needs a SPEC edit. |

## 4. Cryptography (new `spec/crypto.md` section, still `tpp-crypto-v1` primitives)

No new primitive: HKDF-SHA256 and XChaCha20-Poly1305 only. New domain strings:
`tpp/v1/store`, `tpp/v1/store-link`, `tpp/v1/store-link-aad`,
`tpp/v1/store-wrap`, `tpp/v1/store-wrap-aad`, `tpp/v1/store-aad`,
`tpp/v1/store-meta`, `tpp/v1/store-meta-aad`.

```
store_key[E]   = HKDF(ikm = group_key[E], salt = <empty>,
                      info = "tpp/v1/store" || u64be(E), L = 32)

-- per item, at save time, in the client's current epoch E
item_key       = 32 random bytes
wrap_nonce     = 24 random bytes
wrapped_item   = u8(1) || wrap_nonce || XChaCha20-Poly1305(
                    key   = HKDF(store_key[E], wrap_nonce, "tpp/v1/store-wrap" || u64be(E)),
                    nonce = wrap_nonce,
                    aad   = "tpp/v1/store-wrap-aad" || u8(1) || u64be(E) || len32(item_id) || item_id,
                    pt    = item_key)                                         -- 73 bytes

blob           = u8(1) || nonce || XChaCha20-Poly1305(
                    key = HKDF(item_key, nonce, "tpp/v1/store"), nonce,
                    aad = "tpp/v1/store-aad" || u8(1) || len32(item_id) || item_id,
                    pt  = encoded_frame)                                      -- §6 frame, unchanged

sealed_meta    = same shape under HKDF(item_key, meta_nonce, "tpp/v1/store-meta"),
                 aad binds item_id and a meta revision number, plaintext a fixed
                 1024-byte record: version, name, description, tags, content type,
                 filename, body length, sha256(body), saved_at

-- on rekey E → E+1, made by the revoking device, which holds both keys
link[E+1]      = u8(1) || XChaCha20-Poly1305(
                    key   = HKDF(store_key[E+1], <empty>, "tpp/v1/store-link"),
                    nonce = 24 zero bytes,                    -- key used once
                    aad   = "tpp/v1/store-link-aad" || u8(1) || u64be(E) || u64be(E+1),
                    pt    = store_key[E])                                     -- 49 bytes
```

Reading an item wrapped at epoch W while the client is at epoch E ≥ W: derive
`store_key[E]`, open `link[E]` to get `store_key[E-1]`, and so on down to W. The
chain is a handful of links (a revocation is rare, SPEC §2.2), and a client may
cache the opened store keys for the session. **This is a deliberate, scoped
exception to §7's "never keep old keys"**: it applies to store keys only, the
old keys are reachable only from the newest one, and history entries still use
the epoch rule as before.

Why the item key and the metadata use the item key rather than the store key
directly: renaming an item, or later re-wrapping old items to the current epoch
(optional lazy migration, which would let the chain be shortened), changes only
73 bytes and never the blob.

Vectors: a new `store.json` suite (derivation, wrap, blob, meta, a three-link
chain) and failure cases (wrong item id, wrong epoch in a link, a link out of
order, a wrap from a newer epoch than the client holds).

## 5. Server

### 5.1 Records (Redis persistent zone, no TTL)

```
group:<id>:store            zset   item ids scored by saved_at (server time)
store:<item_id>             hash   group_id, wrap_epoch, size, saved_at,
                                   wrapped_item_key, sealed_meta, meta_rev,
                                   state (pending | committed)
group:<id>:store:links      hash   epoch -> link
store:pending               zset   item ids of uploads in progress, by start time
store:reclaim               set    group ids whose store is being deleted
```

About 1.2 KB per item. Redis memory grows with the store, so the docs must say
so, and the per-group item count gets a limit too (§5.4).

### 5.2 Write ordering and crash safety

1. Begin: write the record as `pending` and add it to `store:pending`; reserve
   room in the store quota.
2. Chunks go to `.uploads/` staging, as today.
3. Commit: move the blob to `store/<group_id>/<item_id>.bin`, then mark the
   record `committed`, then add it to the group's zset and remove it from
   `store:pending`.

The sweep looks at `store:pending` entries older than the upload limit (1 h)
and deletes their blob and record. That list is only as long as the uploads in
flight, so reconciliation never lists the whole store.

User deletion: remove the record and zset member first, then the blob. A blob
whose delete fails is added to a small retry set the sweep drains.

### 5.3 Group deletion — your question

Sending one delete per item inside the admin request would indeed stall on a
big store (and on S3 each delete is a round trip). A pure "GC finds it later"
approach needs a scan to find it. The proposal combines both:

- `deleteGroup` already removes the group record atomically, which is what cuts
  devices off. It also adds the group id to `store:reclaim` in the same step
  and returns at once.
- A background reaper (owned by the sweeper goroutine, bounded) deletes the
  group's store records in pages and the prefix `store/<group_id>/`: on disk one
  `RemoveAll`, on S3 a listing plus `DeleteObjects` in batches of 1000, so 10 000
  items are about 10 requests. Then it drops the group from `store:reclaim`.
- Backstop: every sweep lists the top-level group prefixes under `store/` (one
  name per group, not per item) and reclaims any whose group record is gone.
  That covers a crash between the delete and the reaper. Unlike the history
  sweep (§4.5), this one does read Redis, once per group.

The admin UI shows "store being deleted" until the reaper finishes. The same
reaper serves an admin **Purge store** action.

### 5.4 Limits

| Setting (proposed) | Default | Notes |
|---|---|---|
| `TPP_STORE_MAX_TOTAL_BYTES` | 0 (off) | Separate from `TPP_BLOB_MAX_TOTAL_BYTES`; history and store never evict each other. |
| `TPP_STORE_MAX_GROUP_BYTES` | 1 GiB | Overridable per tag and per group in the admin UI, same precedence as the blob limit (SPEC §4.4). |
| `TPP_STORE_MAX_ITEM_BYTES` | = entry cap (50 MiB) | |
| `TPP_STORE_MAX_GROUP_ITEMS` | 10 000 | Bounds Redis memory and the size of the index a client downloads. |

Sizes counted: blob plus the fixed metadata record. A full store refuses with
`QUOTA_EXCEEDED`; nothing is evicted.

### 5.5 Admin UI

Per group: store bytes and item count against its limits, separate from
history; store limits on the group and tag pages; **Purge store** behind a
confirmation. Dashboard: total store usage. Still sizes and counts only.

## 6. Wire (`proto/tpp/v1/store.proto`, new)

- `StoreLimitsRequest/Response` — limits and usage for the caller's group.
- `StoreUploadBegin / Chunk / Commit / Abort` — the entry upload flow with a
  store target; begin carries item id, size, wrap epoch, wrapped item key and
  sealed metadata. Every item takes this path, small or large.
- `StoreListRequest/Response` — paged by `saved_at`, returns per item: id,
  saved_at, size, wrap_epoch, wrapped_item_key, sealed_meta, meta_rev.
- `StoreLinksRequest/Response` — the link chain.
- `StoreFetchRequest` — chunked range read, like `EntryFetchChunkRequest`.
- `StoreUpdateMetaRequest` — new sealed metadata with `expected_meta_rev`
  (compare-and-set, so two devices editing at once do not lose an edit).
- `StoreDeleteRequest`.
- Events `StoreItemAdded / Updated / Deleted` (ids only) for open Store tabs.
- `RekeyRequest` gains `bytes store_link`. The server refuses a rekey without
  it once the group has a store (see Q9).

## 7. Clients

`pkg/tppclient` and `mobile/src/core` both get: store key derivation and chain
walk, item seal/open, metadata codec, the store calls, and an in-memory index
(decrypted metadata) with search, tag filter and sort. Saving from history:
fetch and open the entry (already done for copy), compute the hash, look it up
in the index, ask for name/description/tags (pre-filled from the filename, and
"already saved as …" if the hash matches), seal, upload.

UI: a **Store** tab on desktop (`desktop/ui`) and Android; a **Save to store**
button on each History row and on the current entry on the main page. Copy for
text and images, Save/Download for files (desktop: file dialog; Android: the
system file picker). Docs site pages and screenshots in the same PRs.

## 8. Open questions

| # | Question | Proposal |
|---|---|---|
| Q1 | Losing every device now loses the store forever (today it loses 24 h of history, SPEC §3.3 "Known limitation"). Do we want a recovery key or an encrypted export? | Out of scope for v1, but say it loudly in the docs and the Store tab. A printable recovery key can be added later without a format change (it would wrap `store_key`). |
| Q2 | Should a new device see the whole store (D10)? | Yes. |
| Q3 | Tags: free-text strings inside each item's metadata, or a sealed per-group tag list (rename once, colours)? | Free-text in metadata for v1; the tag list is the union. Renaming a tag rewrites the metadata of its items. |
| Q4 | One tag or several per item? You wrote both. | Several. |
| Q5 | Can items be saved from outside history too (a file picker, the current clipboard)? | Yes, it is the same upload; costs one button. |
| Q6 | Saving a large entry from a phone means download then re-upload (up to 50 MB each way). Do we want a server-side copy? It is possible (wrap the entry's content key as the item key, keep its id and epoch for the AAD), but it ties the store format to the history format. | No for v1; client-side copy only. |
| Q7 | Should History rows show "saved" without fetching the entry? That needs the content hash in the sealed header (`spec/crypto.md` §5.5, a header v2). | Later. v1 checks at save time, after the fetch. |
| Q8 | Is a duplicate refused, or only warned about? | Warn, allow "save anyway" (same text, different name or tags can be useful). |
| Q9 | Rollout: a client that predates the store and does a rekey would not send a link and orphan every item. | The server refuses `RekeyRequest` without `store_link` once the group has a store, with a "update the app" error. Store ships to server and both clients together. |
| Q10 | Does a revoked device's past access matter for old items (a stolen laptop)? Rekeying does not re-encrypt them. | Accept it, as for history; optionally a later "re-encrypt store" action that re-uploads every item. |
| Q11 | Can any device delete or edit any item? | Yes, same as revocation: no permission levels (SPEC §3.3). |
| Q12 | Should the admin's existing **Purge entries** also purge the store? | No: two separate buttons. |
| Q13 | Is the metadata index cached on disk on the device for fast startup? | No for v1: memory only, fetched when the Store tab opens (like history, SPEC §6). |
| Q14 | Size of the metadata record: 1024 bytes allows a name of ~100 characters, a description of ~500 bytes and ~10 tags. Enough? | Yes, with visible counters in the dialog. |
| Q15 | Store usage visible to the user (bar in the Store tab)? | Yes, from `StoreLimitsResponse`. |

## 9. Order of work (one PR each)

1. **Spec** — SPEC sections for the store, the §3.2/§3.3/§4.5 amendments, and
   `spec/crypto.md` store section with vectors (shared contract, own PR).
2. **Proto** — `store.proto`, `RekeyRequest.store_link`, `make proto`.
3. **Server** — records, blob namespace, quota, uploads, reaper and sweep, ws
   handlers, rekey check, config and `.env.example`, admin API and UI.
4. **Go client core** — crypto, chain, calls, index; interop test against the
   relay.
5. **Desktop** — Store tab, Save to store, docs and screenshots.
6. **Android** — the same, plus `npm run interop` coverage of store items.
7. ROADMAP status update.
