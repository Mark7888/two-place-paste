# TwoPlacePaste — Cryptography Specification

**Profile:** `tpp-crypto-v1`
**Version:** 1.0
**Status:** normative for MVP
**Companion to:** [`docs/SPECS.md`](../docs/SPECS.md) §2, §3
**Vectors:** [`spec/vectors/`](vectors/) — 42 vectors, 6 suites

This document defines every byte of TwoPlacePaste cryptography. It exists so
that the Go client (`pkg/tppclient`, Phase 4) and the TypeScript client
(`mobile/src/crypto`, Phase 6a) cannot silently disagree. Where an
implementation and this document differ, the implementation is wrong.

The key words MUST, MUST NOT, SHOULD, SHOULD NOT and MAY are to be interpreted
as described in RFC 2119.

The server implements **none** of this. It stores and relays the byte strings
defined here and cannot read any of them (SPEC §2.1).

---

## 1. Scope

| In scope | Out of scope |
|---|---|
| Device keypairs | Transport security (TLS, terminated by the reverse proxy, SPEC §4.1) |
| Group key generation and wrapping | Server-side authentication of devices (Phase 3c) |
| Entry encryption and the plaintext frame | Admin session cookies (Phase 3d) |
| Epoch handling across rekey | Desktop localhost UI tokens (SPEC §7.2) |
| What every implementation must reject | Key storage at rest (§10) |

---

## 2. Conventions

### 2.1 Notation

- `||` is concatenation.
- `u8(x)`, `u16be(x)`, `u32be(x)`, `u64be(x)` are unsigned big-endian integers of
  1, 2, 4 and 8 bytes. **All integers in this specification are big-endian.**
- `"literal"` is the ASCII bytes of the literal, with **no** trailing NUL and no
  length prefix unless one is written explicitly.
- `bytes[n]` is a fixed-length field of `n` bytes.
- Hex in this document and in the vectors is lowercase and unprefixed.

### 2.2 Primitives

These are pinned here and are **not** chosen per phase (`docs/conventions.md` §9).

| Purpose | Algorithm | Parameters |
|---|---|---|
| Device / ephemeral keys | X25519 (RFC 7748) | 32-byte private, 32-byte public |
| Key derivation | HKDF-SHA256 (RFC 5869) | extract-then-expand, 32-byte output |
| AEAD | XChaCha20-Poly1305 | 32-byte key, 24-byte nonce, 16-byte tag |
| Randomness | platform CSPRNG | Go `crypto/rand`, WebCrypto `getRandomValues` |

There is deliberately **one** AEAD and **one** KDF in the whole system. Every
key-derivation step below is HKDF-SHA256 and every encryption step below is
XChaCha20-Poly1305; an implementation needs exactly three primitives.

**Pinned libraries**

| Language | Libraries |
|---|---|
| Go | `crypto/ecdh` (X25519), `crypto/hkdf`, `golang.org/x/crypto/chacha20poly1305` |
| TypeScript | `@noble/curves` (x25519), `@noble/hashes` (hkdf, sha256), `@noble/ciphers` (xchacha20poly1305) |

Both sets are pure-language implementations with no native module to maintain
per platform, which is what SPEC §5.1 demands of the React Native client.
Adding a fourth primitive, or a second library for an existing one, is a change
to this document (§12).

### 2.3 Domain separation

Five ASCII strings, each used for exactly one purpose. No string is a prefix of
another **in a position where it could be confused**, and every variable-length
value that follows one carries an explicit length prefix.

| String | Used as | Defined in |
|---|---|---|
| `tpp/v1/wrap` | HKDF `info` prefix, key wrapping | §4.2 |
| `tpp/v1/wrap-aad` | AEAD associated data prefix, key wrapping | §4.2 |
| `tpp/v1/entry` | HKDF `info` prefix, content key | §5.1 |
| `tpp/v1/entry-aad` | AEAD associated data prefix, entries | §5.3 |
| `tpp/v1/vectors` | HKDF `ikm` for deterministic test material only | §9.2 |

