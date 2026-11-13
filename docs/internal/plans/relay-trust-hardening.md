# Plan — keep a hostile relay out of pairing and rekeys

**Status:** proposed.
**Raised by:** a security review of what a modified relay can do. The threat model
(SPEC §2.1) says the design does not require trusting the server for confidentiality;
today four paths break that.
**Touches:** Phase 1 — `/pkg/tppclient/**`, `/mobile/**`, `/desktop/**`, one rule in
`/server/internal/ws`, `docs/site`. Phase 2 — `/spec/**`, `/proto/**`, `/server/**`,
`/pkg/tppclient/**`, `/desktop/**`, `/mobile/**`, `docs/site`.

---

## 1. The holes

Each of these needs nothing from the user: a relay running modified code can do it while
the clients behave exactly as written.

| # | Hole | Where | What the relay gets |
|---|---|---|---|
| H1 | An inviter wraps the group key to whatever public key a `PairingJoinNotice` carries. It does not check that the token belongs to an invitation it opened, asks nobody, and never uses the inviter ephemeral key the QR code carries. | `pkg/tppclient/pairing.go` `completePairing`; `mobile/src/core/protocol/client.ts` `completePairing` | The current group key, at any moment any member is connected. It can then leave its device out of `DeviceListResponse`. |
| H2 | A device installs any wrapped key for a higher epoch that the relay pushes. Nothing ties the new key to the old one or to a member. | `pkg/tppclient/client.go` `installWrappedKey`; the same path in `client.ts` | Every device switched onto a key the relay chose. Everything written after that is readable by the relay. |
| H3 | A joining device cannot tell who wrapped the key it receives, in either pairing direction. Its public key is public, so the relay can wrap a key of its own to it. | `JoinPairing`, `Offer.Wait` and their TypeScript twins | The new device placed in a group the relay controls, possibly relaying between it and the real group. |
| H4 | A rekey wraps to the public keys in the roster the relay returns, and the confirmation shows names only. | `group.go` `Revoke`/`Confirm`; `DevicesPanel.tsx`, `DevicesScreen.tsx` | The new epoch's key, by swapping a real device's public key for its own under the same name, or by adding a row with a plausible name. `spec/crypto.md` §11.4 lists this, but nothing in the UI lets the user catch it. |

What is already sound, and stays as it is: group creation (the key is made on the device
and wrapped only to itself), the wrap construction (recipient and epoch bound,
`spec/crypto.md` §4.2), entry encryption, and the member's side of joiner-emitted
pairing (the key comes from the scanned code and the user confirms a fingerprint).

## 2. Approach

Two phases, because they cost very different amounts.

- **Phase 1** is client-side and ships in an ordinary release, with no wire change. It
  closes H1 and H2 against a relay acting alone, and makes H4 catchable by a person.
- **Phase 2** is a crypto and wire change. It closes H3, makes H4 a cryptographic check
  instead of a visual one, and closes what Phase 1 leaves: a relay working with a
  revoked device.

Phase 1 is worth shipping on its own and first: it removes the silent, zero-interaction
attacks within a single release.

**No fallback, in either phase.** A client never takes a weaker path because of
something the relay says: its version, the features it claims, or what the other device
appears to support. A relay controls all of those, so a hostile one would simply claim
whatever unlocks the weaker path. Where the hardened path cannot run — an old relay, an
old app on the other side — the operation fails and the user is told what to update. A
version check on the relay is therefore never part of the trust path; at most it makes a
failure easier to explain.

## 3. Phase 1 — client-side hardening

### 3.1 Confirm every join (H1)

The member-emitted flow gets the same gate joiner-emitted pairing already has
(`docs/internal/plans/joiner-emitted-pairing.md` §5).

- A `PairingJoinNotice` is acted on only if its token is an invitation **this** client
  started, still open and not yet used. Anything else is dropped and logged by device id.
- The notice no longer triggers a wrap. It produces a pending join — the device's name
  and the fingerprint of its public key — and the wrap happens only in `Confirm`, the
  same split as `PrepareAcceptOffer` → `Confirm`, so the type system enforces it. The
  TypeScript client mirrors it.
- The joining device shows its own fingerprint while it waits (desktop `PairingPanel`,
  mobile `SetupScreen` join view — the offer view already does this).
- Desktop and mobile show the "Device X wants to join — check the fingerprint" dialog on
  the inviter, with Add and Reject.

