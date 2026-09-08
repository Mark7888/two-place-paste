# Crypto test vectors

Cross-language test vectors for [`spec/crypto.md`](../crypto.md), profile
`tpp-crypto-v1`.

These fixtures are the contract between the Go client (`pkg/tppclient`,
ROADMAP P4) and the TypeScript client (`mobile/src/crypto`, ROADMAP P6a). Both
run them; a client that cannot reproduce every vector does not ship
(`docs/conventions.md` §11).

## Layout

| File | Suite | Vectors |
|---|---|---|
| `index.json` | manifest — suites, counts, totals | — |
| `x25519.json` | device keypairs and raw X25519 agreement | 6 |
| `wrap.json` | group key wrapping (crypto.md §4) | 3 |
| `derive.json` | HKDF content key derivation (§5.1) | 5 |
| `frame.json` | plaintext frame serialization (§6) | 5 |
| `entry.json` | end-to-end entry encryption (§5) | 5 |
| `failure.json` | inputs that MUST be rejected (§7) | 18 |

## Reading a vector

Every file has the same shape:

```json
{
  "schema_version": 1,
  "profile": "tpp-crypto-v1",
  "suite": "entry",
  "spec": "spec/crypto.md §5",
  "description": "...",
  "vectors": [ { "name": "text-ascii", "description": "...", ... } ]
}
```

- **Every byte string is lowercase, unprefixed hex.** `entry_id`,
  `content_type` and `filename` are the exceptions: they are the UTF-8 strings
  themselves, because that is how an implementation will hold them.
- **Read the epoch from `epoch_hex`, not `epoch`.** `epoch_hex` is the `u64be`
  encoding and is authoritative; `epoch` is the same value as a JSON number, for
  readability only. One vector uses `2^64-1`, which does not survive
  JavaScript's `JSON.parse`.
- `failure.json` vectors carry `operation` (`unwrap`, `open_entry`,
  `decode_frame`) and `expect: "error"`. The only correct outcome is an error.
  An implementation that returns plaintext for one of these is broken, not
  lenient.
- Intermediate values (`shared_secret`, `hkdf_info`, `wrap_key`, `content_key`,
  `aad`) are published deliberately. When a client's final ciphertext is wrong,
  comparing intermediates says *which step* is wrong.

## Regenerating

`gen/` is the reference implementation of `spec/crypto.md` and the source of
these files. It is a spec tool, not product code: a standalone Go module,
deliberately absent from `go.work` (a shared touchpoint owned by ROADMAP P0).

```sh
GOWORK=off go run ./spec/vectors/gen -out spec/vectors           # regenerate
GOWORK=off go run ./spec/vectors/gen -out spec/vectors -check    # fail if stale
```

Every input is derived deterministically from the label
`HKDF-SHA256(ikm = "tpp/v1/vectors", salt = <empty>, info = <label>)`, so
regeneration is byte-identical and a hand-edited vector shows up as a diff. Real
clients draw these values from the CSPRNG. The generator round-trips every
positive vector and asserts that every negative vector fails before it writes
anything.

## Verifying from another language

`verify.mjs` is an independent Node implementation of `spec/crypto.md`, written
against the `@noble/*` libraries the spec pins, that reproduces the whole
corpus. Nothing imports it; it exists to show the fixtures are genuinely
language-neutral and to give P6a a worked starting point.

```sh
npm i @noble/curves @noble/hashes @noble/ciphers
node spec/vectors/verify.mjs
```

## Changing them

`spec/crypto.md` and this directory are a shared contract (ROADMAP §3). A
feature phase never edits them — it opens a `contract-change` issue, and the
change lands as its own PR that regenerates the corpus and updates both clients.
