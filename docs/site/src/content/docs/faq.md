---
title: FAQ
description: Short answers to common questions.
---

### Can I pin or keep an entry longer than 24 hours?

No, and there will not be a way to. Every entry lives exactly 24 hours. The relay's cleanup deletes
whole hours of storage at once without looking anything up, and that only works because no entry's
lifetime can change.

### Can the server operator read my clipboard?

No. The relay only ever has ciphertext and a wrapped copy of your group's key that it cannot
unwrap. See [Security](/two-place-paste/security/).

### Do I have to run my own server?

Someone has to run a relay, but it does not have to be you — a friend's relay works just as well,
and they still cannot read your entries. They will be able to see how many devices you have, when
you sync and how big your entries are.

### Can one relay serve several people?

Yes. Make a creation token per person. Each person's devices form their own group with their own
key, and the per-group storage limit stops one group from crowding out the others.

### Can a device be in two groups?

No, one at a time. Use **Settings → Leave the group** and then join the other one.

### What happens if I lose a device?

Revoke it from another device in the [Devices](/two-place-paste/guides/devices/) tab. It loses
access to everything written afterwards.

### What happens if I lose all my devices?

The group cannot be recovered — no one but your devices ever had its key. Ask for a new creation
link and start a new group.

### Is there a Linux app? An iPhone app?

Not yet. The Android app is in development; Linux desktop and iOS are deferred. See
[Linux](/two-place-paste/install/linux/) and [Android](/two-place-paste/install/android/).

### How big can an entry be?

50 MB of ciphertext by default (`TPP_ENTRY_MAX_BYTES`). The desktop app's automatic syncing stops
at 25 MB unless you change it; manual **Upload** and **Download** are not limited.

### Is TwoPlacePaste open source? Under which license?

Yes. The relay server is licensed under the
[GNU Affero General Public License v3.0 or later](https://github.com/Mark7888/two-place-paste/blob/master/server/LICENSE),
and everything else — the desktop and Android apps, the client library, the wire schema and these
docs — under the
[Mozilla Public License 2.0](https://github.com/Mark7888/two-place-paste/blob/master/LICENSE).
The TwoPlacePaste name and logo are not covered by either license.
[`LICENSING.md`](https://github.com/Mark7888/two-place-paste/blob/master/LICENSING.md) has the
details. If you run your own relay, see
[License](/two-place-paste/self-hosting/overview/#license) for what that means for you.

The open-source libraries each app is built from are listed, with their licenses, under
**Settings → About → Open-source licenses** in the desktop and Android apps, and in
`/usr/share/doc/tpp/` in the relay's Docker image.
