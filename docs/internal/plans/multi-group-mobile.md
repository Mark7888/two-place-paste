# Plan — Several groups on the Android app

**Status:** in progress. The storage rules are in `/spec/crypto.md` §10.1 and
the behaviour in SPECS §3.5; the desktop app does the same thing
(`multi-group-desktop.md`), and this plan only says where Android differs.

---

## 1. Goals and non-goals

The goals are the desktop's (`multi-group-desktop.md` §2): keep several
groups, one in use, switch from a **Groups** tab, behind a setting that is off
by default; turning it off keeps every group; a downgrade opens the group last
in use.

Non-goals, beyond the desktop's:

- The quick-settings tile has no group picker. It syncs with the group in use,
  as now.
- The demo never takes part. Its client is a separate in-memory one, and a
  device is only offered the demo before it has been in any group, so it has
  nothing set aside.

## 2. Decisions

1. **The same records as the desktop, in the same places.** The group in use
   stays in the `session` secure-store entry. Groups set aside are one more
   entry, `parked-groups`, holding `{ "version": 1, "groups": [...] }`, each
   element the stored form `session` already uses (`StoredState`: base64url
   keys, the epoch as a decimal string). Android and desktop never read each
   other's store, so this is for consistency, not compatibility.
2. **The same ref.** A group is named to the screens by `groupRef`: the first
   16 hex characters of SHA-256 over `"tpp/v1/group-ref" || group_id`. The
   tests pin it against values the Go core produces.
3. **The swap happens inside the client.** `Client` keeps its identity in
   memory and the store, and `forget` already resets it in place without a
   new client. `switchGroup`, `parkActive` and `removeParked` follow `forget`:
   disconnect, drop what belonged to the old identity (invitations, the recent
   entry, the entry cache, store keys, limits), install the new state, write
   it. The session store keeps the same `Client` object, so the tile handler
   and every screen keep working with no rewiring, and the app connects again
   if it is in the foreground.
4. **The write order and the start-up clean-up are the desktop's.** Park the
   outgoing group, write `session`, un-park the incoming one; on open, drop a
   parked copy of the group in use. A record with an unknown version is left
   alone and nothing that writes the list runs.
5. **Only a deleted group is discarded.** The Go client knows for certain when
   it was revoked. This client only knows it *probably* was (SPEC §3.3 step 5:
   the relay is up and refuses the socket), so a probably-revoked group is set
   aside like any other and the user can remove it. A group the relay said was
   deleted (410) is discarded.
6. **The setting is a secure-store entry, `multiGroup`.** The app has no
   other settings store, and the demo gate already keeps its flag there the
   same way. It holds `"1"` or nothing.
7. **The setup screen offers the groups set aside.** On Android the setup
   screen *is* the app while the device is in no group: there is no tab bar.
   After **Add a group**, or after leaving the group in use, the setup screen
   lists the groups set aside with **Switch**, so the user is never stuck on
   it with groups they cannot reach.
8. **Seven tabs.** Groups goes between Devices and Pairing. The bar's tabs are
   columns that share the width, so a seventh fits a 360 dp screen at about
   51 dp each, still above the 48 dp target.

## 3. Core (`src/core/protocol`)

- `state.ts`: `toStored` / `fromStored` split out of `encodeState` /
  `decodeState`, so the parked list reuses them.
- `groups.ts`: `PARKED_STATE_NAME`, `groupRef`, `ParkedGroup`, and the encode
  and decode of the parked record.
- `client.ts`: `activeRef`, `parkedGroups()`, `parkActive()`,
  `switchGroup(ref)`, `removeParked(ref)`, the parked list loaded in `open`,
  and one reset shared with `forget`. A promise chain serialises the four
  operations and `forget`.

## 4. App (`src/app`)

- `sessionStore.ts`: `multiGroup` in the snapshot, with `parkedGroups` and
  `activeRef`; `setMultiGroup`, `switchGroup`, `addGroup`, `removeGroup`, each
  refused while the setting is off or the demo runs. A switch or an add resets
  the screens, as leaving does.
- `GroupsScreen.tsx`: the group in use, the groups set aside with **Switch**
  and **Remove**, and **Add a group**, with an `Alert` before Remove and Add.
- `SettingsScreen.tsx`: a **Keep several groups** switch in the Group section;
  the leave text says, with it on, that only the group in use is left.
- `SetupScreen.tsx`: decision 7.
- `App.tsx`: the Groups tab, only with the setting on and not in the demo.

## 5. Tests

- Core, against the in-process relay: add a group, switch back and forth, sync
  in each; a rekey while a group is set aside is caught up on switching back;
  restart with both; the interrupted-switch clean-up; an unreadable record is
  left alone; a deleted group is discarded; `forget` keeps the parked groups;
  `groupRef` matches the Go values.
- Lint, typecheck, Jest.

## 6. Steps

1. This plan.
2. Core: parked groups and the switch, with tests.
3. App: the session store, the screens, the setting.
4. Docs: the Android part of the *Several groups* guide, SPECS §3.5.