Reject leaves a device record on the relay with no wrapped key: the relay adds the device
at join time (`handlePairingJoin`) and nothing removes one that never got a key. It is
harmless — it holds no key — but it shows in Devices. Phase 1 marks such rows
("never received a key"); a relay-side cleanup or a reject message belongs to Phase 2,
because it is a wire change.

### 3.2 Check a new epoch's key before installing it (H2)

A new epoch is only accepted if it chains back to the key this device already holds.
The store link (`spec/crypto.md` §7a.2) is exactly that proof: `link[E]` opens with the
store key of epoch `E` and must yield the store key of `E-1`.

- Before installing a key for epoch `E > current`, the client fetches the store links
  (`StoreLinksRequest`, already on the wire), opens `link[E]`, `link[E-1]`, … down to
  `current + 1`, and checks the last one yields its own current store key. On any
  failure, or a missing link, it refuses the key and tells the user the relay offered
  a key it could not verify.
- Server: require `store_link` on **every** rekey. Today it is required only while the
  group holds store items (SPEC §3.3). Every client since the store already sends one,
  so this refuses only pre-store clients, which are already told to update.
- `spec/crypto.md` §7 gains the rule; SPEC §3.3 gains the server requirement. No byte
  layout changes, so this is a revision note, not a new profile.

Two cases have no link to check, and both are refused rather than waved through, because
a hostile relay can fake either one:

- **A relay that does not hand the links over.** One older than v1.2.0 has no
  `StoreLinksRequest`, and v1.2 refuses it with `STORE_DISABLED` while the group's store
  is off — which is the default. The relay is changed to serve links whatever the store
  switch says; against an older one the client refuses every new epoch and says the relay
  needs updating. Everything else keeps working.
- **An epoch made by a client older than v1.2.0** carries no link. Once the relay
  requires one, such a rekey cannot happen; on a relay that does not yet require one, a
  new device refuses that epoch. Its way back is to leave and pair again, which loses
  nothing: it starts empty either way, and it reads the store again from the new epoch.

What it does not stop: a revoked device holds the old key, so a relay working with one
can still forge a link. Phase 2 closes that.

### 3.3 Show fingerprints (H4)

- Devices list (desktop `DevicesPanel`, mobile `DevicesScreen`): each device's
  fingerprint, and this device's own in Settings → About, so the two can be compared.
- The revoke confirmation lists every device that will get the new key, with its
  fingerprint, and tells the user to check them against each device's own screen.

### 3.4 After upgrading

Docs tell users, once every device runs the new version: open Devices, compare the
fingerprints, revoke anything unknown. A relay that already took a key under H1 keeps it
until the next rekey. Phase 1 has no rekey without a revocation; Phase 2 adds one (§4.5).

### 3.5 Phase 1 compatibility

"Old" is a released client or relay without Phase 1. v1.2.0 is the first release whose
relay serves store links and whose clients send one with every rekey.

| Pairing | Result |
|---|---|
| New member invites, old device joins | Works. The member gets the confirmation; an old joiner does not show its fingerprint on the join screen, so the user compares the name only — or opens that device's "show a code" view, which shows the same key's fingerprint. |
| Old member invites, new device joins | Works, unprotected: the old member still wraps without asking. |
| Either direction with an offer | Unchanged; it already confirms. |

| Rekey | Result |
|---|---|
| Rekey by a v1.2+ client, new devices receive it | Works and is checked. |
| Rekey by any client, old devices receive it | Works, unchecked, as today. |
| Rekey by a pre-1.2 client, new relay | Refused with "update this app", as pre-store clients are already told when the store holds items. |
| Rekey by a pre-1.2 client, v1.2 relay | Goes through without a link; new devices refuse that epoch and have to pair again. |
| New clients on a relay without Phase 1 | Pairing, sync and history work. Every new epoch is refused until the relay is updated — on v1.2 only while the group's store is off, which is the default. |

Fingerprints in Devices and in the revoke dialog need both devices updated, since an old
client cannot show them. The Go and TypeScript `Fingerprint` already agree, because the
offer flow compares them across clients.

### 3.6 Phase 1 work, in landing order

Two PRs: the contract change on its own (CLAUDE.md, "Shared contracts"), then everything
else together. Landing the server, both clients and both apps in one PR means `master`
never holds a client whose library asks for a confirmation its app does not show yet.

1. **Spec** — `spec/crypto.md` §7 (a new epoch must chain back through its store link,
   with a revision note), SPEC §3.2 (every join is confirmed) and §3.3 (every rekey carries
   a link). No byte layout or vector changes.
