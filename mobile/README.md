# TwoPlacePaste — Android app

The Android client (ROADMAP P7, SPEC §3.2, §5.2, §6, §7.1, §7.3). React Native
and TypeScript, structured so that iOS is files added rather than an app
restructured.

```
src/core/         crypto and protocol. No React, no React Native.
  crypto/         spec/crypto.md byte for byte, validated against /spec/vectors
  protocol/       the WebSocket client and one method per flow of SPEC §3, §6
src/platform/     every OS capability, behind per-OS files (SPEC §7.3)
src/app/          the five screens and the session that wires them together
src/interop/      the scripted test that pairs this client with the Go one
android/          the Android project: the tile service and two native modules
scripts/gopeer/   the Go half of the interop test (a standalone module)
```

`src/core` is the load-bearing boundary. It imports nothing from React Native,
which is what lets the same code run in three places — the app on Hermes, the
quick-settings tile's sync, and Node, where the vector tests and the interop
test exercise it against the real server.

## Working on it

```sh
npm ci
npm test        # the crypto vectors and the protocol client
npm run lint
npm run typecheck
npm run interop # pairs this client with the Go client against a real relay
```

`npm run interop` needs `go` and `redis-server` on the machine and skips with a
reason when they are missing. It builds `/server` and runs it, so it is the only
test here that exercises the actual relay.

To build the app you also need the Android SDK and a JDK:

```sh
cd android && gradle wrapper --gradle-version 9.4.1   # once: the JAR is not committed
./gradlew assembleDebug                               # needs Metro: npm start
./gradlew assembleDebug -PbundleInDebug=true          # standalone, starts on its own
```

The difference matters. React Native leaves the JavaScript bundle out of a
debug build — that is what fast refresh is — so a plain `assembleDebug` APK
shows "Unable to load script" unless `npm start` is running and reachable.
`-PbundleInDebug=true` empties React Native's `debuggableVariants`, so the APK
carries its own bundle and starts with no dev server. CI builds with it and
asserts the bundle is inside the APK, because that is the artefact a reviewer
installs to run the manual matrix below.

The Gradle wrapper JAR is deliberately absent — this repository contains no
binary blobs, and the same rule keeps the launcher icon and the tile icon as
vector drawables and the debug keystore as the one Gradle generates on the
machine that builds.

## The quick-settings tile

Android 10 blocked clipboard reads from apps that are not focused and are not
the default IME. That single rule shapes this client: there is no background
watcher, no accessibility service and no auto-sync, and there will not be one —
SPEC §7.1 says the feature is dropped rather than worked around.

The tile is what replaces it. Tapping it:

1. records the request and starts the activity (`SyncTileService.onClick`),
2. which foregrounds the app — the legal moment to read the clipboard,
3. and JavaScript then performs exactly one sync and calls `TppTile.report`,
4. which renders the outcome on the tile and shows a toast.

A tap reaches the app on one of two paths, because it can find it in either
state: a running app receives an event, and a cold-started one claims the
pending request during startup. The flag is claimed once, so a tap can never
produce two syncs and a launcher tap never produces one.

### Behaviour by Android version

| Version | What the tile does | Why |
|---|---|---|
| 12 (API 31) | `startActivityAndCollapse(Intent)` collapses the shade and starts the app; the sync runs with the app in the foreground. | The Intent overload is the only one that exists. |
| 13 (API 33) | Same. The system may show a "TwoPlacePaste pasted from…" notice on a clipboard read; that is the OS, not the app. | Android 13 added the paste notification. |
| 14+ (API 34) | `startActivityAndCollapse(PendingIntent)`; the Intent overload throws `UnsupportedOperationException` on 14 and is used only below it. | Behaviour change in Android 14 for tiles. |

On every version the app must be unlocked to reach the clipboard: from a locked
device the tap opens the shade's tile, the system asks for the lock screen, and
the sync runs once the device is unlocked. **This table is written from the
platform contracts, not from a device run — see "What could not be executed".**

## Sync direction

SPEC §6 says upload if the local clipboard is newer, otherwise download.
Android is the one platform in this system where that comparison is possible:
`ClipDescription.getTimestamp()` (API 26+) records when the clipboard was last
set, which neither macOS nor Windows exposes. `sync()` uses it, against the
newest entry's *metadata* rather than the entry — deciding a direction should
not cost a 10 MB download.

Where the platform will not say (it answers 0), the app does not guess: it says
so and leaves the choice to the two directional buttons, because guessing here
silently destroys whichever side it overwrites.