### 2.4 Version byte

Every ciphertext container defined here begins with `u8(version)` and binds that
same byte into its AEAD associated data. For this profile the value is `0x01`.

A recipient MUST reject a container whose version byte it does not implement,
**before** deriving any key material. This is what makes a future
`tpp-crypto-v2` a rollout problem rather than a decryption-failure problem.

---

## 3. Device keys

Each device generates one X25519 keypair on first launch (SPEC §2.2).

```
device_private  = 32 random bytes from the CSPRNG
device_public   = X25519(device_private, basepoint)
```

- The private key MUST NOT leave the device — not to the server, not to another
  device, not into a log or an error message (`docs/conventions.md` §1).
- Scalar clamping is performed inside the scalar multiplication, per RFC 7748
  §5. The stored private key is the raw 32 bytes as generated; implementations
  MUST NOT store a pre-clamped scalar, because the vectors give the raw bytes.
- An X25519 shared secret that is all zeroes MUST be rejected (RFC 7748 §6.1).
  Both pinned libraries do this; an implementation that rolls its own MUST too.

Vectors: [`x25519.json`](vectors/x25519.json).

---

## 4. Group keys

### 4.1 Generation

```
group_key = 32 random bytes from the CSPRNG
```

A group key is generated in exactly two places (SPEC §3):

1. **Group creation** (§3.1 step 4) — the creating device generates the first
   group key at **epoch 1**.
2. **Rekey after revocation** (§3.3 step 3) — the revoking device generates a
   fresh group key at **epoch + 1**.

A group key MUST NOT be derived from, or reused between, epochs. A rekey is a
new random key, not a ratchet: revocation must be a hard break.

The group key is never used directly to encrypt an entry (SPEC §2.2). It is
only ever an HKDF input (§5.1) or an AEAD plaintext (§4.3).

### 4.2 Wrap derivation

Wrapping is an anonymous public-key encryption of the group key to one device.
It is a DHKEM-shaped construction in the style of RFC 9180 base mode, built from
the three primitives of §2.2 and nothing else.

Given a recipient public key `recipient_public` and an epoch:

```
ephemeral_private = 32 random bytes from the CSPRNG
ephemeral_public  = X25519(ephemeral_private, basepoint)
shared            = X25519(ephemeral_private, recipient_public)

wrap_info = "tpp/v1/wrap" || ephemeral_public[32] || recipient_public[32]
wrap_key  = HKDF-SHA256(ikm = shared, salt = <empty>, info = wrap_info, L = 32)

wrap_aad  = "tpp/v1/wrap-aad" || u8(0x01) || u64be(epoch)
```

- `salt = <empty>` means HKDF-Extract is called with a zero-length salt, which
  RFC 5869 §2.2 defines as `HashLen` zero bytes. Go's `hkdf.Key` takes `nil`;
  noble's `hkdf` takes `undefined`.
- Both public keys are bound into `info`, so a wrapped key is cryptographically
  tied to the recipient it was made for.
- The epoch is bound into the AAD, so a wrapped key from an earlier epoch cannot
  be replayed as the current one by the server.

**A fresh `ephemeral_private` MUST be generated for every wrap operation** — per
recipient, not per rekey. A rekey to three remaining devices performs three
wraps with three ephemeral keypairs.

### 4.3 Wrap container

```
wrapped_key =
    u8(0x01)                 version
 || ephemeral_public[32]
 || ciphertext[32]           XChaCha20-Poly1305 of group_key
 || tag[16]

encryption: XChaCha20-Poly1305(
                key   = wrap_key,
                nonce = 24 zero bytes,
                aad   = wrap_aad,
                plaintext = group_key[32])
```

Total length is **81 bytes**, always. A blob of any other length MUST be
rejected without attempting decryption.

