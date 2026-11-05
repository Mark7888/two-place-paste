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

- **A relay older than v1.2.0** has no `StoreLinksRequest`. The client refuses every new
  epoch on it and says the relay needs updating. Everything else keeps working.
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
| New clients on a pre-1.2 relay | Pairing, sync and history work; every new epoch is refused until the relay is updated. |

Fingerprints in Devices and in the revoke dialog need both devices updated, since an old
client cannot show them. The Go and TypeScript `Fingerprint` already agree, because the
offer flow compares them across clients.

### 3.6 Phase 1 work, in landing order

Every merge to `master` ships to the Beta channel, so each PR must leave `master` safe to
use on its own. That decides the order. Each client and its app land together: the
library's new confirmation step would otherwise break pairing in an app that does not
show the dialog yet. Docs site changes go in the PR that changes the behaviour.

1. **Spec** — `spec/crypto.md` §7 (a new epoch must chain back through its store link,
   with a revision note), SPEC §3.2 (every join is confirmed) and §3.3 (every rekey carries
   a link). No byte layout or vector changes. Its own PR, as a shared contract.
2. **Server** — `store_link` required on every rekey, with a test of the refusal and a
   line in the docs site's upgrade notes.
3. **Go client and desktop** — `completePairing` split into notice → pending join →
   `Confirm`; invitation matching; epoch verification against store links; fingerprints
   on roster devices. Desktop: the join dialog, the joiner's own fingerprint,
   fingerprints in Devices, the revoke dialog and Settings, the refused-key message.
   Docs: pairing and devices pages, desktop screenshots retaken. Tests: a notice for an
   unknown, expired or used token wraps nothing; a key for a new epoch with a missing,
   wrong or forged link is refused; a real rekey across one and across two epochs is
   accepted.
4. **TypeScript client and Android** — the same, with the same tests, plus the interop
   test pairing in both directions through the new confirmation. Docs: the Android parts
   of the same pages, and a short section on what the relay can and cannot do.

Steps 2–4 depend only on step 1 and can be written in parallel. A release follows step 4.

## 4. Phase 2 — authenticated pairing and membership

### 4.1 Crypto additions (`spec/crypto.md` 1.4, additions only)

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

The relay stores certs and proofs as opaque bytes and hands them back. It enforces
nothing about them — its checks are not part of the trust path.

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
- **No downgrade:** a client that has once seen its group verified records that in its
  own state and from then on refuses version-`0x01` wraps, joins without a proof, and
  rows without certs. The relay cannot switch it back.

### 4.4 Rollout and compatibility

A wire change lands in the server and both clients together (CLAUDE.md, "Shared
contracts"). Every addition is a new protobuf field or message, which older code skips,
so nothing breaks on contact. What changes is how much is checked, and it is decided
per group: until a group is upgraded (§4.5) every new client behaves as Phase 1 does.

**Before the group is upgraded**

| Situation | Result |
|---|---|
| New relay, old clients | Works as today. |
| Old relay, new clients | Works at Phase 1 level. An old relay drops the new fields, so there are no certs to check and the upgrade is not offered. |
| New member invites, old device joins | The joiner sends no proof. The member falls back to the Phase 1 confirmation and an unauthenticated wrap, which the old joiner can open. |
| Old member invites, new device joins | The member sends no authenticated wrap. The joiner accepts it, as it would today, and says the pairing could not be verified because the other device is out of date. It has no earlier state to defend, so this is no weaker than Phase 1. |
| New code read by an old client | Works: the secret and the inviter key are fields the old decoder skips. The QR code is slightly denser. |

**After the group is upgraded**

| Situation | Result |
|---|---|
| Old client in the group | Cannot open a `0x02` wrap, so it is stuck at the old epoch and told to update and pair again. To avoid stranding anyone, the upgrade step reads every device's last-seen client version from the relay and refuses until all are new. That check only keeps devices from being left behind; it is not trusted for security. |
| Old device joining | Refused: it cannot send a proof or open an authenticated wrap. It is told to update first. |
| Relay downgraded, or pretending to be old | The upgraded clients' downgrade rule (§4.3) still holds: they refuse unauthenticated wraps, unproven joins and rows without certs. The group stops admitting devices and rekeying until the relay is back, and the client says so. |

So upgrading a group is one-way, and it waits until every device and the relay run the
new version.

### 4.5 Upgrading an existing group

A group made before Phase 2 has no certs, and — under H1 — the relay may already hold its
key. Certifying the current roster as-is would accept whatever the relay added, so the
upgrade is a deliberate step:

1. On one device, **Devices → Verify devices** lists every device with its fingerprint.
2. The user compares each one with that device's own screen and ticks it; unticked rows
   are removed.
3. The client runs a **rekey without a revocation** (empty `revoked_device_id`; the relay
   refuses that today): new key, authenticated wraps, certs for every ticked device.

After that the group is verified on every device that picks up the new epoch, and any key
the relay took before is useless. Until then the Devices screen shows a "not verified"
banner.

### 4.6 Phase 2 work, in landing order

The same rule as §3.6: each PR leaves `master` safe. The one dangerous intermediate state
is a group upgraded while one app cannot yet open an authenticated wrap, so **Verify
devices** lands last, after both apps understand everything it produces.

1. **Spec** — `spec/crypto.md` 1.4 (§4.1 here), the reference generator in
   `spec/vectors/gen` and the new vectors, SPEC §2.1 and §3.2–§3.3, and the roster row of
   §11.4 changed from "mitigated at the UI level" to what this phase guarantees. Its own
   PR.
2. **Proto** — the fields of §4.2, plus the client version each device reports on
   connect; `make proto` and both generated trees. Its own PR.
3. **Server** — store and return certs and proofs, rekey without a revocation,
   `PairingJoinReject`, client version per device. Tests against Redis.
4. **Go client and desktop** — `tppcrypto` wrap v2, proofs and certs against the
   vectors; the rules of §4.3 and the downgrade flag in `State`; the "not verified"
   banner, unverified rows and the rekey notice. Docs for each.
5. **TypeScript client and Android** — the same, the same vectors; the interop test in
   both pairing directions and across a rekey. Docs for each.
6. **Verify devices** — the upgrade flow of §4.5 in both clients and both apps, and its
   docs page.

Steps 4 and 5 can be written in parallel once step 3 is merged. A release follows step 6.

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
| 1 | 5–6 | ~1.5–2.5k lines, no wire or spec change beyond two rules | Smaller than joiner-emitted pairing |
| 2 | 8 | ~5–8k lines, a crypto spec revision, new vectors, a proto change across server and both clients | Joiner-emitted pairing plus most of the persistent store's client work |

Phase 1 is the urgent part: it closes the two holes a relay can use with no user action.
Phase 2 is the full fix, and it means every device must update before a group can be
verified.
