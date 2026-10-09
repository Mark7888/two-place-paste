# Plan — versioning, releases and the desktop auto-updater

**Status:** proposed.
**Raised by:** ROADMAP P8 (release artefacts). The CI builds today carry no version at all:
`Info.plist`, `winres.json`, the NSIS script and `build.gradle` all hard-code `0.1.0` /
`versionCode 1`, so neither a person nor an updater can tell two builds apart.
**Touches:** `.github/workflows/**`, `scripts/` (new), `/desktop/**`, `/mobile/android/**`.
Nothing in `/proto`, `/server`, `/pkg` or `/spec`.

Three parts, in the order they have to land: **(1)** every build knows its version,
**(2)** a tag produces a GitHub Release, **(3)** the desktop service updates itself from
releases or Actions runs.

**Decisions taken** (2026-10-09):
- Separate macOS builds for Apple Silicon and Intel, not one universal `.app` (§2.2).
- The first tag is `v0.1.0`.
- No Apple Developer ID: the app stays ad-hoc signed and first installs use `install-macos.sh` (§3.5).
- The Android in-app updater is out of scope. This plan gives Android versioning, a
  permanent signing key and APKs on Stable/Beta. The in-app installer is a follow-up.
- "Require approval for all external contributors" is already on.

---

## 0. Facts the design rests on

Checked against this repository and the GitHub API before writing anything below.

| # | Fact | Consequence |
|---|---|---|
| F1 | Listing workflow runs and artifacts works **without a token** on a public repo (`/actions/workflows/desktop.yml/runs`, `/actions/runs/{id}/artifacts`). | Discovery for every channel needs no credentials. |
| F2 | **Downloading** an artifact (`/actions/artifacts/{id}/zip`) **requires an authenticated request, even on a public repo**. Release assets do not. | Stable and Beta are served from Releases (§2, §3.7) and need no token. Only Nightly downloads artifacts, so only Nightly needs a token, kept in the OS's secure storage (§3.9). |
| F3 | Artifacts expire (90 days here: `expires_at` on the listing). | The updater skips `expired: true` and falls back to the next run. |
| F4 | `desktop.yml` builds on `push` to **`main`/`master` only**. Every other branch builds **only through `pull_request`**, and that includes PRs from **forks**. | The triggers stay as they are: no builds for side-branch pushes. A commit with no build is an error in Nightly (§3.8). Beta **must never install a fork's build** (§3.6), and Nightly shows where a commit came from before installing it. |
| F5 | `android.yml` builds a **debug** APK on a fresh runner, so AGP generates a **new debug keystore on every run**. | Two CI APKs never have the same signature, so Android refuses to install one over the other. A stable signing key is a prerequisite for APK updates, whatever the version numbers say. |
| F6 | macOS asks for approval (Settings → Privacy & Security → *Open Anyway*) because the **browser** stamps the download with `com.apple.quarantine` and the app is not notarised. A file that **our own process** downloads with `net/http` and unpacks with `ditto` gets **no quarantine attribute**. Windows behaves the same way: no `Zone.Identifier` stream, so no SmartScreen prompt. | Only the **first** install needs the manual approval. Every update the app applies itself does not. |
| F7 | The macOS key store goes through `/usr/bin/security` (`pkg/tppclient/keystore/keystore_darwin.go`), so the keychain ACL belongs to `security`, not to our ad-hoc signature. | Replacing the binary with a build that has a different ad-hoc signature does **not** lose access to the keys. |
| F8 | Go's monotonic clock stops while a Mac sleeps (`internal/power` relies on exactly this). A `time.Ticker(6h)` can take days of wall time on a laptop that is mostly asleep. | Schedule against a **persisted wall-clock "last checked"** time and check on wake as well, not on a bare ticker. |

---

## 1. Versioning on every run

### 1.1 Use the tag as the source of truth, not a `.version` file

The latest release is just the newest `vX.Y.Z` tag reachable from the commit being built:

```sh
git describe --tags --abbrev=0 --match 'v[0-9]*.[0-9]*.[0-9]*'   # → v1.0.0
```

This needs `actions/checkout` with `fetch-depth: 0` (or `fetch-tags: true` plus enough
history). Nothing is ever written back to the repository, so there is **no race** and no
bot commit on a protected branch.

