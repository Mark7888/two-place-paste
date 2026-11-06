# Plan — Several groups on the desktop app

**Status:** in progress. The storage rules are in `/spec/crypto.md` §10.1;
this plan is how the client core and the desktop app follow them. The Android
app has its own plan, `multi-group-mobile.md`.

---

## 1. Why

A device belongs to one group. Someone with two circles to share a clipboard
with — work and home, say — has to leave one group and pair again to reach the
other, and leaving deletes the keys, so pairing again means finding a device
from that group at hand.

This lets the desktop app keep any number of groups and switch between them,
with one group active at a time.

## 2. Goals and non-goals

Goals:

- A user can keep several groups, see them listed, switch to one, add a new
  one and remove one, from a **Groups** tab.
- The feature is **off by default**, behind a setting. With it off, the app is
  the app it was: no Groups tab, nothing new in any other screen.
- Turning it off keeps the active group active and keeps the others stored; the
  tab and its actions go away, and come back with the setting.
- A switch is a clean stop of one group and a clean start of the other: nothing
  of the old group reaches the new one, so there is no cross-group logic
  anywhere else in the app.
- A downgrade to a build without the feature opens the last active group.

Non-goals:

- Two groups live at once. One socket, one group key in use (§3, decision 1).
- A group picker in the tray. Switching is rare enough to belong in the app;
  the tray keeps showing the active group's name, as it does now.
- Moving clipboard history, store items or devices between groups.
- Anything on the relay or the wire. A parked group is, to its group, a device
  that is offline.

## 3. Decisions

1. **One active group; the others are parked.** Switching stops the client the
   way leaving does — supervisor cancelled, socket closed, background work
   waited for — swaps the state underneath it and connects again. The service
   resets what it holds about a group, as `Forget` already does, and the UI
   reloads every panel. This is what makes the switch "the app closed, the app
   started with the other group" without a process restart.
2. **The active group stays under `session`.** It is the record a build
   without this feature reads (`DefaultStateName`), in the same format, so a
   downgrade carries on with it (`/spec/crypto.md` §10.1).
3. **Parked groups are one more keystore secret, `parked-groups`.** One record
   holding a list, not one record per group: the keystore cannot list its
   items, and on macOS each item is a keychain entry the OS may ask the user
   to allow access to after a signed update, so one item means one prompt
   however many groups there are. A record is about 300 bytes per group, far
   below anything the backends limit.
4. **The order of writes makes an interruption harmless.** A switch writes the
   parked list with the outgoing group added (the incoming one still in it),
   then `session` with the incoming group, then the parked list without it. A
   crash between any two leaves a group in both places, never in neither. On
   start the client drops a parked record whose group id is the active one's,
   and keeps the active record.
5. **Each group has its own device keypair.** Adding a group parks the active
   one and starts from a fresh state with a new keypair, exactly as leaving
   does, then goes through the ordinary create or pair flow. A keypair is never
   reused across groups (`/spec/crypto.md` §10.1).
6. **A dead group is not parked.** A group this device was revoked from, or
   that the admin deleted, can never be used again. Adding a group or
   switching away from one in that state discards it, as leaving would, and
   the UI says so before the user confirms.
7. **The UI never sees a group id.** Group and device ids are bearer
   credentials on the wire. The UI addresses a group by a `ref`: the first 16
   hex characters of SHA-256 over `"tpp/v1/group-ref" || group_id`. It is
   stable, unique enough for a handful of groups, and says nothing about the
   group to anyone without the id.
8. **The device name is the installation's.** A switch keeps the name the app
   is using now rather than the one stored with the parked group, so renaming
   the device applies to whichever group is active.
9. **Leaving applies to the active group only.** It deletes that group's keys,
   as now, and leaves the parked ones alone; the device is then in no group
   and the Groups tab offers the parked ones. Removing a parked group is the
   same operation on a parked record: local, with the same warning that the
   relay still lists the device.
10. **The setting gates the actions, not the data.** With it off the service
    refuses the group endpoints (409) and the status says the tab is hidden;
    the parked list is not touched. Turning it off is never a reason to delete
    keys.

## 4. Storage

`parked-groups` holds JSON, written through the keystore like `session`:

```json
{ "version": 1, "groups": [ <State>, <State>, … ] }
```

Each element is a `tppclient.State`, the very struct `session` holds, so a
parked group can be put back under `session` byte for byte. `version` lets a
later build change the shape; a reader that finds a version it does not know
leaves the record alone and reports no parked groups, rather than overwriting
groups it cannot read.

No group id or device id is ever logged. Logs name a group by its `ref`.

## 5. Client core (`pkg/tppclient`)