The all-zero nonce is safe **because `wrap_key` is used for exactly one
message**: it is derived from a fresh ephemeral scalar, so no two wrap
operations share a key. An implementation that caches or reuses
`ephemeral_private` across recipients breaks this and MUST NOT exist.

### 4.4 Unwrap

```
1. Reject unless len(wrapped_key) == 81 and wrapped_key[0] == 0x01.
2. ephemeral_public = wrapped_key[1..33]
3. shared    = X25519(device_private, ephemeral_public)
4. wrap_key  = HKDF-SHA256(shared, <empty>, "tpp/v1/wrap" || ephemeral_public || device_public, 32)
5. group_key = XChaCha20-Poly1305-Open(wrap_key, 24 zero bytes, wrap_aad, wrapped_key[33..81])
```

Note that step 4 uses the recipient's **own** public key, which it recomputes
from its private key. It does not read a public key from the wire.

Vectors: [`wrap.json`](vectors/wrap.json), and the `unwrap-*` cases in
[`failure.json`](vectors/failure.json).

---

## 5. Entry encryption

### 5.1 Content key

```
entry_nonce = 24 random bytes from the CSPRNG, fresh for every entry

entry_info  = "tpp/v1/entry" || u64be(epoch)
content_key = HKDF-SHA256(ikm = group_key, salt = entry_nonce, info = entry_info, L = 32)
```

The nonce is the HKDF salt **and** the AEAD nonce. This is deliberate: the
content key is unique per entry, so the AEAD's nonce-reuse budget is one message
per key rather than 2^96 messages under the group key. A repeated nonce
therefore does not produce a keystream collision under a shared key; it produces
the same content key, which is no worse than re-encrypting the same entry.

The nonce MUST come from the CSPRNG. Counters, timestamps and hashes of the
plaintext are all forbidden.

Vectors: [`derive.json`](vectors/derive.json).

### 5.2 Entry container

```
container =
    u8(0x01)          version
 || entry_nonce[24]
 || ciphertext[n]     XChaCha20-Poly1305 of the plaintext frame (§6)
 || tag[16]

encryption: XChaCha20-Poly1305(
                key   = content_key,
                nonce = entry_nonce,
                aad   = entry_aad,
                plaintext = encoded_frame)
```

`container` is the byte string the server stores — inline in Redis at ≤256 KB,
in the blob backend above that (SPEC §4.3). The **10 MB cap is measured on
`container`**, not on the plaintext: the server has no knowledge of plaintext
size, and the cap it enforces is the size it actually receives.

### 5.3 Associated data

```
entry_aad =
    "tpp/v1/entry-aad"
 || u8(0x01)                     version
 || u64be(epoch)
 || u32be(len(entry_id))         length in bytes of the UTF-8 encoding
 || entry_id                     raw UTF-8 bytes, no terminator
```

`entry_id` is the server-assigned identifier, treated here as an opaque UTF-8
string. Binding it means an entry cannot be relabelled, duplicated under a new
id, or replayed as a different entry by the server. Binding the epoch means an
entry cannot be presented as belonging to a later key generation.

The length prefix is what makes the encoding unambiguous: without it,
`(epoch, "ab" + "c")` and `(epoch, "ab" + "c")` split differently would collide.

**`content_type` is not in the AAD.** It is inside the encrypted frame (§6),
which authenticates it just as strongly and additionally hides it from the
server, as SPEC §2.3 requires. See §11.2.

### 5.4 Decryption

```
1. Reject unless len(container) >= 41 and container[0] == 0x01.
2. entry_nonce = container[1..25]
3. content_key = HKDF-SHA256(group_key, entry_nonce, "tpp/v1/entry" || u64be(epoch), 32)
4. frame       = XChaCha20-Poly1305-Open(content_key, entry_nonce, entry_aad, container[25..])
5. Decode the frame per §6. A frame that does not decode is a failure, not a
   partially usable entry.
```