**Why not a CI-written `.version` file.** A release job that commits `.version` to `master`
needs write access to a protected branch and triggers every workflow again. It also hits
a GitHub concurrency gotcha: a concurrency group holds **one running and one pending** run,
and a newly queued run **cancels the pending one**, even with `cancel-in-progress: false`.
Push three tags quickly and the middle release is dropped without an error.

If you still want a file (for local builds, or so a person can see the version in a
diff), make it **human-written and CI-verified**. You bump `.version` in the PR, and the
release workflow fails if `tag != v$(cat .version)`. CI reads it and never writes it, so
parallel runs are harmless.

### 1.2 The version scheme

One script, `scripts/version.sh`, called by every workflow. It writes `$GITHUB_OUTPUT`:

| Output | Release (`v1.0.1` tag) | Any other run (latest tag `v1.0.0`) |
|---|---|---|
| `version` (SemVer, for people) | `1.0.1` | `1.0.2-dev.20261008161500+56bd693`, see note |
| `channel` | `stable` | `beta` (default branch) / `nightly` (any other branch, or a `pull_request` run) |
| `stamp` (ordering key) | seconds since 2025-01-01T00:00Z | same |
| `version_code` (Android) | `= stamp` | `= stamp` |
| `numeric` (`X.Y.Z`, for plists/Windows) | `1.0.1` | `1.0.2` |

Note: I'd recommend `next-patch-dev` (`1.0.2-dev.…`) over `1.0.1.{timestamp}`. SemVer
precedence then makes every dev build sort **after** the release it is based on and
**before** the next release, whatever that turns out to be. A four-part `1.0.1.N` is not
SemVer, and it reads as if it came after the release. Either works, though, because
nothing orders by this string (next point).

**The ordering key is `stamp`, everywhere.** It is the time the run *built*, taken once per
workflow run in a `version` job, and every other job reads that job's outputs, so macOS,
Windows and Android from the same run share one number. Then:

- **"Newer" means a larger stamp**, on desktop and Android alike. That is exactly what
  "latest run" means for Beta. It also means Android's `versionCode` and the
  updater can never disagree.
- `versionCode` limit: 2 100 000 000. Seconds since 2025 reach that in **2091**, and the
  value is about 55 M today.
- A re-run of an old commit gets a new, larger stamp. That is the honest answer to "which
  build is newer", and it is what Android needs anyway.
- **Downgrades are explicit.** Moving from a Nightly commit back to Beta or Stable often means a *smaller*
  stamp. Desktop offers "Install anyway (older build)". Android cannot downgrade without
  an uninstall, which is the same for every app.

### 1.3 Where the version goes

| Target | How |
|---|---|
| Go binary | New package `desktop/internal/buildinfo` with `Version`, `Channel`, `Stamp`, `Commit`, `RunID`, set by `-ldflags "-X …/buildinfo.Version=…"` in the existing *Build the binary* step. A plain `go build` gets `0.0.0-local`, channel `local`, and **never updates itself**. |
| `Info.plist` | `PlistBuddy -c "Set :CFBundleShortVersionString $numeric"` and `CFBundleVersion $stamp`, in the *Build the .app* step. Apple wants 1–3 integers in both, which `numeric` and `stamp` are. |
| Windows exe | The `.syso` files are committed with `0.1.0`. In CI, regenerate them before `go build`: `go-winres make --arch amd64,arm64 --product-version "$version" --file-version "$numeric.0"`. The fixed file version is four `uint16`s, so the stamp cannot go there. The string `ProductVersion` carries the full version. |
| NSIS | `makensis -DVERSION=$numeric -DSOURCE_EXE=…` (the script already takes both). |
| Android | `build.gradle`: `versionCode (findProperty("versionCode") ?: "1") as Integer`, `versionName findProperty("versionName") ?: "0.0.0-local"`, and CI passes `-PversionCode=… -PversionName=…`. |
| Artifact **names** | `tppdesktop-macOS-arm64-<version>`, `tppdesktop-macOS-x64-<version>`, `tppdesktop-Windows-x64-<version>`. The updater can then compare a run against itself **from the listing alone**, without downloading anything. `+` is legal in an artifact name. |
| UI | `GET /api/status` (or the new `/api/update`) returns `buildinfo`, and the Settings panel shows it. |

