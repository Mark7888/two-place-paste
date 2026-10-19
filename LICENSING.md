# Licensing

Copyright (C) 2026 Mark Licsauer

TwoPlacePaste is open source under two licenses. The default for everything in
this repository is the **Mozilla Public License 2.0**; the relay server is the
one exception and is under the **GNU Affero General Public License v3.0 or
later**.

| Path | License | License text |
|---|---|---|
| Everything not listed below | MPL-2.0 | [`LICENSE`](LICENSE) |
| `server/` and everything under it | AGPL-3.0-or-later | [`server/LICENSE`](server/LICENSE) |
| `pkg/tppclient/` | MPL-2.0 (a copy of the default) | [`pkg/tppclient/LICENSE`](pkg/tppclient/LICENSE) |

The MPL-2.0 part includes the Android app (`mobile/`), the desktop app
(`desktop/`), the Go client core (`pkg/tppclient/`), the wire schema
(`proto/`), the crypto spec and test vectors (`spec/`), the deployment files
(`deploy/`), the scripts and the documentation.

`pkg/tppclient/LICENSE` is the same MPL-2.0 text as the root `LICENSE`. It is
there because `pkg/tppclient` is a Go module of its own, and Go tooling such as
pkg.go.dev only looks inside a module's folder for its license.

## Why two licenses

- **The relay server is AGPL** so that anyone who runs a modified relay as a
  service for other people has to offer those people the modified source.
  The relay's admin and pairing pages link to its source code for this; an
  operator running a modified relay points the link at their version with
  `TPP_SOURCE_URL`.
- **Everything else is MPL** so the apps can be published in app stores (the
  Apple App Store's terms are incompatible with the GPL family), and so other
  projects can build compatible clients on the wire schema and the client core.
  MPL is still copyleft per file: changes to these files have to be shared when
  the software is distributed.

The server is built with code from `pkg/tppclient/`. MPL-2.0 explicitly allows
that: the combined server binary is distributed under AGPL-3.0-or-later, while
the `pkg/tppclient` files themselves stay under MPL-2.0.

## Name and logo

The TwoPlacePaste name, logo and app icon are not licensed under either
license. That covers the artwork in [`assets/icon/`](assets/icon/) and every
icon generated from it (listed in [`assets/icon/README.md`](assets/icon/README.md)).
The code in that folder (`generate.py`) is MPL-2.0 like the rest.

If you distribute a modified version, give it your own name and icon so users
can tell it apart from the official one.

## Third-party code

Dependencies keep their own licenses. Generated and vendored code follows the
license of the folder it is generated into.

Most of those licenses (MIT, BSD, ISC, Apache-2.0) ask that a binary
distribution carry their copyright notices and license texts. Every build does
that with a notices file written by [`scripts/notices`](scripts/notices/notices.mjs)
from what the build actually used:

| Build | Where the notices are |
|---|---|
| Relay (Docker image) | `/usr/share/doc/tpp/THIRD_PARTY_NOTICES.txt`, next to the relay's `LICENSE` and `LICENSE.MPL-2.0` |
| Desktop app | Embedded; **Settings → About → Open-source licenses** |
| Android app | In the APK's assets; **Settings → About → Open-source licenses** |

## Contributions

Contributions are accepted under the license of the folder they change: MPL-2.0
for most of the repository, AGPL-3.0-or-later for `server/`. There is no
contributor license agreement. See [`CONTRIBUTING.md`](CONTRIBUTING.md).
