---
title: How it works
description: The relay, groups, devices and keys, in plain language.
---

```
  ┌──────────────┐        ciphertext         ┌─────────────┐        ciphertext        ┌──────────────┐
  │ Work laptop  │ ───────── wss:// ───────▶ │    Relay    │ ───────── wss:// ──────▶ │ Home desktop │
  │ (encrypts)   │ ◀──────────────────────── │ Redis+blobs │ ◀─────────────────────── │ (decrypts)   │
  └──────────────┘                           └─────────────┘                          └──────────────┘
```

**The relay** is a small Go server with a Redis database. Devices keep a WebSocket open to it. It
stores each clipboard entry — encrypted — for 24 hours and tells the other devices when a new one
arrives. Small entries live in Redis; large ones in a blob store (a disk volume or S3).

**A group** is a set of devices that share one symmetric key. You create a group once, with a
single-use link from the relay's admin page, and then add devices to it.

**A device** is one install of the app. On first launch it generates its own key pair. The private
half never leaves the device: it is kept in the macOS keychain, or in a file sealed with Windows
DPAPI.

**Adding a device** means an existing member wraps (encrypts) the group key to the new device's
public key. The relay passes the wrapped key along; it never has the key itself.

**Revoking a device** generates a new group key — a new *epoch* — and wraps it for the devices that
stay. The revoked device cannot read anything written afterwards.

**An entry** is one clipboard item: text, an image or a file, up to 50 MB. It is encrypted with a
fresh key derived from the group key. Its type, filename and size travel in a small sealed header so
that History can describe an entry without downloading it. The relay sees only that the entry
exists, how big the ciphertext is, and when it was written.

Every entry expires 24 hours after it was written. There is no way to pin or extend one — that is
deliberate, and the storage cleanup relies on it.

For the full threat model, see [Security](/two-place-paste/security/).