### 1.4 Android prerequisites (from F5)

1. Create one PKCS#12 release keystore. Store it as `ANDROID_KEYSTORE_B64`,
   `ANDROID_KEYSTORE_PASSWORD` and `ANDROID_KEY_ALIAS` secrets. In a PKCS#12 store the key
   password is the store password, so there's no separate key-password secret. **Back it up offline.** Losing it means
   every user has to uninstall.
2. Add a `signingConfigs.release` that reads those values from env/properties, and use it
   for **every** CI build (release *and* dev). If the secrets are absent (forks), fall back to
   debug signing and name the artifact `…-unsigned-…` so nothing installs it as an update.
3. Use one `applicationId` (`com.twoplacepaste`) for all channels, so a phone can move
   Stable → Beta → Nightly by version code. Keep the `.debug` suffix for local
   `assembleDebug` only.
4. CI then builds `assembleRelease` (minified, signed) rather than `assembleDebug -PbundleInDebug`.

The in-app APK installer for Android (`REQUEST_INSTALL_PACKAGES` + `PackageInstaller`)
deserves its own plan. It can reuse §3.3's channel logic unchanged, because it reads the
same API.

---

## 2. Release workflow on `v*.*.*` tags

### 2.1 Make the existing builds reusable

Add `workflow_call` (with inputs `version`, `channel`, `stamp`) to `desktop.yml` and
`android.yml`, next to their current triggers. The release workflow then **calls** them
rather than duplicating the build steps. Artifacts that a called workflow uploads belong to
the caller's run, so the release job can `download-artifact` them.

### 2.2 `.github/workflows/release.yml`

```yaml
name: Release
on:
  push:
    tags: ["v[0-9]+.[0-9]+.[0-9]+", "v[0-9]+.[0-9]+.[0-9]+-*"]   # -rc.1 → prerelease

concurrency:
  group: release-${{ github.ref }}      # per tag: two different tags may run in parallel
  cancel-in-progress: false

permissions:
  contents: read

jobs:
  version:        # runs scripts/version.sh; also asserts the tagged commit is on the default
                  # branch (git merge-base --is-ancestor) and, if .version exists, that it matches
  desktop:        # uses: ./.github/workflows/desktop.yml  with channel=stable
  android:        # uses: ./.github/workflows/android.yml  with channel=stable, secrets: inherit
  publish:
    needs: [version, desktop, android]
    permissions:
      contents: write                   # the only job that can write
    steps:
      # download-artifact → stage with stable names → SHA256SUMS → sign manifest (§3.6)
      # gh release create "$TAG" --verify-tag --generate-notes [--prerelease if '-' in tag] <files>
```

**Asset names are fixed across releases**, so the latest one is always one URL away:
`https://github.com/Mark7888/two-place-paste/releases/latest/download/<name>`

| Asset | Contents |
|---|---|
| `TwoPlacePaste-macOS-arm64.zip` | `ditto`-zipped `.app` for Apple Silicon, built natively on the arm64 runner. |
| `TwoPlacePaste-macOS-x64.zip` | The same for Intel, cross-compiled on the same runner (`CGO_ENABLED=1 GOARCH=amd64 CC="clang -arch x86_64"`). A step asserts `lipo -archs` prints `x86_64`. This covers ROADMAP P6 note 2. The updater picks the zip for `runtime.GOARCH`. One exception: an Intel build running under Rosetta on Apple Silicon (`sysctl.proc_translated == 1`) switches to the arm64 build at its next update. |
| `TwoPlacePaste-Windows-x64.exe` | The bare exe, which is what the updater swaps in. |
| `TwoPlacePaste-Setup.exe` | The NSIS installer, for first installs (puts the exe in `%LOCALAPPDATA%\Programs\TwoPlacePaste`). |
| `TwoPlacePaste-android.apk` | Release-signed APK. |
| `manifest-desktop.json` (+ `.sig`), `manifest-android.json` (+ `.sig`) | Version, stamp, channel, commit, and the SHA-256 of each of that platform's assets (§3.6). One manifest per platform, because the Beta release (§3.7) is filled by two separate workflows. |
| `SHA256SUMS` | For people. |
| `install-macos.sh` | First-install helper (§3.5). |