The `epoch` in steps 3 and 4 is the epoch the **server** reports for the entry.
A client MUST NOT try other epochs to make an entry decrypt; see §7.

Vectors: [`entry.json`](vectors/entry.json), and the `entry-*` cases in
[`failure.json`](vectors/failure.json).

---

## 6. Plaintext frame

The frame is what the AEAD protects. It carries the content type and filename,
so neither reaches the server (SPEC §2.3).

It is a fixed binary encoding rather than protobuf: `proto/` is a shared
contract owned by Phase 1 (ROADMAP §3), and the plaintext framing must not
become a cross-phase dependency. The server never parses this structure — it
only ever sees the ciphertext — so there is nothing for the transport schema to
gain from owning it.

```
encoded_frame =
    u8(0x01)                      frame version
 || u16be(len(content_type)) || content_type    UTF-8, MUST be non-empty
 || u16be(len(filename))     || filename        UTF-8, length 0 means absent
 || u64be(created_at_unix_ms)                   UTC milliseconds since 1970-01-01
 || u32be(pad_len)                              MUST be 0 in v1
 || u32be(len(body))         || body            the clipboard payload
 || pad[pad_len]                                zero bytes
```

Rules:

- The encoding is **canonical**: one frame has exactly one valid serialization.
  A decoder MUST reject trailing bytes after `pad`, and MUST reject any length
  prefix that exceeds the remaining input.
- `content_type` is an IANA media type, optionally with parameters — e.g.
  `text/plain; charset=utf-8`, `image/png`, `application/pdf`. It MUST NOT be
  empty; an unknown type is `application/octet-stream`.
- `filename` is present only for file entries and is a bare filename: it MUST
  NOT contain a path separator, and a consumer MUST treat it as untrusted
  display text, never as a path to write to.
- `created_at_unix_ms` is the **client's** clock, in UTC, and is advisory. The
  server's own timestamp governs TTL and ordering (SPEC §4.5, §6). A client MUST
  NOT trust this field for expiry decisions.
- `pad_len` is reserved for the plaintext padding left open by SPEC §9. In v1 an
  encoder MUST write 0 and a decoder MUST reject a non-zero value, so that
  padding can be introduced as `tpp-crypto-v2` without ambiguity.

Vectors: [`frame.json`](vectors/frame.json), and the `frame-*` cases in
[`failure.json`](vectors/failure.json).

---

## 7. Epochs

The epoch is a `u64` that identifies the group key generation (SPEC §1.3). It
starts at **1** on group creation and increments by 1 on every rekey. It is
carried in `u64be` form everywhere it is bound (§4.2, §5.1, §5.3).

A client holds exactly one `(epoch, group_key)` pair at a time.

| Situation | Required behaviour |
|---|---|
| Entry epoch **equals** the client's epoch | Decrypt normally. |
| Entry epoch is **older** | **Skip silently** (SPEC §3.3). No error to the user, no retry, no attempt to keep old keys around. Such entries expire within 24 hours. |
| Entry epoch is **newer** | The client is behind a rekey. It MUST fetch its new wrapped key, unwrap it (§4.4), install the new epoch, and only then decrypt. If no wrapped key is available, skip the entry. |
| Decryption fails at the client's own epoch | Report it. This is corruption or tampering, not a normal condition. |

A client MUST NOT retain group keys from previous epochs in order to read older
entries. Forward secrecy across revocation is the entire point of the rekey
(SPEC §3.3), and a client that keeps old keys silently defeats it.

**Local clipboard is never touched by a rekey** (SPEC §3.3): installing a new
epoch is a key operation only.

---

## 8. Where each operation happens

Mapping this document onto the group lifecycle in SPEC §3:

| SPEC step | Operation | This document |
|---|---|---|
| §3.1 step 4 — create group | generate device keypair, generate group key at epoch 1, wrap to self | §3, §4.1, §4.2 |
| §3.2 step 1 — inviter starts pairing | generate a **pairing** ephemeral keypair | §3 |
| §3.2 step 2 — joiner joins | generate device keypair, send public key | §3 |
| §3.2 step 4 — inviter wraps to joiner | wrap the current group key at the current epoch | §4.2 |
| §3.2 step 5 — joiner is a member | unwrap, install `(epoch, group_key)` | §4.4 |
| §3.3 step 3 — rekey | generate a new group key at epoch+1, wrap once per remaining device | §4.1, §4.2 |
| §3.3 step 4 — atomic upload | all wrapped keys in one request; the server applies them or none | server-side, Phase 3a |
| §6 — put an entry | frame, derive content key, seal | §6, §5.1, §5.2 |
| §6 — read an entry | epoch check, open, decode | §7, §5.4, §6 |

Note that the pairing ephemeral keypair of SPEC §3.2 step 1 (transported inside
the QR payload) and the wrap ephemeral keypair of §4.2 are **different keys with
different lifetimes**. The pairing key authenticates the pairing channel and is
Phase 3c's concern; the wrap key exists for the duration of a single 81-byte
container.

---

## 9. Test vectors

### 9.1 Corpus

[`spec/vectors/`](vectors/) holds 42 vectors in 6 suites, indexed by
[`index.json`](vectors/index.json). Every byte string is lowercase hex. A client
that cannot reproduce every vector does not ship (`docs/conventions.md` §11).

| Suite | Vectors | Asserts |
|---|---|---|
| `x25519.json` | 6 | public key derivation; agreement matches from both sides |
| `wrap.json` | 3 | every intermediate of §4 — shared secret, info, wrap key, AAD, container |
| `derive.json` | 5 | §5.1 content keys, including epoch `2^64-1` to pin the big-endian encoding |
| `frame.json` | 5 | §6 encoding, including Unicode, binary, filename and empty body |
| `entry.json` | 5 | §5 end to end |
| `failure.json` | 18 | inputs every implementation MUST reject |

The failure suite is the important half. Wrong recipient, wrong epoch, wrong
entry id, wrong group key, tampered ciphertext, tampered nonce, tampered
ephemeral key, truncation, unknown version, trailing bytes, non-zero `pad_len`,
empty content type — for each, the only correct outcome is an error. **An
implementation that returns plaintext for any of these is broken, not lenient.**

`epoch` is emitted both as a JSON number and as `epoch_hex` (its `u64be`
encoding). **`epoch_hex` is authoritative:** a `u64` above 2^53 does not survive
JavaScript's `JSON.parse`, and one vector deliberately uses `2^64-1`.

### 9.2 Regenerating

The vectors are generated from the reference implementation in
[`vectors/gen/`](vectors/gen/), which is a spec tool, not product code — it is a
standalone Go module and is deliberately absent from `go.work` (a shared
touchpoint owned by Phase 0, ROADMAP §3).

```sh
GOWORK=off go run ./spec/vectors/gen -out spec/vectors           # regenerate
GOWORK=off go run ./spec/vectors/gen -out spec/vectors -check    # fail if stale
```

All vector inputs — keys, nonces, group keys, bodies — are derived
deterministically as `HKDF-SHA256(ikm = "tpp/v1/vectors", salt = <empty>, info =
<label>)`, so regeneration is byte-identical. Real clients draw these values
from the CSPRNG; the determinism exists only so that a hand-edited vector shows
up as a diff.

The generator round-trips every positive vector and asserts that every negative
vector actually fails before writing anything.

### 9.3 Consuming

[`vectors/verify.mjs`](vectors/verify.mjs) is a reference consumer: an
independent Node implementation of this document, written against the pinned
`@noble/*` libraries, that reproduces all 42 vectors. It is not part of any
build — it exists to demonstrate that the corpus is genuinely language-neutral
and to give Phase 6a a worked starting point.