2. **Implementation** — after 1 is merged:
   - Server: `store_link` required on every rekey, with a test of the refusal; store
     links served whether or not the group's store is on.
   - Go and TypeScript clients: `completePairing` split into notice → pending join →
     `Confirm`; invitation matching; epoch verification against store links; fingerprints
     on roster devices. Tests: a notice for an unknown, expired or used token wraps
     nothing; a key for a new epoch with a missing, wrong or forged link is refused; a
     real rekey across one and across two epochs is accepted; the interop test pairs in
     both directions through the new confirmation.
   - Desktop and Android: the join dialog, the joiner's own fingerprint, fingerprints in
     Devices, the revoke dialog and Settings, the refused-key message.
   - Docs site: pairing and devices pages, upgrade notes, a short section on what the
     relay can and cannot do; desktop screenshots retaken.

A release follows 2.

## 4. Phase 2 — authenticated pairing and membership

### 4.1 Crypto additions (`spec/crypto.md` 1.6, additions only)

Following the 1.3 precedent: new strings name new purposes and every container carries
its own version byte, so this stays `tpp-crypto-v1` and every existing vector stays
byte-identical.

- **Authenticated wrap, container version `0x02`.** The RFC 9180 auth-mode shape: the
  wrap key also mixes in a DH between the **sender's device key** and the recipient.
  ```
  shared_e  = X25519(ephemeral_private, recipient_public)
  shared_s  = X25519(sender_private,    recipient_public)
  wrap_info = "tpp/v1/wrap-auth" || ephemeral_public || recipient_public || sender_public
  wrap_key  = HKDF-SHA256(shared_e || shared_s, <empty>, wrap_info, 32)
  ```
  Still 81 bytes. Only someone holding `sender_private` can make a wrap that opens
  against `sender_public`, so a recipient that learned `sender_public` from a trusted
  place knows who sent the key.
- **Pairing secret.** 16 random bytes generated by the device that shows the code, carried
  in the code and never sent to the relay. The code already travels out of band (QR, or
  the `#fragment` of a `/pair` link — SPEC §6a), which is the channel the relay cannot see.
  - Invite: the joiner sends `join_proof = HMAC(HKDF(secret, "tpp/v1/pair-join"), token || joiner_public || name)`.
  - Offer: the member sends `accept_proof = HMAC(HKDF(secret, "tpp/v1/pair-accept"), offer_code || member_public || wrapped_key)`.
- **Device certificates.** Proof that a member admitted a device, which the relay cannot
  make without the group key:
  ```
  cert_key[E] = HKDF-SHA256(group_key[E], <empty>, "tpp/v1/device-cert" || u64be(E), 32)
  cert        = HMAC-SHA256(cert_key[E], u64be(E) || device_id || device_public || name)
  ```
- Vectors for all three, and failure vectors (wrong sender, wrong secret, wrong epoch).

### 4.2 Wire additions (`/proto`, its own PR)

| Message | Added |
|---|---|
| `PairingPayload` | `inviter_device_public_key`, `pair_secret`. Field 3 (the unused ephemeral key) is kept and ignored. |
| `PairingJoinRequest`, `PairingJoinNotice` | `join_proof` |
| `PairingOffer` | `offer_secret` |
| `PairingOfferAcceptRequest`, `PairingComplete` | `sender_device_public_key`, `accept_proof` |
| `PairingWrappedKeyUpload`, `PairingOfferAcceptRequest`, `CreateGroupRequest` | `device_cert` for the device being admitted |
| `Device` (roster row) | `cert` |
| `RekeyRequest` | `device_certs` for every remaining device at the new epoch; `revoked_device_id` may be empty (§4.5) |
| `WrappedKeyAvailable` | `sender_device_id` |
| new `PairingJoinReject` | the inviter turns a join down; the relay deletes the keyless device |
| `Device` (roster row) | `protocol_level`: the level the device last connected with, which the relay already stores (SPEC §5.3) |
| `Capability` | `CAPABILITY_PAIRING_V2 = 1`, advertised in `HelloResponse` by a relay that speaks this phase |

The relay stores certs and proofs as opaque bytes and hands them back. It enforces
nothing about them — its checks are not part of the trust path.

Phase 2 is **protocol level 2** (`docs/internal/plans/protocol-compatibility.md`, SPEC
§5.3): it is the "pairing change at level 2" that plan was written for. The relay's
`ProtocolMax` becomes 2 and Phase 2 apps send `proto=2`. §4.4 says what each side does
with that.

