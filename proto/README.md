# Wire protocol (`tpp.v1`)

**Owned by:** Phase 1. **Spec:** SPEC §5.1, §3, §4.3, §6.

This directory is a **shared contract** (ROADMAP §3). Every downstream phase
consumes the generated bindings; none of them edits the schema. A phase that
needs a message, a field or an error code it cannot find opens an issue tagged
`contract-change`, which is merged as its own small PR.

## Shape of the protocol

Every frame on the WebSocket is a serialized `Envelope`:

```protobuf
message Envelope {
  string      id      = 1;  // correlation id, echoed on the response
  MessageType type    = 2;  // discriminator for payload
  bytes       payload = 3;  // the serialized message named by type
}
```

Nothing else is ever written to the socket. The concrete message is opaque
bytes, so replacing the carrier — HTTP/2, gRPC, anything — moves `Envelope`s and
leaves every other message in this package untouched. That is the reason SPEC
§5.1 keeps protobuf after rejecting gRPC.

| File | Contents |
|---|---|
| `tpp/v1/envelope.proto` | `Envelope`, `MessageType` |
| `tpp/v1/error.proto` | `Error`, `ErrorCode` |
| `tpp/v1/group.proto` | group creation, device roster, rekey (SPEC §3.1, §3.3) |
| `tpp/v1/pairing.proto` | pairing exchange and the QR/paste payload (SPEC §3.2) |
| `tpp/v1/entry.proto` | put / latest / history / fetch (SPEC §4.3, §6) |
| `tpp/v1/events.proto` | server-pushed events |

Conventions inside the schema:

- **Timestamps** are `int64` milliseconds since the Unix epoch, UTC — never a
  well-known `Timestamp`, so neither language needs the WKT descriptors and
  neither can accidentally involve local time (docs/conventions.md §6).
- **Key material** is always `bytes` and always opaque to the server. Algorithms
  and byte layouts are pinned by `/spec/crypto.md` (Phase 2), never here.
- **Enum numbers are permanent.** Renumbering an `ErrorCode` or a `MessageType`
  breaks clients that upgrade independently of the server.
- Nothing in the schema carries a content type, a filename or a plaintext
  length. Those live inside the ciphertext, which is what makes SPEC §2.3
  ("the server does not see content type") structural rather than aspirational.

## Deliberately absent

**Connection authentication frames.** SPEC §5 gives a connection a device
identity, but the proof a device presents depends on decisions Phase 2 has not
made yet (`/spec/crypto.md`). Inventing a `HelloRequest` here would pin a crypto
choice from the wrong phase. Phase 3c raises a `contract-change` issue once the
crypto spec exists; only `CreateGroupRequest` and `PairingJoinRequest` are legal
on an unauthenticated connection, and that is already stated in their comments.

**Anything resembling pin, favourite or extend-TTL.** `EntryMeta.expires_at` is
fixed at write time. The blob garbage collector depends entirely on entry
lifetimes being immutable (SPEC §4.5, standing prohibition).

## Generating

```sh
make proto          # regenerate both languages
make proto-check    # regenerate and fail if anything changed
```

`proto/generate.sh` does the work. It needs Go and Node and **no `protoc`
binary**: `buf` compiles the schema itself. Both generators are pinned —
`tools/go.mod` for `buf` and `protoc-gen-go`, `package-lock.json` for
`ts-proto` — so the same schema produces byte-identical output on every machine.
That is what makes the committed output checkable in CI.

The script formats (`buf format --write`), lints (`buf lint`), generates, and
then typechecks the generated TypeScript with `tsc --noEmit`. A schema change
that produces uncompilable output fails locally rather than in Phase 6a.

| Output | Consumer |
|---|---|
| `/pkg/tppclient/protogen/tppv1/*.pb.go` | Go client core (P4), server (P3c) |
| `/mobile/src/protocol/gen/tpp/v1/*.ts` | React Native app (P6a) |

Both directories are **generated in full and committed**. `buf.gen.yaml` sets
`clean: true`, so they are emptied before each run and a deleted message cannot
leave a stale file behind. Do not hand-write anything in them — it will be
deleted on the next `make proto`.

## Consuming the generated code

**Go.** Import `github.com/Mark7888/two-place-paste/pkg/tppclient/protogen/tppv1`.
The `pkg/tppclient` module requires `google.golang.org/protobuf`; this phase
added that single line to `pkg/tppclient/go.mod`, which is the one file it
touched outside `/proto/**` and the generated directories — generated Go that
does not compile would not satisfy the P1 acceptance criteria.

**TypeScript.** `ts-proto` output imports `@bufbuild/protobuf/wire` at runtime,
so `/mobile/package.json` must declare `@bufbuild/protobuf` as a dependency when
Phase 6a creates it. `proto/package.json` pins the same version for the
typecheck only; the mobile app owns its real dependency list.