---

## 10. Key storage

Out of scope for this document in the sense that it prescribes no format, but
two rules are normative because they are cryptographic:

- A device private key and the current group key MUST be stored where other
  local applications cannot read them: the OS keychain/credential store where
  one is available, and a file with owner-only permissions otherwise.
- Neither key, nor a wrapped key, nor a plaintext frame may be written to a log
  or embedded in an error (`docs/conventions.md` §1, §2).

---

## 11. Design notes

### 11.1 Key wrapping is not a libsodium sealed box

ROADMAP P2 recommended `crypto_box_seal`. This document specifies the
HKDF-SHA256 + XChaCha20-Poly1305 construction of §4 instead, for one reason:
**primitive count**.

A sealed box requires X25519, Blake2b-256 (for the nonce), HSalsa20 (for the
`crypto_box` key derivation) and XSalsa20-Poly1305 — four primitives, of which
three are used nowhere else in this system. In `@noble/ciphers` the HSalsa20
core is a low-level `hsalsa` export operating on `Uint32Array` scratch buffers,
so the React Native client would be assembling a sealed box from primitives
rather than calling one function.

The construction in §4 reuses X25519, HKDF-SHA256 and XChaCha20-Poly1305 —
exactly the three primitives §5 already requires — and is a single-recipient
instance of the DHKEM + AEAD shape standardised by RFC 9180 base mode. It is
anonymous to the same degree (the sender's identity is an ephemeral key that
appears nowhere else), which is all SPEC §3.2 step 4 asks for.

There is no libsodium peer anywhere in this system, so sealed-box wire
compatibility buys nothing.

### 11.2 The AAD binds epoch and entry id, not content type

ROADMAP P2 recommended `AAD = epoch || entry_id || content_type`. That is not
constructible: the recipient needs the AAD **before** it can decrypt, and
`content_type` lives inside the ciphertext precisely so the server cannot see it
(SPEC §2.3). Putting it in the AAD would mean either sending it in clear
alongside the entry — leaking it to the server — or requiring the recipient to
guess it.

The stated goal ("a re-typed or replayed entry fails") is met by §6: the content
type is inside the AEAD-protected frame, so changing it invalidates the tag just
as an AAD binding would, and it stays confidential as well as authenticated.

### 11.3 Rejected: `filippo.io/edwards25519`

ROADMAP P2 suggested it for the Go side. It is an Ed25519 field-arithmetic
package and is not needed: X25519 is in the Go standard library as
`crypto/ecdh`, and HKDF as `crypto/hkdf` since Go 1.24. The only non-stdlib Go
dependency this profile requires is `golang.org/x/crypto/chacha20poly1305`.

### 11.4 What this design does not protect against

- **Traffic analysis.** Ciphertext length leaks approximate plaintext length
  (SPEC §9). `pad_len` reserves the fix without implementing it.
- **A malicious server withholding entries.** It cannot read or forge them, but
  it can drop them, and clients have no way to detect that.
- **A malicious server lying about the device roster.** A revoking device wraps
  the new key to the public keys the server hands it (SPEC §3.3 step 1). SPEC
  §3.3 step 2 mitigates this at the UI level — the user confirms a named list of
  devices before the rekey — not cryptographically.
- **Compromise of a device.** It holds the group key and can read everything in
  the group until revoked and re-keyed.

---

## 12. Changing this document

`spec/crypto.md` and `spec/vectors/**` are a shared contract (ROADMAP §3).
Changes are never made inside a feature phase: an agent opens a
`contract-change` issue, and the change lands as its own PR that regenerates the
vectors and updates both client implementations.

Any change to a byte layout, a domain-separation string, a derivation or a
primitive is a **new profile**, `tpp-crypto-v2`, with a new version byte (§2.4).
The version byte is bound into every AAD specifically so that the two profiles
cannot be confused on the wire.
