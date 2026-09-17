# Plan — joiner-emitted pairing (pairing in both directions)

**Status:** not implemented. Needs a wire contract change, so it cannot land inside a
feature phase (ROADMAP §3) — it is its own PR, or a short series of them.
**Raised by:** ROADMAP P7 review. The Android client implements every direction the
current contract allows; this document is the other half.
**Touches:** `/proto/**` (P1), `/server/**` (P3), `/pkg/tppclient/**` (P5),
`/desktop/**` (P6), `/mobile/**` (P7), `/e2e/**` (P8).

---

## 1. What works today, and what does not

Pairing today is **member-emitted**: the device that already holds a group key mints the
token and displays it; the device without one consumes it. The transport is free — QR or
copied text, in any combination — but the *direction* is fixed, because
`PairingStartRequest` is only legal on an authenticated connection
(`server/internal/ws/handlers.go`): only a member can mint a pairing token.

| # | Wanted | Direction | Today |
|---|---|---|---|
| 1 | Desktop paired: desktop shows QR, phone scans | member → joiner | ✅ |
| 2 | Desktop paired: desktop shows code, phone pastes | member → joiner | ✅ |
| 3 | Desktop paired: **phone generates code, desktop pastes** | joiner → member | ❌ |
| 4 | Phone paired: **desktop shows QR, phone scans** | joiner → member | ❌ |
| 5 | Phone paired: **desktop shows code, phone pastes** | joiner → member | ❌ |
| 6 | Phone paired: phone generates code, desktop pastes | member → joiner | ✅ |
| 7 | Phone A paired: A shows, B scans or pastes | member → joiner | ✅ |
| 8 | Phone A paired: **B shows, A scans or pastes** | joiner → member | ❌ |

Every ❌ is the same missing flow, not five of them: **an unpaired device emitting a code
that a member consumes.** Nothing about QR versus text is involved — that is already
symmetric, and the desktop deliberately has no scanner (SPEC §7.2), which is why the
text form has to keep working in both directions.

## 2. Why it cannot be faked in a client

The three things a joiner would have to put in a code are a relay URL, a pairing token,
and its own public key. It has the last one. It cannot mint a token — that takes an
authenticated connection — and a token is what the whole flow is keyed on. So the code an
unpaired device shows has to mean something new on the wire, and the relay has to be able
to hold it.

## 3. Recommended mechanism — a relay-held offer

The mirror image of the existing flow, with the roles swapped and `PairingComplete`
reused unchanged.

1. The joiner learns the relay URL (see §6 — this is the one real cost) and dials an
   unauthenticated socket.
2. It sends **`PairingOfferRequest{device_name, device_public_key}`**. The relay stores an
   offer under a fresh single-use code with a short TTL and answers
   **`PairingOfferResponse{offer_code, expires_at_unix_ms}`**. The joiner keeps the socket
   open, exactly as `PairingJoinRequest` does today.
3. The joiner displays a code carrying `{server_url, offer_code, device_public_key}`, as a
   QR and as the same string in text.
4. A member scans or pastes it. **Before anything is wrapped, the member's UI shows the
   joining device's name and a fingerprint of its public key and asks the user to
   confirm** (§5).
5. The member wraps the current group key to the public key *from the code* at the current
   epoch (`/spec/crypto.md` §4.2, unchanged) and sends
   **`PairingOfferAcceptRequest{offer_code, device_public_key, wrapped_group_key}`**.
6. The relay verifies the offer exists, is unconsumed and unexpired, and that
   `device_public_key` matches the stored offer **byte for byte**; then it creates the
   device record in the accepting member's group, stores the wrapped key, consumes the
   offer atomically, and pushes the existing **`PairingComplete`** to the joiner's waiting
   socket.
7. The joiner unwraps, installs `(epoch, group_key)` and reconnects authenticated —
   identical to step 5 of the flow that exists now.

The byte-for-byte key check in step 6 is what keeps the relay out of the trust path: the
member wraps to a key it read out of band, and a relay that substituted one would have to
make it match an offer it did not create.

## 4. Wire additions

Three message types, and one thing to get right.

```
MESSAGE_TYPE_PAIRING_OFFER_REQUEST        = 26
MESSAGE_TYPE_PAIRING_OFFER_RESPONSE       = 27
MESSAGE_TYPE_PAIRING_OFFER_ACCEPT_REQUEST = 28
```

**The thing to get right:** a scanned string is currently decoded as a bare
`PairingPayload`, and protobuf will happily decode an unrelated message as one without
error — field numbers, not names, are on the wire. A second kind of code therefore needs
an explicit discriminator, not a second bare message:

```protobuf
message PairingCode {
  oneof code {
    PairingPayload invite = 1;   // member-emitted, as today
    PairingOffer offer = 2;      // joiner-emitted
  }
}
```

Clients decode `PairingCode` first and fall back to a bare `PairingPayload`, so codes
shown by an older build keep working. `mobile/src/core/protocol/pairing.ts`'s
`classifyCode` already separates a creation link from a pairing code by shape and gains a
third answer here; it is the natural place for the fallback.

## 5. Security — the risk moves, and must be shown to the user

Today a hostile code costs the *joiner* nothing but a failed pairing: it holds no group
key to lose. Reverse pairing inverts that. A member who scans a hostile offer **admits a
device to the group** and hands it the group key, which is the strongest thing any actor
in this system can be given.

So the named confirmation of SPEC §3.3 step 2 applies here too, and normatively: a member
MUST render the offered device's name and a public-key fingerprint, and MUST NOT wrap
anything until the user confirms. A UI that pairs on scan alone is wrong, and the existing
revocation gate (`prepareRevoke` returning a plan that `confirm` consumes) is the shape to
copy — enforce it in the client API so a screen cannot skip it.

Other rules the relay enforces: one offer pairs exactly one device (consumed atomically),
a short TTL matching the 5 minutes of SPEC §3.2, accept is legal only on an authenticated
connection, and offer minting is rate-limited like the other unauthenticated paths.

A stolen offer code is not a key-disclosure: the wrapped key is bound to the offer's
public key, which belongs to the honest joiner. It is a nuisance (that device can be
admitted by a careless member), not a compromise.

## 6. The one real cost — the joiner must know where the relay is

A device with no group key also has no relay URL, and a code it shows has to say where the
offer is held. Two ways out:

- **(a) Ask for it, once.** The joiner's setup screen gains a relay URL field, and the
  code carries it. One extra field for the user, no extra protocol. **Recommended.**
- **(b) A second out-of-band hop.** The joiner's code carries only its name and public
  key; the member registers the device and then shows `{server_url, device_id, group_id,
  epoch}` back for the joiner to read. No URL to type, but two exchanges, and it puts a
  scanner requirement on whichever side lacks one.

Pick (a). (b) is worth keeping in mind only if typing a URL proves to be the thing users
get wrong.

## 7. Work, in landing order

1. **Contract** (`/proto/**`, P1): the three message types, `PairingOffer`, the
   `PairingCode` wrapper; regenerate Go and TypeScript; `make proto-check` stays green.
2. **Spec** (`/spec/crypto.md`, P2): no byte layout, derivation or primitive changes — the
   wrap is §4.2 exactly as it stands. Add the two rows to the §8 mapping table and a note
   under §3.2 that the wrap target may be learned from either side's out-of-band code.
   No vector changes.
3. **Server** (`/server/internal/{ws,store}`, P3): offer storage in the ephemeral zone
   with TTL, the two handlers, the atomic consume, the push. Tests: exactly-one-device, a
   mismatched public key, an expired offer, an unauthenticated accept, two members racing
   to accept the same offer.
4. **Go client** (`/pkg/tppclient`, P5): `StartOffer(ctx)` for an unpaired client
   (returns the code and a handle that resolves when a member accepts) and
   `PrepareAcceptOffer(ctx, code)` → `Confirm()` for a member, so the confirmation of §5
   is enforced by the type system rather than by a screen remembering.
5. **Desktop** (`/desktop`, P6): show an offer when unpaired (QR + text); paste an offer
   when paired, with the confirmation dialog.
6. **Mobile** (`/mobile`, P7): show an offer on the setup screen when unpaired (QR + text,
   plus the relay URL field of §6a); scan or paste an offer in the Pairing tab, with the
   confirmation dialog.
7. **E2E** (`/e2e`, P8): the eight rows of §1, in both directions, across desktop↔phone
   and phone↔phone.

## 8. Acceptance

- [ ] Every row of the table in §1 passes, including the five that fail today.
- [ ] A member cannot admit a device without confirming a named, fingerprinted dialog —
      asserted in the client's own tests, not only in the UI.
- [ ] An offer pairs exactly one device; a second accept of the same code is refused.
- [ ] An expired offer is refused, and the joiner is told why.
- [ ] A code shown by a build that predates `PairingCode` still pairs.
- [ ] The relay still cannot unwrap anything: the threat-model review of SPEC §2.3 is
      re-run over the new frames.