### 4.3 Client rules

- **Inviter:** wraps only when `join_proof` verifies, using the authenticated wrap. The
  fingerprint dialog of §3.1 stays as a second check, and because the proof already
  rules out the relay, it can be shortened to the name.
- **Joiner (invite):** opens the wrap against `inviter_device_public_key` from the code.
- **Joiner (offer):** checks `accept_proof` against its own secret, which proves
  `sender_device_public_key`, then opens the wrap against it.
- **Roster:** every row's cert is checked under the current epoch's key. A row without
  a valid cert is shown as "not added by any of your devices" with a warning, and is
  **never** wrapped to.
- **Rekey:** wraps only to verified rows, with the authenticated wrap, and issues fresh
  certs at the new epoch (the old ones were made with a key the revoked device holds).
- **New epoch:** the sender must be a verified device at the old epoch, the wrap must
  open against its key, the link chain of §3.2 must hold, and the new roster's certs must
  name the sender. Each device then shows "Re-keyed by *Laptop*, removing *Old phone*",
  so a key pushed by a revoked device working with the relay shows up as the wrong
  device doing the removing.
- **No weaker path:** a Phase 2 client never produces or accepts a version-`0x01` wrap, a
  join without a proof, an accept without a proof, or a key for a device without a valid
  cert — in any group, verified or not, whatever the relay reports. There is no per-group
  flag to downgrade and nothing for a relay to switch off, so there is no "pretend to be
  old" attack.

### 4.4 Rollout and compatibility

A wire change lands in the server and both clients together (CLAUDE.md, "Shared
contracts"). Every addition is a new protobuf field or message, so nothing crashes on
contact. What decides who may do what is the protocol level of SPEC §5.3: level 1 is
every app before this phase, level 2 is a Phase 2 app.

**The relay, at level 2**
- `ProtocolMax` is 2 and `HelloResponse` advertises `CAPABILITY_PAIRING_V2`.
- `ProtocolMin` stays 1 at first, so an old device already in a group keeps
  connecting and syncing.
- From a level-1 socket it refuses, with `ERROR_CODE_CLIENT_OUTDATED`, every request
  that would admit a device or change the key: `PairingStartRequest`,
  `PairingJoinRequest`, `PairingWrappedKeyUpload`, `PairingOfferRequest`,
  `PairingOfferAcceptRequest` and `RekeyRequest`. Protocol-compatibility §4 refuses only
  the pairing requests; rekeys join them here because a level-1 rekey would wrap
  version-`0x01` keys and no certs, which Phase 2 devices refuse (§4.3), stranding
  them.

**A Phase 2 app**
- Sends `proto=2` and `HelloRequest` on every socket, as SPEC §5.3 already has it do.
- Pairs and rekeys only when the relay advertises `CAPABILITY_PAIRING_V2`. Without it —
  an old relay, which answers `HelloRequest` as unsupported, or any relay that leaves
  the capability out — both are refused on the device with *the relay needs updating
  to add or remove devices*, naming the operator's website when there is one (SPEC
  §5.3). Sync, history and the store keep working.
- Never falls back (§2). The capability check only makes the failure early and
  readable: a relay that advertises the capability and then misbehaves still fails
  every proof and cert check of §4.3, and a relay that hides it gains nothing but a
  refusal.

| Situation | Result |
|---|---|
| Phase 2 relay, Phase 2 apps | Works, fully checked. |
| Old relay, or one that claims to be | No capability: pairing and rekeys are refused on the device, with the message above. Everything else keeps working. |
| Old app pairing or rekeying through a Phase 2 relay | The relay answers `CLIENT_OUTDATED`; the app says to update (apps since protocol levels shipped show *the relay needs a newer version of this app*). |
| Phase 2 app and an old app pairing, either direction, through an old relay | Refused by the Phase 2 side: no capability, and the old app sends no proof anyway. |
| Old app already in the group | Keeps syncing at the current epoch. The next rekey, by a Phase 2 device, wraps a key it cannot open, so it falls behind. Before that rekey the app lists the devices whose `protocol_level` is 1 and warns that they will stop syncing until they are updated and paired again. The level is what the relay reports: a courtesy for the warning, never a check. |
| New code read by an old app | The old app cannot read the new kind of code (protocol-compatibility §3.6) and says to update; one that predates that says the code is invalid. |

