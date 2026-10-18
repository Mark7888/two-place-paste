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

Each entry carries a **sealed header** — type, filename and size, encrypted under the group key —
so History can describe an entry without downloading it. Every header is exactly 297 bytes, so it
leaks nothing.

## Keys

- **Each device** generates a key pair on first launch. The private key never leaves the device.
- **Each group** has one symmetric key, shared by its devices and rotated on every revocation.
- **Each entry** is encrypted with an AEAD under a key derived from the group key, with a fresh
  random nonce. The group key itself is never used directly.
- The relay stores the group key only **wrapped** to each device's public key.

On the device, keys are kept in the **macOS login keychain** or a file sealed with **Windows
DPAPI** to your user account.

## Revocation

Revoking a device creates a new group key (a new *epoch*) and wraps it only to the remaining
devices. The revoked device cannot read anything written afterwards. Entries written before stay
readable to whoever already had them, until they expire — at most 24 hours.

## The desktop app's local page

The app's window is a web page served on `127.0.0.1`. Because any web page in your browser can try
to reach `127.0.0.1`, the app:

- listens on `127.0.0.1` only, never on the network,
- requires a random token, minted at each launch, on every request — the page takes it from the URL
  once, keeps it in memory and removes it from the address bar; it is never stored in a cookie or
  `localStorage`,
- checks the `Origin` header on every request, including the WebSocket.

## Reporting a problem

Please report security issues privately through
[GitHub security advisories](https://github.com/Mark7888/two-place-paste/security/advisories/new)
rather than in a public issue.

## The details

- [Specification](https://github.com/Mark7888/two-place-paste/blob/master/docs/internal/SPECS.md) — the full design, including the threat model.
- [Crypto profile](https://github.com/Mark7888/two-place-paste/blob/master/spec/crypto.md) — algorithms, formats and test vectors.