---

## 3. The desktop auto-updater

### 3.1 Settings and UI

`config.Settings` gets two non-secret fields. As with every other toggle (SPEC §7.2),
auto-update **defaults to off**:

```go
UpdateChannel string `json:"update_channel"` // "stable" (default) | "beta" | "nightly"
NightlyCommit string `json:"nightly_commit"` // full SHA the user entered, only for "nightly" (§3.8)
AutoUpdate    bool   `json:"auto_update"`
```

The **GitHub token** is needed only for Nightly (F2). It is a secret, so it never goes
into `desktop.json`; §3.9 covers how it's stored and used.

UI (`SettingsPanel.tsx`): a channel dropdown, an auto-update toggle, and, shown only for Nightly,
a **commit hash** field and the token controls (§3.9), **Check now**, current vs. available version, and **Install & restart**.
The tray gets a *Check for updates…* item.
API (registered like the existing routes, behind `s.guard(originScripted, tokenRequired, …)`):
`GET /api/update`, `POST /api/update/check`, `POST /api/update/install`, and
`PUT`/`DELETE /api/update/token` (§3.9).

### 3.2 Package layout

```
desktop/internal/buildinfo/     version vars set by -ldflags
desktop/internal/update/
  source.go        Source interface: Latest(ctx, channel) (Candidate, error)
  release.go       Stable/Beta: manifest + asset from a release URL, no API calls
  actions.go       Nightly: commit lookup + artifact download via the API (§3.8)
  token.go         token load/save/delete through the keystore (§3.9)
  verify.go        ed25519 manifest check + sha256
  schedule.go      wall-clock scheduler (F8); darwin variant in schedule_darwin.go (cgo)
  apply_darwin.go  bundle swap
  apply_windows.go exe swap
```

### 3.3 Finding the candidate per channel

| Channel | Check | Download | Token |
|---|---|---|---|
| **Stable** | `GET github.com/{o}/{r}/releases/latest/download/manifest-desktop.json` (`latest` excludes drafts and prereleases) | the asset named in the manifest, same URL form | no |
| **Beta** | `GET github.com/{o}/{r}/releases/download/channel-beta/manifest-desktop.json` (§3.7) | same | no |
| **Nightly** (commit) | `GET /commits/{hash}` (resolves a short hash to the full SHA; works for fork-PR commits too) → `GET /actions/workflows/desktop.yml/runs?head_sha=<full sha>` → its artifacts | `archive_download_url`, **after confirmation** for an unsigned build (§3.8) | **yes** (§3.9) |

Stable and Beta use **plain release-download URLs, not the REST API**. That means no API rate
limit and no token, and the app never has to know whether the default branch is `master`
or `main`: CI decides that when it publishes (§3.7).

Rules that apply to every channel:

- Stable and Beta: parse `stamp` from the artifact name. A candidate is offered only if
  `stamp > buildinfo.Stamp`. On an explicit channel switch the user may install a smaller one
  ("older build"). Nightly ignores the stamp: you named the build, so it's installed
  whether it's older or newer.
- Nightly: an `expired` artifact (F3) means the commit needs a new build. Say so, and link
  the run so it can be re-run.
- Nightly sends the token on its API calls, which gives 5 000 requests/hour rather than
  the 60/hour an unauthenticated client gets. It only calls the API when you enter a hash
  or while it waits for a running build.
- Respect `Retry-After` / `X-RateLimit-Reset`. Never retry in a tight loop.

### 3.4 Scheduling without costing battery

