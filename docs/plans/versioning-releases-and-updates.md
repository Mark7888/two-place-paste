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

---

## 0. Facts the design rests on

Checked against this repository and the GitHub API before writing anything below.

| # | Fact | Consequence |
|---|---|---|
| F1 | Listing workflow runs and artifacts works **without a token** on a public repo (`/actions/workflows/desktop.yml/runs`, `/actions/runs/{id}/artifacts`). | Discovery for every channel needs no credentials. |
| F2 | **Downloading** an artifact (`/actions/artifacts/{id}/zip`) **requires an authenticated request, even on a public repo**. Release assets do not. | Stable needs nothing. Beta/Nightly need a token, *or* CI has to mirror those builds somewhere public (§3.7). |
| F3 | Artifacts expire (90 days here: `expires_at` on the listing). | The updater skips `expired: true` and falls back to the next run. |
| F4 | `desktop.yml` builds on `push` to **`main`/`master` only**. Every other branch builds **only through `pull_request`**, and that includes PRs from **forks**. | "Nightly = any branch" has to change the trigger, and it **must never install a fork's build** (§3.6). |
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
| `channel` | `stable` | `beta` (default branch) / `nightly` (any other branch) / `pr<N>` (a `pull_request` run, from `github.event.number`) |
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
  "latest run" means for Beta and Nightly. It also means Android's `versionCode` and the
  updater can never disagree.
- `versionCode` limit: 2 100 000 000. Seconds since 2025 reach that in **2091**, and the
  value is about 55 M today.
- A re-run of an old commit gets a new, larger stamp. That is the honest answer to "which
  build is newer", and it is what Android needs anyway.
- **Downgrades are explicit.** Moving from Nightly to Stable usually means a *smaller*
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
| Artifact **names** | `tppdesktop-macOS-universal-<version>` etc. The updater can then compare a run against itself **from the listing alone**, without downloading anything. `+` is legal in an artifact name. |
| UI | `GET /api/status` (or the new `/api/update`) returns `buildinfo`, and the Settings panel shows it. |

### 1.4 Android prerequisites (from F5)

1. Create one release keystore. Store it as `ANDROID_KEYSTORE_B64`, `ANDROID_KEYSTORE_PASSWORD`,
   `ANDROID_KEY_ALIAS` and `ANDROID_KEY_PASSWORD` secrets. **Back it up offline.** Losing it means
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
| `TwoPlacePaste-macOS-universal.zip` | `ditto`-zipped `.app`. Build arm64 and amd64 (cross-compile with `CGO_ENABLED=1 GOARCH=amd64 CC="clang -arch x86_64"` on the arm64 runner), then `lipo -create`. That covers ROADMAP P6 note 2, and the updater needs no architecture logic. |
| `TwoPlacePaste-Windows-x64.exe` | The bare exe, which is what the updater swaps in. |
| `TwoPlacePaste-Setup.exe` | The NSIS installer, for first installs (puts the exe in `%LOCALAPPDATA%\Programs\TwoPlacePaste`). |
| `TwoPlacePaste-android.apk` | Release-signed APK. |
| `manifest.json`, `manifest.json.sig` | Version, stamp, channel, commit, and the SHA-256 of each asset (§3.6). |
| `SHA256SUMS` | For people. |
| `install-macos.sh` | First-install helper (§3.5). |

---

## 3. The desktop auto-updater

### 3.1 Settings and UI

`config.Settings` gets two non-secret fields. As with every other toggle (SPEC §7.2),
auto-update **defaults to off**:

```go
UpdateChannel string `json:"update_channel"` // "stable" (default) | "beta" | "nightly" | "pr"
UpdatePR      int    `json:"update_pr"`      // the PR number, only for "pr"
UpdatePRSHA   string `json:"update_pr_sha"`  // the head commit the user confirmed (§3.8)
AutoUpdate    bool   `json:"auto_update"`
```

The optional **GitHub token** for Beta/Nightly downloads (F2) is a secret, and
`desktop.json` must stay secret-free, so it goes into the **keystore** under its own name,
next to the device key. A fine-grained PAT with *Public repositories (read-only)* and no
other permissions is enough.

