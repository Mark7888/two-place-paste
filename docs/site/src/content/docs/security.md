---
title: Security
description: What TwoPlacePaste protects, from whom, and how.
---

## In one paragraph

The relay is **trusted with metadata but not with content**. It relays and stores ciphertext,
enforces group membership and size limits, and knows how many devices there are and when they were
active. It cannot decrypt any clipboard entry, and the design does not depend on trusting it for
confidentiality.

## What the relay sees

| Sees | Never sees |
|---|---|
| Ciphertext and its size | Clipboard content |
| When entries were written | Content type or filename |
| Device ids, names and public keys | Plaintext size |
| Wrapped (encrypted) copies of the group key | The group key itself |
| Group membership | Device private keys |

Each entry carries a **sealed header** — type, filename, size and a hash of the content, encrypted
under the group key — so History can describe an entry without downloading it. Every header is
exactly 553 bytes (297 from apps and relays that predate the hash), so its length tells the relay
nothing about the entry.

## Keys

- **Each device** generates a key pair on first launch. The private key never leaves the device.
- **Each group** has one symmetric key, shared by its devices and rotated on every revocation.
- **Each entry** is encrypted with an AEAD under a key derived from the group key, with a fresh
  random nonce. The group key itself is never used directly.
- The relay stores the group key only **wrapped** to each device's public key.

On the device, keys are kept in the **macOS login keychain** or a file sealed with **Windows
DPAPI** to your user account.

## Adding a device

The device that holds the group key hands it over, wrapped to the new device's public key. That key
travels through the relay, so before anything is handed over the app shows the new device's
**fingerprint** and asks you to compare it with what the new device shows. A relay that put a key of
its own in its place would show a different fingerprint. The app also ignores a join for a code it
did not show, or one already used.

## Revocation

Revoking a device creates a new group key (a new *epoch*) and wraps it only to the remaining
devices. The revoked device cannot read anything written afterwards. Entries written before stay
readable to whoever already had them, until they expire — at most 24 hours.

The revoke dialog lists the devices that will get the new key, each with its fingerprint: the list
comes from the relay, and the fingerprints are how you check it.

Every remaining device checks the new key before using it. Each key change carries a *store link*
that only a holder of the previous key can make, and a device accepts a new key only if the links
lead back to the key it already has. So the relay cannot move your devices onto a key it chose.

## What a hostile relay can still do

The design assumes the relay may be run by someone you do not fully trust. Even so:

- It can **drop or withhold** entries and keys. It cannot read or forge them.
- A **newly joining device** cannot tell who handed it its first key. The device already in the
  group is protected by the fingerprint check; the new one relies on the pairing code reaching it
  directly.
- A **revoked device working with the relay** could forge a key change for the devices it was
  removed from. They would stop syncing with the device that removed it, which is how it shows.

## The desktop app's local page

The app's window is a web page served on `127.0.0.1`. Because any web page in your browser can try
to reach `127.0.0.1`, the app:

- listens on `127.0.0.1` only, never on the network,
- requires a random token, minted at each launch, on every request — the page takes it from the URL
  once, keeps it in memory and removes it from the address bar; it is never stored in a cookie or
  `localStorage`,
- checks the `Origin` header on every request, including the WebSocket.

## Passwords on the clipboard

With *Watch the clipboard* on, the desktop app does not upload a copy that its application marked as
private — a password manager's, most often — or one that matches a built-in detector or your own
filter. You can still send it by hand, after a confirmation. This is decided on your computer, and
the relay never learns about it. See [Sensitive content](/two-place-paste/guides/sensitive-content/).

## Reporting a problem

Please report security issues privately through
[GitHub security advisories](https://github.com/Mark7888/two-place-paste/security/advisories/new)
rather than in a public issue.

## The details

- [Specification](https://github.com/Mark7888/two-place-paste/blob/master/docs/internal/SPECS.md) — the full design, including the threat model.
- [Crypto profile](https://github.com/Mark7888/two-place-paste/blob/master/spec/crypto.md) — algorithms, formats and test vectors.