## The two states of this app

There is no third one, and the shell enforces it (`src/app/App.tsx`).

**Not in a group.** The setup screen is the whole app: no tab bar, because
every other screen needs a group key to do anything and a tab that leads
nowhere is worse than no tab. It offers the only two ways in (SPEC §3) — join
a group a member is inviting this device to, or create the first group from a
relay's creation link — and one scanner that takes either code and works out
which it is from the code itself (`classifyCode`).

**In a group.** The five tabs. Pairing only ever *shows* a code, because that
is the direction the payload travels: only a member can mint a pairing token
against the relay, so the member displays and the joiner scans or pastes. A
phone scans the QR; a desktop, which has no camera by design (SPEC §7.2),
takes the string under it.

Every transport is symmetric — QR or copied text, desktop↔phone or
phone↔phone — but the direction is not: an unpaired device cannot show a code
for a member to accept, because minting a pairing token needs an authenticated
connection. That is a wire-contract limit, not a client one. The plan for
making it symmetric is [`docs/plans/joiner-emitted-pairing.md`](../docs/plans/joiner-emitted-pairing.md).

A device holds one group key, so it belongs to one group. Moving it elsewhere
is **Settings → delete this device's keys**, which returns the app to the
setup screen with no restart — and is a local operation: the relay still lists
the device until another device revokes it, which is what re-keys the group
(SPEC §3.3).

## What this client refuses to do

- **It never pulls history.** The History tab lists nothing until the fetch
  button is pressed, and nothing lists it on connect or on a rekey. A client
  that listed it automatically would be making the user's clipboard history
  travel without being asked (SPEC §6).
- **A new device starts empty.** It cannot read what predates it and does not
  ask for it (SPEC §3.2).
- **It keeps exactly one (epoch, group key) pair.** An entry from an older
  epoch is skipped and reported as "nothing to copy", never decrypted with a
  retained key (spec/crypto.md §7).
- **A revocation is prepared, not performed.** `prepareRevoke` returns a plan
  carrying the roster the user must be shown; the only way to revoke anything
  is to confirm that plan (SPEC §3.3 step 2).
- **Nothing in `src/core` can touch the clipboard.** That is what structurally
  guarantees that a rekey never does (SPEC §3.3).

## Keys at rest

The device private key and the group key live in one blob in
Keystore-backed encrypted storage: `react-native-keychain` with
`STORAGE_TYPE.AES_GCM_NO_AUTH`, which encrypts under a key the Android Keystore
holds and this process can use but not extract. No biometric prompt is attached
— the tile's one-tap promise cannot survive a dialog, and the device lock is
the boundary this relies on. Neither key is ever rendered, logged or exported;
the Settings screen shows the public half only.

## What could not be executed

The third acceptance criterion — "tile sync works from a locked-then-unlocked
device and from another app; behaviour documented on Android 12/13/14" — needs
a phone or an emulator. This phase was built in a Linux container with no
Android SDK and no device, so the table above is the platform's documented
behaviour rather than an observed one, and the box stays unticked.

What a reviewer should run, against a P4 relay, on an Android 12, 13 and 14
device or emulator:

1. Pair the phone from a desktop's QR code, then from a pasted code. Both are
   the same string; both must work. Then check the reverse direction: pair a
   second device *from* the phone's Pairing tab.
2. On a fresh install, confirm the setup screen has no tab bar, and that
   scanning the creation link on the relay's admin page creates the group.
3. Add the **Paste sync** tile to the quick-settings panel. Copy text in another
   app, pull down the shade, tap the tile: the app should flash, a toast should
   report what happened, and the tile's subtitle should keep saying it.
4. Repeat with the screen locked: the tap should ask for the lock screen, and
   the sync should complete after unlocking.
5. Copy a screenshot and repeat, in both directions. An image arriving from a
   desktop is staged in the app's cache and put on the clipboard as a
   FileProvider URI.
6. Kill the app from recents and tap the tile: the cold-start path claims the
   pending request exactly once, so exactly one sync should happen.
7. Revoke the phone from the desktop while the phone is in the background. Bring
   it forward: it should report that the relay is refusing it.
8. Check the window's edges, in portrait and in landscape, with gesture
   navigation and with three-button navigation: the app is edge-to-edge, so its
   background should reach both bars while no title, button or tab label sits
   under the status bar, under the navigation bar, or under a display cutout.