- **Triggers:** shortly after start (about 2 min, so login isn't slowed), on wake
  (`power.WatchWake` already calls `svc.Resumed`, so hook the updater there), and on a
  periodic tick. Each trigger only proceeds if
  `now − lastChecked ≥ 6h` (wall clock, persisted in the config dir), per F8.
- **macOS:** use **`NSBackgroundActivityScheduler`** (interval 6 h, tolerance 2 h,
  `qualityOfService = .utility`). This is Apple's API for exactly this case. The system
  coalesces the work with other wake-ups and **defers it on battery, in Low Power Mode, and
  under thermal pressure**. A small cgo shim is enough, the same way `clipboard/changecount_darwin.go`
  is done, and the callback calls into Go. Also skip auto-checks when
  `NSProcessInfo.isLowPowerModeEnabled`. A `!cgo` build falls back to the Go loop below.
- **Windows (and the fallback):** a goroutine with a coarse ticker (30 min) that only does
  the wall-clock comparison and returns, which costs nothing. Optionally skip while
  `GetSystemPowerStatus` reports Battery Saver.
- **Download only when an update is found.** Auto-update **downloads in the background
  and installs at the next idle moment**: screen locked (`power.ScreenLocked`, which
  already exists), or no clipboard sync in the last N minutes. It never restarts the
  service in the middle of someone's paste.

### 3.5 Applying the update

The same handshake on both platforms. **The old process must release port 47821 before
the new one binds**, or the new one shows the tray's "port in use" failure:

1. Download into `<user cache>/TwoPlacePaste/update/`, on the **same volume** as the install so
   that rename is atomic.
2. Verify (§3.6). On any failure, delete the download and report it. Nothing has been touched yet.
3. Swap (platform-specific, below).
4. Start the new binary with `--wait-pid <old pid>`, then shut down cleanly (`stop()`).
   The new process waits up to 10 s for that PID to exit, then binds and starts.
5. On its first healthy start, the new process deletes the `.old` copy left by the
   *previous* update. The copy from *this* update stays until the next one, so the UI can
   offer **Roll back**.

**Windows** (`%LOCALAPPDATA%\Programs\TwoPlacePaste`, per-user, no admin needed):
- A running exe can't be overwritten but **can be renamed**:
  `tppdesktop.exe → tppdesktop.exe.old`, `tppdesktop.exe.new → tppdesktop.exe`.
- Launch with `CREATE_NEW_PROCESS_GROUP | DETACHED_PROCESS` so the child outlives us.
- The path doesn't change, so the `HKCU\…\Run` login item stays valid.
- Downloaded by us, so no Mark-of-the-Web (F6). SmartScreen only appears for the first
  install, from the browser.

**macOS:**
- Unpack with `ditto -x -k` into the staging dir. Check the new bundle's
  `CFBundleIdentifier == com.twoplacepaste.desktop` and run `codesign --verify --deep`.
- **Swap the whole bundle, never files inside it.** Rewriting the binary inside a running
  signed bundle can get the process killed with *Code Signature Invalid*.
  `rename(App.app → App.app.old)`, then `rename(staged.app → App.app)`.
- If the bundle's directory isn't writable (`/Applications` for a non-admin user), say so
  and suggest `~/Applications`. Never ask for an admin password.
- Relaunch with `open -n <bundle> --args --wait-pid <pid>`. The LaunchAgent plist points
  at `…/Contents/MacOS/tppdesktop`, which still exists after the swap.
- No quarantine attribute (F6), so **no trip to Privacy & Security** after the first install.

**First install on macOS** (the one approval nobody can skip without a Developer ID):
- `install-macos.sh`, published with each release:
  `curl -fsSL https://github.com/Mark7888/two-place-paste/releases/latest/download/install-macos.sh | sh`.
  It downloads with `curl`, which doesn't set quarantine, unpacks to `/Applications`
  (or `~/Applications`) and opens the app. No Settings detour.
- Or by hand: `xattr -dr com.apple.quarantine /Applications/TwoPlacePaste.app`.
- The real fix is an Apple Developer ID ($99/year) plus `notarytool`. The workflow already
  has the `MACOS_SIGN_IDENTITY` branch for it. With notarisation the updater still works
  unchanged.

### 3.6 Trust: never install a stranger's build

This is a public repo, and `desktop.yml` runs on **fork pull requests** (F4). Any channel
that installs "whatever built last" without you choosing it must never pick up such a
build: it would run a stranger's code on your machine the moment they opened a PR. Beta
and Stable install on their own, so they rely on two defences. Use both:

1. **Filter.** Only the publish job in §3.7 writes to the Beta release. It runs only on
   `push` to the default branch of this repository, where fork code can't land without you
   merging it. The job holds `contents: write` and runs nothing from the checkout.
2. **Sign.** Generate an ed25519 key pair once. The private key goes into the secret
   `UPDATE_SIGNING_KEY` (a PKCS#8 PEM, made with `openssl genpkey -algorithm ed25519`). The
   public key is committed (done) as `desktop/internal/update/update-signing.pub.pem` and embedded
   with `go:embed`. It isn't secret, and committing it makes any change to it visible in review.
   CI signs on `ubuntu-latest` (OpenSSL 3):
   `openssl pkeyutl -sign -rawin -inkey key.pem -in manifest.json -out manifest.json.sig`. Every build writes
   `manifest.json` (version, stamp, channel, commit, run id, sha256 of the payload) and
   signs it. The updater refuses any payload whose manifest signature fails or whose
   sha256 doesn't match. **Fork PR runs never see secrets**, so they can't produce a valid
   signature even if the filter is bypassed. The same signature protects Stable against
   a tampered release asset.

### 3.7 Beta: a rolling prerelease

Beta builds are published to one fixed prerelease, tagged `channel-beta`, so that the
updater can download them from a public, permanent URL with no token. The release
**holds one build at a time**, overwritten by each push to the default branch, so it
never collects old files.

A `publish-beta` job, added to both `desktop.yml` and `android.yml`:

```yaml
publish-beta:
  needs: [package]          # the existing build jobs
  if: github.event_name == 'push' && github.ref_name == github.event.repository.default_branch
  runs-on: ubuntu-latest
  permissions:
    contents: write         # the only job outside release.yml that can write
  concurrency:
    group: publish-beta-${{ github.workflow }}
    cancel-in-progress: false
  steps:
    # 1. download-artifact (this run's builds, plus its signed manifest-<platform>.json)
    # 2. if the release's current manifest has a larger stamp, stop: a newer run already published
    # 3. gh release upload channel-beta <payloads> --clobber   # payloads first
    # 4. gh release upload channel-beta manifest-<platform>.json manifest-<platform>.json.sig --clobber
    # 5. move the channel-beta tag to this commit, and put the version in the release title
```

Details that matter:
- **`github.event.repository.default_branch`** makes this work on `master` today and on
  `main` after a rename, with nothing hard-coded.
- **Payloads go up first and the manifest last.** `--clobber` replaces files one at a time,
  so for a few seconds the release can hold a new payload under the old manifest. The
  updater checks the payload's sha256 against the manifest, finds a mismatch, and tries
  again at the next check. It never installs the half-updated pair.
- **The concurrency group is correct here.** If several pushes land close together, the
  pending-run cancellation from §1.1 drops the older pending one, which is what you want.
  Step 2 covers a slow older run that finishes after a newer one.
- **The release is created on first use** by the publish job (as a prerelease titled
  "Beta"), so there is nothing to set up by hand. The tag matches neither `v*` pattern, so it
  never triggers `release.yml`, and `releases/latest` ignores prereleases, so Stable is
  unaffected. The upload logic lives in `scripts/publish-beta.sh`, shared by both workflows.
- Android's Beta APK goes into the same release (`manifest-android.json`), so the phone's
  updater can later read Beta the same way.

### 3.8 Nightly: install one specific commit

Nightly isn't a feed. **You enter a commit hash, and the app installs that commit's build.**
That covers every case the earlier "any branch" and "PR number" options did:
- a branch in this repo;
- a PR's head commit, a fork's included;
- an old commit on `master`, to bisect a regression.

Nothing is installed that you didn't name, which makes it the safe way to run anything.

**Lookup:**
1. Accept a short or full hash. `GET /repos/{o}/{r}/commits/{hash}` resolves it to the full
   SHA. A fork PR's commits resolve too, because GitHub keeps them under `refs/pull/N/head`
   in this repo. An ambiguous short hash is an error, so ask for more characters.
2. `GET /actions/workflows/desktop.yml/runs?head_sha=<full sha>`. Possible outcomes:
   - **No run:** an error, and nothing else. *"No desktop build exists for commit
     abc1234. Only commits on the default branch and the latest commit of an open pull
     request are built, and only when they change `desktop/`."* No workflow is started
     from the app, so the token stays read-only (§3.9).
   - **Running or queued:** show "build in progress". Install automatically when it
     finishes, but only if you've already confirmed it (below).
   - **Failed:** show the run link. Nothing to install.
   - **Several successful runs** (a re-run, or a commit that was both a PR head and then
     pushed to the default branch): prefer the **signed** one, then the newest.

**Signed or not decides whether you're asked:**

| Build | Comes from | What happens |
|---|---|---|
| **Signed** (§3.6) | a run in this repo, which had the secrets | Installs straight away, like Beta. |
| **Unsigned** | a fork PR run (no secrets), or any run without a valid signature | A confirmation screen first. |

A fork's build can't be signed, and must not be. Signing it afterwards in a
`pull_request_target` or `workflow_run` job is the classic "pwn request": it would give a
stranger's binary a valid signature, and that binary would then pass the checks for
signed builds everywhere. So unsigned builds are accepted **only through Nightly, only for
the exact commit you typed, and only after you confirm**. Integrity comes from the
artifact listing's `digest` (`sha256:…`), checked against the downloaded zip.

The confirmation screen shows:
- the commit's message and author;
- where it came from: the branch, or the PR number, title and fork name (from the run's
  `head_repository`, `head_branch` and, when GitHub can link it, `GET /commits/{sha}/pulls`);
- a link to the commit's diff;
- a warning if the commit's PR changes `.github/workflows/**`. A `pull_request` run uses
  the workflow files **from the PR**, so a fork can change how its own artifact is built,
  not only the app code;
- one button: **Install this commit**.

**A commit never changes, so Nightly never auto-updates.** The auto-update toggle and the
schedule don't apply while Nightly is selected. The one exception is waiting for a build
that is still running, and only for a commit you've confirmed. A re-run of the same commit
makes a build with a newer stamp. Ignore it: it's the same code. To move on, you enter a
new hash, or switch back to Beta or Stable (often a smaller stamp, so the explicit
downgrade from §1.2).

**Workflow changes this needs:**
- **None to the triggers.** `desktop.yml` keeps building on `push` to the default branch
  and on `pull_request`. A side branch gets a build only once it has a PR, and only for
  commits that change `desktop/**`. Each push to a PR builds its new head commit, so the
  commit you'd want to try is the one that has a build. Same-repo PR runs get the secrets,
  so they're signed and install without the confirmation screen. Fork PR runs aren't.
- Recommended repo setting: *Settings → Actions → General → "Require approval for all
  external contributors"*. A fork's workflow then doesn't run until you approve it, so an
  unsigned build can't exist without you having looked at the PR first.
- Every Nightly download needs the token (F2, §3.9).

The same flow fits the Android app later: enter a hash, see the same confirmation, install
the APK. One caveat: a fork's APK can't use the release key (F5). Build it as the `.debug`
application id, so it installs **next to** the real app rather than over it. A same-repo
commit's APK is signed with the real key and installs over the existing app as usual,
as long as its `versionCode` is higher.

### 3.9 The Nightly token: how it's created, stored and used

Only Nightly uses it, and only to read Actions data. A missing or invalid token affects
Nightly alone; Stable and Beta work without one.

**The token to create.** A **fine-grained** personal access token, never a classic one:
- *Repository access:* **Only select repositories →** `Mark7888/two-place-paste`.
- *Permissions:* **Actions: Read-only**. GitHub adds *Metadata: Read-only* on its own.
  Nothing else.
- *Expiration:* 90 days or less. GitHub sends the expiry date back in the
  `github-authentication-token-expiration` response header, so the app can show "expires
  in 12 days" and warn you before it does.

That token can read workflow runs and download artifacts from this one repository. It
can't push, open PRs, start workflows, or see any other repository.

**Where it's stored: the existing keystore,** as a secret named `github-token` next to the
device key (`pkg/tppclient/keystore`):

| OS | Backend | Protection |
|---|---|---|
| macOS | login keychain, through `/usr/bin/security` | encrypted with your login password, like the device key |
| Windows | file sealed with **DPAPI** under your account | only your Windows account on this machine can decrypt it |

So it gets exactly the protection the clipboard's own keys already have, without new storage
code. Never in `desktop.json`, never in the log: the conventions already forbid logging
secrets (docs/conventions.md §2), and the updater logs only "token set / not set / rejected".

**How the UI handles it:**
- `PUT /api/update/token` accepts it, checks it (one call to the run listing), and then
  saves it. Behind the same guard as the other routes (origin check + session token), on
  127.0.0.1 only.
- **It can't be read back.** `GET /api/update` returns only `{token: {set, expires_at,
  valid}}`. The field in the UI shows "Token saved, expires 2027-01-05" with **Replace** and
  **Remove** buttons.
- `DELETE /api/update/token` removes it from the keystore. A `401` from GitHub marks it
  invalid and asks for a new one; it doesn't keep retrying with a dead token.

**How it's sent:**
- Only as `Authorization: Bearer …`, only over HTTPS, and only to `api.github.com`. Never in
  a URL, so it can't end up in a log or a proxy's history.
- The artifact download is two explicit steps. The first request goes to `api.github.com`
  with the token and **doesn't follow the redirect** (`CheckRedirect` returns
  `http.ErrUseLastResponse`). The second fetches the short-lived signed storage URL from
  `Location` with a **fresh request and no token**. Go already drops `Authorization` on a
  redirect to another host, but doing it explicitly keeps the token from depending on that.

**For development from a checkout:** an environment variable, `TPP_DESKTOP_GITHUB_TOKEN`,
overrides the stored token. It follows the existing `TPP_DESKTOP_*` overrides in
`cmd/tppdesktop`, and is never written anywhere.

---

## 4. Order of work

**One pull request, one commit per step**, in this order. Each commit builds and passes its
own tests, so the history can be reviewed and bisected step by step.

Already done before the PR: the four repository secrets, the update-signing public key
(`desktop/internal/update/update-signing.pub.pem`), and the repo setting that requires
approval for fork runs.

1. **Versioning.** `scripts/version.sh` and its tests, the `version` job in `desktop.yml` and
   `android.yml`, `desktop/internal/buildinfo`, the version in plist/winres/NSIS/Gradle, and
   the version in artifact names. **(Part 1)**
2. **Separate macOS builds.** Apple Silicon natively, plus Intel cross-compiled with an
   architecture check.
3. **Android release signing.** `signingConfigs.release` from secrets for every CI build,
   with fork builds falling back to the `.debug` app. **(F5)**
4. **Signed manifests.** `manifest-<platform>.json` + `.sig` produced in CI. **(§3.6)**
5. **Release workflow.** `workflow_call` on desktop/android, `release.yml`, NSIS in CI,
   `install-macos.sh`. **(Part 2)**
6. **Beta publishing.** The `publish-beta` job in both workflows; it creates `channel-beta`
   if missing. **(§3.7)**
7. **Updater core.** Settings fields, release source (Stable/Beta), signature and checksum
   check. **(Part 3)**
8. **Applying updates.** The Windows exe swap, the macOS bundle swap, `--wait-pid`, and rollback.
9. **Scheduling.** Wall-clock checks, check on wake, `NSBackgroundActivityScheduler` on
   macOS, and installs at idle.
10. **API and UI.** Routes, Settings panel, tray item, version display.
11. **Nightly.** Token storage (§3.9), commit lookup, and the confirmation screen for unsigned
    builds (§3.8).

After merge: tag `v0.1.0` to prove the release pipeline, then `v0.1.1` for the manual
update test (§5).

## 5. Test plan

- `version.sh` unit-tested with a throwaway repo: no tags, one tag, a tag on another
  branch, a `-rc` tag, and a run on the tag itself.
- `update` package: an `httptest` server that serves canned release and run listings,
  covering expired artifacts, a fork run, a bad signature, a sha mismatch, `304`, and a
  rate-limit response, a half-updated Beta release (new payload, old manifest), and a `401`
  on the token. A test asserts the token never reaches the storage host or the log. The swap logic is tested on a temp dir (rename sequence, `.old`
  cleanup, rollback).
- Manual, on real machines: install v0.1.0 → tag v0.1.1 → confirm auto-update **with no
  Gatekeeper prompt** on macOS and no SmartScreen prompt on Windows, the login item still
  working, and keys still readable (F7).