A new file, `groups.go`:

| API | What it does |
|---|---|
| `DefaultParkedName` | `"parked-groups"`. |
| `Options.ParkedName` | The keystore entry to use; defaults to the above. |
| `type ParkedGroup` | `Ref`, `GroupName`, `ServerURL` — what a UI shows. |
| `GroupRef(groupID) string` | The ref of decision 7. |
| `(*Client) ActiveRef() string` | The active group's ref, or `""` when in no group. |
| `(*Client) ParkedGroups() []ParkedGroup` | The parked groups, in the order they were parked. |
| `(*Client) ParkActive() error` | Parks the active group (or discards a dead one) and leaves the client in no group with a fresh keypair, ready to create or pair. |
| `(*Client) SwitchGroup(ref) error` | Decision 1 and 4: stops, parks the active group, installs the parked one, persists. The caller connects. `ErrUnknownGroup` for a ref it does not hold. |
| `(*Client) RemoveParked(ref) error` | Deletes one parked group's record. |

`New` loads the parked list and applies the clean-up of decision 4. The stop and
reset that `Forget` does move into one helper that `Forget`, `ParkActive` and
`SwitchGroup` share, and a mutex serialises the four so two switches cannot
interleave their writes.

Without a keystore the parked list lives in memory, as `State` does.

## 6. Desktop service

- `config.Settings.MultiGroup` (`multi_group`, default false), with
  `SettingsView.multi_group` and `SettingsPatch.multi_group`.
- `Status.multi_group` mirrors the setting, so the shell can show or hide the
  tab without a second request.
- `Relay` gains the four core calls above.
- `Service` gains:
  - `Groups(ctx)` — the active group and the parked ones;
  - `SwitchGroup(ctx, ref)` — refuses while the setting is off; withdraws a
    pairing offer on screen, runs the core switch, resets the service's group
    state (the same reset `Forget` does, factored out), connects, and
    publishes `groups` and `connected`/`disconnected` events;
  - `AddGroup(ctx)` — parks the active group and leaves the device ready to
    create or pair;
  - `RemoveGroup(ctx, ref)` — removes a parked group; never the active one.
- The tray needs nothing: it follows the activity, which already carries the
  group name.

## 7. Localhost API

| Route | Body | Answer |
|---|---|---|
| `GET /api/groups` | — | `GroupsView` |
| `POST /api/groups/switch` | `{ "ref": … }` | `GroupsView` |
| `POST /api/groups/add` | — | `GroupsView` |
| `POST /api/groups/remove` | `{ "ref": … }` | `GroupsView` |

`GroupsView` is `{ active: GroupView | null, parked: GroupView[] }`, with
`GroupView = { ref, name, server_url, dead? }`. All four sit behind the usual
token and Origin guard. All four answer 409 while the setting is off.

A new event kind, `groups`, tells an open page that the list changed.

## 8. UI

- **Settings → Behaviour** gains "Keep several groups". Turning it on adds the
  Groups tab; turning it off says the active group stays and the others are
  kept for when it is turned back on.
- **Groups** tab, between Devices and Pairing, shown only with the setting on:
  - the active group first, marked active, with its relay;
  - each parked group with **Switch** and **Remove…**;
  - **Add a group**, which explains that the current group is kept, then parks
    it and opens Pairing to create or join one.
  - Removing asks first and says what the leave dialog says: the keys go from
    this machine and the relay still lists the device.
- A switch reloads every panel through the usual event-driven refresh; History
  and Store are read again because the group changed.
- Settings → Group's leave dialog says, when the setting is on, that only the
  active group is left.

## 9. Docs

- SPECS gains §3.5, the behaviour in a few lines.
- Docs site: a new guide page, *Several groups*, and the setting on the
  Settings page.

## 10. Tests

- Core, against a real relay: two groups, switch back and forth, sync in each,
  restart in between; a switch crashed half way is cleaned up on start; a
  revoked group is discarded, not parked; a parked group catches up on a rekey
  made while it was parked; `Forget` leaves parked groups alone.
- Core, without a relay: the parked record's version gate, the ref, and that a
  keypair is never shared between groups.
- Service: the setting gates every action; a switch resets group state and
  withdraws an offer; removing the active group is refused.
- localui: routes, guard, and bodies.
- UI: typecheck and build.

## 11. Steps

Each step builds and passes the tests on its own:

1. This plan.
2. Client core: parked groups, switch, park, remove, start-up clean-up, tests.
3. Desktop settings and service: the toggle, the four operations, tests.
4. Localhost API: routes, types, the `groups` event, tests.
5. UI: the toggle, the Groups tab.
6. SPECS and the docs site.