**Later: raising the minimum.** A Stable release after Phase 2 raises `ProtocolMin` to
2. From then on a level-1 app is refused at the connection with 426 and the body of
SPEC §5.3, and apps since protocol levels shipped say *update this app* instead of
falling behind silently. Apps from before levels misreport the 426 (protocol-
compatibility §3.4), which is why this waits for a release, not the Phase 2 one.

None of the relay's refusals protect against a hostile relay, which can simply not make
them. They keep an honest deployment consistent and tell people what to update. The
protection is §4.3: the apps have no weaker path to fall back to.

### 4.5 Bringing an existing group in

A group made before Phase 2 has no certs, and — under H1 — the relay may already hold its
key. Certifying the current roster as-is would accept whatever the relay added, and the
rule of §4.3 forbids wrapping to a device without a cert. So the first Phase 2 rekey of
such a group is also the step that certifies it:

1. On one device, **Devices → Verify devices** (or any revocation) lists every device
   with its fingerprint. Devices without a cert are marked as not yet verified.
2. The user compares each one with that device's own screen and ticks it; unticked rows
   are removed.
3. The client rekeys — with no revocation if none was asked for (empty
   `revoked_device_id`; the relay refuses that today) — with authenticated wraps and
   certs for every ticked device only.

Pairing needs no such step: a device paired by a Phase 2 app gets its cert at once. Until
the group has been through step 3, the Devices screen shows the uncertified devices with
a "not verified" banner, and any key the relay took before is still good; after it, that
key is useless.

### 4.6 Phase 2 work, in landing order

Two PRs again: both contracts together, then the implementation. Both apps switch to
the Phase 2 protocol in the same PR, so no build on `master` speaks only half of it.

1. **Spec and proto** — `spec/crypto.md` 1.6 (§4.1 here), the reference generator in
   `spec/vectors/gen` and the new vectors, SPEC §2.1 and §3.2–§3.3, the roster row of
   §11.4 changed from "mitigated at the UI level" to what this phase guarantees,
   SPEC §5.3 (level 2 and `CAPABILITY_PAIRING_V2`); the fields of §4.2, with
   `make proto` and both generated trees.
2. **Implementation** — after 1 is merged:
   - Server: store and return certs and proofs, rekey without a revocation,
     `PairingJoinReject`, `protocol_level` in the roster; `ProtocolMax` 2, the
     capability, and the level-1 refusals of §4.4. Tests against Redis, including a
     level-1 socket refused for each pairing request and `RekeyRequest`.
   - Go and TypeScript clients: `proto=2`; wrap v2, proofs and certs against the
     vectors; the rules of §4.3 with no version-0x01 path left in either client; the
     capability check of §4.4; tests that an old relay, a relay that leaves the
     capability out, a relay that advertises it and then forges, and an old peer each
     make pairing and rekeys fail rather than fall back; the interop test in both
     pairing directions and across a rekey.
   - Desktop and Android: the "not verified" banner, unverified rows, the rekey notice,
     the warning about level-1 devices before a rekey, the "relay needs updating"
     message, and Verify devices (§4.5).
   - Docs site for all of it.

A Stable release follows 2, with both apps. This PR is large (most of the estimate
in §6); it is reviewed in one go, commit by commit.

3. **Later Stable release** — `ProtocolMin` 2 (§4.4), once Phase 2 apps have been out
   long enough.

## 5. What stays out of scope

- **Withholding.** The relay can still drop entries, keys or pairing messages. It can
  stop the service; it cannot read it.
- **Forks.** A relay working with a revoked device can still give different devices
  different epochs. Phase 2 makes that visible (devices stop syncing, and the rekey notice
  names the wrong device) but does not prevent it.
- **A malicious client build.** All of this assumes the apps are the published ones;
  desktop updates are already signature-checked (`internal/update`).
- **A pairing code seen by someone else.** The code is the out-of-band secret; anyone who
  sees it before it is used can race the real device. Codes stay short-lived and
  single-use, as today.

## 6. Size

Estimates from the files involved and from the past plans of similar shape; lines include
tests.

| Phase | PRs | Rough size | Comparable to |
|---|---|---|---|
| 1 | 2 | ~1.5–2.5k lines, no wire or spec change beyond two rules | Smaller than joiner-emitted pairing |
| 2 | 2 | ~5–8k lines, a crypto spec revision, new vectors, a proto change across server and both clients | Joiner-emitted pairing plus most of the persistent store's client work |

Phase 1 is the urgent part: it closes the two holes a relay can use with no user action.
Phase 2 is the full fix, and it means every device must update before a group can be
verified.