UI (`SettingsPanel.tsx`): a channel dropdown, an auto-update toggle, a token field shown
only for Beta/Nightly, **Check now**, current vs. available version, and **Install & restart**.
The tray gets a *Check for updates…* item.
API (registered like the existing routes, behind `s.guard(originScripted, tokenRequired, …)`):
`GET /api/update`, `POST /api/update/check`, `POST /api/update/install`.

### 3.2 Package layout

```
desktop/internal/buildinfo/     version vars set by -ldflags
desktop/internal/update/
  source.go        Source interface: Latest(ctx, channel) (Candidate, error)
  github.go        releases + Actions implementations (ETag-cached)
  verify.go        ed25519 manifest check + sha256
  schedule.go      wall-clock scheduler (F8); darwin variant in schedule_darwin.go (cgo)
  apply_darwin.go  bundle swap
  apply_windows.go exe swap
```

### 3.3 Finding the candidate per channel

| Channel | Calls (no token needed) | Download |
|---|---|---|
| **Stable** | `GET /repos/{o}/{r}/releases/latest` (excludes drafts and prereleases) → read `manifest.json` | release asset URL, no auth |
| **Beta** | `GET /repos/{o}/{r}` → `default_branch` (cached; handles **`master` and `main`** without hard-coding either) → `GET /actions/workflows/desktop.yml/runs?branch=<default>&event=push&status=success&per_page=5` → `GET /actions/runs/{id}/artifacts` | `archive_download_url`, **with token** |
| **Nightly** | as Beta without `branch=`, then **keep only runs whose `head_repository.full_name == "Mark7888/two-place-paste"`** | same |
| **Pull request #N** | `GET /pulls/{N}` → `head.sha`, `head.repo.full_name`, author, title → `GET /actions/workflows/desktop.yml/runs?head_sha=<sha>&status=success` (a `push` run for a same-repo branch, a `pull_request` run for a fork) → its artifacts | same, **pinned and confirmed** (§3.8) |

Rules that apply to every channel:

- Parse `stamp` from the artifact name. A candidate is offered only if `stamp > buildinfo.Stamp`.
  For an explicit channel switch the user may install a smaller one ("older build").
- Skip `expired` artifacts and fall through to the next run (F3).
- Send `If-None-Match` with the stored ETag. A `304` does not count against the
  **60 requests/hour unauthenticated** limit. With a token the limit is 5 000.
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

This is a public repo, and `desktop.yml` runs on **fork pull requests** (F4). A Nightly that
installs "the latest artifact on any branch" would run a stranger's code on your machine
the moment they opened a PR. Two defences, and you should use both:

1. **Filter.** Only `event == push` runs from this repository, as in §3.3. To make Nightly
   see every branch, change the trigger to `push: branches: ["**"]` and keep `pull_request`
   for forks only, which also avoids double builds:
   `if: github.event_name == 'push' || github.event.pull_request.head.repo.fork`.
2. **Sign.** Generate an ed25519 key pair once. The private key goes into the secret
   `UPDATE_SIGNING_KEY` and the public key is compiled into `buildinfo`. Every build writes
   `manifest.json` (version, stamp, channel, commit, run id, sha256 of the payload) and
   signs it. The updater refuses any payload whose manifest signature fails or whose
   sha256 doesn't match. **Fork PR runs never see secrets**, so they can't produce a valid
   signature even if the filter is bypassed. The same signature protects Stable against
   a tampered release asset.

### 3.7 If a token for Beta/Nightly is too much friction

Mirror those builds to public **rolling prereleases**: after a successful push run, CI
uploads the assets with `gh release upload --clobber` to a fixed prerelease tagged
`channel-beta` (default branch) or `channel-nightly` (any branch). The updater then uses
release-asset URLs for all three channels, with no token and no 90-day expiry. Give that job
`concurrency: { group: publish-${{ channel }}, cancel-in-progress: false }`, and have it
skip the upload if the release already holds a larger stamp. Here the "newest pending run
cancels older pending" behaviour from §1.1 is exactly what you want.

I'd start with the token (it's what was asked for, and it needs no extra CI). Switch to
the mirror if Beta/Nightly ever has testers other than you.

### 3.8 The pull request channel: run any PR's build, on purpose

The goal is to be able to run **any** PR's build, forks included. A Nightly that follows
"whatever built last" is the unsafe way to get there. **Asking for a PR number is the safe
way**, because nothing is installed that you didn't name. What it has to cover:

**A fork's build can't be signed, and must not be.** A fork PR run gets no secrets, so §3.6's
signature can't exist for it. Signing it afterwards in a `pull_request_target` or
`workflow_run` job is the classic "pwn request". It would give a stranger's binary a valid
signature, and that binary could then pass on the Nightly channel too. So PR builds are
**unsigned by design**, and the PR channel is the only place an unsigned build is accepted.
It is installed only after you say yes. Integrity comes from the artifact listing's
`digest` (`sha256:…`), checked against the downloaded zip.

**What you are trusting, and what the UI shows before installing:**
- PR number, title, author, and whether it's from a fork (`head.repo.full_name`).
- The **head commit**, with a link to the PR's *Files changed*.
- A warning if the PR changes `.github/workflows/**`. A `pull_request` run uses the
  workflow files **from the PR**, so a fork can change how its own artifact is built, not
  only the app code.
- One button: **Install this commit**.

**Pinned to the commit you confirmed.** The confirmed SHA is stored in `update_pr_sha`.
What happens when the PR gets new commits depends on whose PR it is:

| PR from | New commits pushed | Auto-update |
|---|---|---|
| This repo (you or a collaborator) | Follows the PR's head like Nightly, if the build is **signed** | allowed |
| A fork | Shows *"PR #N has new commits: review and install?"* | **never**: an author can push a malicious commit after you've looked |

The PR channel also ends on its own terms. When the PR is merged or closed, the UI offers
to switch back to Beta (or Stable). That is usually a smaller stamp, so it's the explicit
downgrade from §1.2.

**Workflow changes this needs:**
- Keep `pull_request` builds for **forks** (the dedupe condition from §3.6 already does).
  Same-repo branches are covered by their `push` runs. The lookup goes by `head_sha`, so
  either kind of run is found.
- `scripts/version.sh` gives `pull_request` runs the channel `pr<N>`, so the artifact name
  says which PR it is: `tppdesktop-Windows-x64-1.0.2-pr42.20261008161500+abc1234`.
- Recommended repo setting: *Settings → Actions → General → "Require approval for all
  external contributors"*. A fork's workflow then doesn't run at all until you approve it,
  so the build can't exist without you having looked at the PR first.
- Fork PR artifacts download with the same token as Beta/Nightly (F2). The rolling-release
  mirror in §3.7 deliberately **does not** cover PRs, because it would need write access in a
  fork-triggered run.

The same flow fits the Android app later: enter a PR number, see the same confirmation,
install the APK. One caveat: a fork's APK can't use the release key (F5). Build it as the
`.debug` application id, so it installs **next to** the real app rather than over it. It
also can't update itself, because each run has a new debug key. A same-repo branch's APK is
signed with the real key and updates normally.

---

## 4. Order of work

1. `scripts/version.sh`, the `version` job in each workflow, `buildinfo`, version into
   plist/winres/NSIS/gradle, version in artifact names. **(Part 1)**
2. Android release keystore + `signingConfigs.release` for all CI builds. **(Part 1, F5)**
3. Update-signing key, `manifest.json` + `.sig` produced in CI. **(§3.6)**
4. `workflow_call` on desktop/android, `release.yml`, universal macOS build, NSIS in CI,
   `install-macos.sh`. Tag `v0.1.0` to prove it. **(Part 2)**
5. `desktop.yml` trigger change for Nightly (push on all branches, PRs only from forks),
   `pr<N>` channel in `version.sh`, and the repo setting requiring approval for fork runs.
6. `internal/update`: sources → verify → apply (Windows first, then macOS) → API + UI → scheduler,
   then the PR channel with its confirmation screen (§3.8).
   **(Part 3)**

## 5. Test plan

- `version.sh` unit-tested with a throwaway repo: no tags, one tag, a tag on another
  branch, a `-rc` tag, and a run on the tag itself.
- `update` package: an `httptest` server that serves canned release and run listings,
  covering expired artifacts, a fork run, a bad signature, a sha mismatch, `304`, and a
  rate-limit response. The swap logic is tested on a temp dir (rename sequence, `.old`
  cleanup, rollback).
- Manual, on real machines: install v0.1.0 → tag v0.1.1 → confirm auto-update **with no
  Gatekeeper prompt** on macOS and no SmartScreen prompt on Windows, the login item still
  working, and keys still readable (F7).
