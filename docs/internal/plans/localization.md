# Plan — Localization of the desktop and Android apps

**Status:** proposed, for review. Nothing is built yet.
**Touches:** `/desktop/**` (config, service, localui, tray, a new `internal/i18n`, UI),
`/mobile/**` (app, core error handling, platform, Android resources), `/scripts/**`
(a translation check), `.github/workflows/` (a new `i18n.yml`, in its own PR),
`/docs/site/**`. No change to `/proto`, `/spec`, `/api`, the server or the admin UI.

---

## 1. Goals and non-goals

1. Every sentence a person reads in the desktop app (UI, tray, OS notifications)
   and the Android app (screens, notifications, quick-settings tile) comes from a
   **translation catalog in the app**, never from the relay.
2. **English (`en.json`) is the source of truth.** Every other language is a file
   next to it. A key missing from that file falls back to English, key by key.
3. **Adding a language is adding a file.** No code lists the languages: the apps
   discover the catalogs that are there, and the language picker names each one
   from the file itself.
4. **First-run language choice.** A new install's setup starts with "Choose a
   language", preselected from the OS language when a catalog for it exists.
   **Existing installs stay on English** until the user changes it in Settings.
5. **Hungarian** is the first translation.
6. A CI check on `master` reports, per language, the keys `en.json` has and the
   translation lacks. It fails its own job but **does not block the Beta
   publish** (review answer 1a).

Not in scope: the relay's pairing page and the admin UI (a later, separate
plan); right-to-left layout; translating logs, CLI output, `TPP_*` settings
or the settings file's keys; translating user data such as device names, group
names (the admin's display text) or the relay operator's URL; a hosted
translation tool (the file format below is one Weblate and Crowdin read, so
one can be connected later without changing the apps).

## 2. What exists today

### 2.1 Text that comes from the relay

The relay's `Error.message` (`proto/tpp/v1/error.proto`) is meant for logs,
and both clients are supposed to branch on `code`. Mostly they do. These paths
still let relay wording reach the screen:

| Where | What leaks | Path to the screen |
|---|---|---|
| `desktop/internal/service/service.go` `Status` (`st.LastError = err.Error()`) | `latest()` returns a `StatusError`; its `Error()` appends the wrapped error, so the user sees `…: tppclient: server error ERROR_CODE_X: <relay text>` | `status.last_error` → `SyncPanel.tsx` |
| `service.go` `OnDisconnected` (`msg = err.Error()`) | The WebSocket close reason the relay sent ("device revoked", "write queue overflow", …) or a dial error | `s.lastError` → `status.last_error` → `SyncPanel.tsx` |
| `service.go` `awaitOffer` (`Message: err.Error()`) | A pairing offer's failure, relay code and message included | `EventOffer.message` (the UI does not render it today, but it is one change away from doing so) |
| `mobile/src/core/protocol/errors.ts` `describe()` | Only five relay codes get their own sentence. Every other code (`TOKEN_EXPIRED`, `TOKEN_CONSUMED`, `RATE_LIMITED`, `NOT_FOUND`, `EPOCH_CONFLICT`, `UNAUTHENTICATED`, `PERMISSION_DENIED`, `INTERNAL`, …) falls through to `relay error ERROR_CODE_X: <relay text>` | `failureMessage` (37 call sites) |
| `mobile/src/core/protocol/connection.ts` | `TppError('disconnected', 'the relay closed the connection: ${reason}')` quotes the relay's close reason | `describe()` → `err.message` |

Device names and the group name also come from the relay, but they are data
the user or admin typed, shown as-is, and stay untranslated.

### 2.2 Where the English lives

| Area | Today | Size (rough) |
|---|---|---|
| Desktop UI (`desktop/ui/src`) | Literals in JSX, about 7,300 lines over 30 files. About 40 places show `err.message` / `res.message` composed by the Go service | Most of the work |
| Desktop service (`desktop/internal/service`, `localui`) | About 88 `localui.Errorf(...)` sentences, sync results (`"uploaded " + describe(c)`), `fmt.Sprintf` notices ("A %s entry arrived while auto-sync was paused…"), event messages, `writeError` strings, `filterMessage` (validation text from `internal/sensitive`), updater states | Medium |
| Tray (`desktop/internal/tray`) | Menu labels, tooltips, OS notifications, the "could not start" menu | Small |
| Mobile app (`mobile/src/app`) | Literals in screens; `toLocaleString()` with no locale; sizes and plurals built by hand | Large |
| Mobile core (`mobile/src/core`) | `describe()` sentences and 76 `new TppError(kind, '…')` messages, some shown to the user through `describe()` | Medium |
| Android native | `strings.xml` holds two strings; `TransferService.kt` hard-codes the channel description and "Upload finished" / "Upload failed" | Small |

Dates go through `toLocaleString()` without a locale (about 15 places), so they
follow the OS rather than the app's language.

### 2.3 First-run detection

- **Desktop:** `config.Settings.SetupDone` (`setup_done`) records that the
  setup wizard (`desktop/ui/src/components/SetupWizard.tsx`) was finished. The
  wizard saves every answer in one request at the end.
- **Mobile:** there is no wizard. `App.tsx` shows `SetupScreen` (pair, accept
  a code, create a group, demo) until the device is in a group. Small flags
  (`MULTI_GROUP_NAME`, `HIDE_SPACE_BANNERS_NAME`) live in `secureStore`.

## 3. Decisions

| # | Decision | Why |
|---|---|---|
| D1 | **i18next + react-i18next** in both apps, current versions pinned `~`. Both are MIT. | One library and one file format for web React and React Native. Per-key fallback to `en` is built in (`fallbackLng: 'en'`). Plurals use `Intl.PluralRules`, which Hermes has. Its JSON v4 format is read by Weblate, Crowdin and most other tools. FormatJS (ICU MessageFormat) was the alternative: more expressive, but heavier, and it needs `Intl` polyfills on Hermes. |
| D2 | **One catalog per app** (review answer 4): `desktop/internal/i18n/locales/<lang>.json` and `mobile/src/locales/<lang>.json`. File names are BCP 47 tags (`en`, `hu`, later `pt-BR`). | The two apps share almost no sentences, and per app keeps each catalog next to the only code that reads it. |
| D3 | **Keys are nested by area**, written dotted in code: `sync.uploadButton`, `errors.relay.tokenExpired`, `tray.menu.quit`. Interpolation is i18next's `{{name}}`. Plurals use i18next's suffixes (`_one`, `_other`, plus `_few` / `_many` where a language needs them). | Reviewers can find a screen's strings; translators get context from the path. |
| D4 | **Each file names itself**: a top-level `"_meta": { "name": "Magyar", "englishName": "Hungarian" }`. The picker lists the files it finds, each in its own name. | Adding a language needs no code and no list to edit. |
| D5 | **Language detection and default.** The best match is: the OS's preferred languages, in order, first by exact tag, then by base language (`hu-HU` → `hu`); else `en`. That match is only a **preselection** in the first-run picker. A saved choice always wins. | As requested: a new user starts in their language, and one tap confirms or changes it. |
| D6 | **Existing installs stay on English.** Desktop: `Settings.Language` (`"language"`, `omitempty`). When it is empty, the language is `en` if `setup_done` is true, and the OS match while setup is still unfinished. Mobile: a `language` key in `secureStore`. At startup, if it is absent and the device has finished setup (it is in a group, or has parked groups), `en` is written once. Otherwise the picker is shown. | Matches review answer 3 without a migration step anyone has to run. |
| D7 | **Desktop: the Go service sends codes and parameters, never sentences.** Every message the service produces for a person becomes `{ "code": "sync.autoPaused", "params": { "kind": "image", "bytes": 1048576 } }`, in error bodies, sync results, notices and events. The UI renders it with `t(code, params)`. Parameters are raw values (byte counts, ISO times, `kind` codes); the UI formats them in the chosen language. | Preferred in review. The service and UI ship in one binary, so the local API can change shape in one step with no compatibility period. Sentences built in Go could never be translated well (word order, plurals, Hungarian suffixes on numbers). |
| D8 | **Desktop catalogs live in Go and are served to the UI.** `desktop/internal/i18n` embeds `locales/*.json` (`go:embed`), renders tray and notification text itself, and serves `GET /api/i18n` (the list: tag + `_meta`) and `GET /api/i18n/{lang}` (one catalog) behind the usual token and Origin checks. The UI loads `en` plus the chosen language at startup and when the language changes. | One copy of the desktop catalog, used by both the tray and the UI. The UI build needs no list of languages, so a new file is picked up with no code change, as asked. |
| D9 | **Desktop Go renderer is deliberately small**: `{{name}}` interpolation and plural suffixes, with the plural category from `golang.org/x/text/feature/plural` (BSD, `x/text`). It is used only for tray and OS notifications, whose keys stay simple; the UI uses full i18next. A Go test keeps tray keys inside that subset. | No second full i18n library in the binary. |
| D10 | **Desktop OS language** comes from the Go side, a new `desktop/internal/oslocale`. On Windows, `GetUserPreferredUILanguages` via the existing `golang.org/x/sys/windows`. On macOS, `CFLocaleCopyPreferredLanguages` through cgo, which the darwin build already uses. It is exposed as `os_languages` in the settings view. `navigator.languages` is the fallback. | The UI runs in a browser whose language may differ from the OS, and the tray needs the answer before any browser opens. |
| D11 | **Mobile: `src/core` stays free of text a person reads.** `describe()` moves out of core into `src/app/i18n/errors.ts`, which maps a `TppError.kind` or a relay `ErrorCode` to a key. `TppError.message` stays as English developer text for logs. Where one kind covers several situations a user must tell apart (`'invalid'`: "not a pairing code" vs "not a URL"), `TppError` gains an optional `reason` code next to `kind`. | Keeps the core / app boundary of the conventions: core has no React Native, and now no UI language either. |
| D12 | **Mobile catalogs are discovered by the bundler**: `require.context('./', false, /\.json$/)` in `src/locales/index.ts`, with Metro's `unstable_allowRequireContext` turned on in `metro.config.js`. Jest maps that one module to a Node `fs`-based twin. | Dropping `xx.json` into the folder is the whole change. (Fallback if `require.context` proves fragile: a generated, committed `index.ts`, with a CI check that it is current, like `make proto-check`.) |
| D13 | **Android native text follows the app's language.** JS calls a new `setAppLanguage(tag)` in `AppInfoModule`, which calls `AppCompatDelegate.setApplicationLocales` (`MainActivity` is already an `AppCompatActivity` through `ReactActivity`). Native strings (channel name and description, "Upload finished" / "Upload failed", the tile label) move to `res/values/strings.xml`. A Gradle task generates `res/values-<lang>/strings.xml` and `res/xml/locales_config.xml` from the mobile catalogs' `android.*` keys, the same way `notices.gradle` generates the notices. | Notifications appear when the JS bundle may not be running (the share flow). As a side effect, Android 13's per-app language setting lists the app's languages, generated from the same folder. Whether a change made *there* should flow back into the app is open question Q4. |
| D14 | **Dates, times and sizes go through `Intl`** with the app's language: `Intl.DateTimeFormat(lang)`, `Intl.NumberFormat(lang)` and an i18next formatter for bytes (`{{size, bytes}}`). The unit names (`kB`, `MB`) are catalog keys. | `toLocaleString()` follows the OS, not the app. |
| D15 | **Unexpected errors** keep a translated frame around untranslated detail: `errors.unexpected` = "Something went wrong: {{detail}}". `detail` is only ever a **local** error's text (an OS clipboard error, a file system error), never a relay's. | Review answer: unknown or unexpected errors may stay as they are. The frame is still in the user's language. |
| D16 | **Two kinds of check, with different weight.** (a) *English is complete*, which **blocks PRs**. Desktop UI and mobile get typed keys (react-i18next's `CustomTypeOptions` with `resources` typed from `en.json`), so `t('typo')` fails `npm run typecheck`. A Go test checks that every code the service and tray use is a key in `en.json`. (b) *Translations are complete*, which is **reported**: the new `i18n.yml` (§4.6) fails its own job on `master`, and nothing else waits for it. | A missing English key is a bug (the user sees a raw key). A missing Hungarian key is a gap, and English fills it until it is translated. |

## 4. Work items

### 4.0 Phase 0 — stop relay text reaching the screen (small, separate PR)

Independent of localization, and worth having first (review answer 7):

- Desktop: `failureText(err)` returns a `StatusError`'s own `Message` (without
  the wrapped error) before falling back to `relayError`. `Status`
  (`LastError`), `OnDisconnected` and `awaitOffer` call `failureText(err)`
  instead of `err.Error()`. The full error still goes to the log.
- Mobile: `describe()` gets a sentence for **every** `ErrorCode` (a total
  `switch`, with a generic "could not complete that request" default for
  `INVALID_ARGUMENT`, `INTERNAL`, `UNSPECIFIED` and codes newer than the
  build) and for the `'disconnected'` kind. It never returns a
  `ProtocolError`'s message.
- Tests: a Go table test of `failureText` over each `tppclient` sentinel and a
  raw `ProtocolError`. A Jest test that, for every `ErrorCode`, `describe()`
  neither contains the relay's message nor the string `ERROR_CODE`.

### 4.1 Phase 1 — foundations and the language choice

**Desktop**
- `desktop/internal/i18n`: embedded catalogs, `Languages()`, `Match(osTags)`,
  the small renderer (D9), and the `/api/i18n` handlers in `localui`.
- `desktop/internal/oslocale` (D10).
- `config.Settings.Language`, plus the effective-language rule (D6) in one
  function the tray and the settings view share.
- UI: `i18n.ts` (init, backend that fetches `/api/i18n/{lang}`, `bytes`
  formatter), a `useT` hook, typed keys from `en.json`.
- Setup wizard: a new first step, `"language"`, listing the catalogs and
  preselecting the match. Choosing one switches the UI at once. It is saved
  with the other answers and `setup_done` at the end, as the wizard already
  does.
- Settings panel: a "Language" picker. Saving re-renders the UI and the tray
  menu (the tray sets its labels once at start today, so it gains a relabel
  step for a language change).

**Mobile**
- `src/locales/en.json`, `src/locales/index.ts` (D12), `src/app/i18n/` (init,
  formatter, typed keys, the startup rule of D6).
- `platform/appInfo.*`: `preferredLanguages()` (from `LocaleList.getDefault()`)
  and `setAppLanguage(tag)` (D13).
- A `LanguageScreen` shown before `SetupScreen` when no language is saved.
  Settings gains a "Language" row.

**Checks**
- `scripts/i18n/check.mjs` and `scripts/i18n_test.sh` (§4.6).
- `i18n.yml`, as its own PR (CI workflows are a shared contract).

At the end of phase 1 only the language screens themselves are translated.
Everything else is still English, and nothing looks different to an existing
user.

### 4.2 Phase 2 — desktop UI strings

Move every literal in `desktop/ui/src` into `en.json` under its panel's
namespace. Replace `toLocale*` calls (D14) and hand-built plurals and
concatenations. Retake the docs screenshots only where layout changed.

### 4.3 Phase 3 — desktop service and tray to codes and parameters

- `localui`: `StatusError` gains `Key string` and `Params map[string]any`.
  `Errorf(status, err, format, …)` is replaced by
  `localui.Fail(status, err, key, params)`. Error bodies become
  `{ "error": { "key": "...", "params": { … } } }`. `SyncResult.Message`,
  `SkippedView.Message`, `Event.Message`, update states and the rest become the
  same `{key, params}` shape (`localui.Text`).
- `relayError` maps each `tppclient` sentinel to an `errors.relay.*` key.
- `internal/sensitive` validation errors become typed errors carrying a key
  and parameters, instead of English prefixed with `"sensitive: invalid filter: "`.
- Tray labels, tooltips and notifications render through `internal/i18n` in
  the effective language.
- A Go test walks the service's keys (the constants live in one file,
  `internal/service/keys.go`, so the test can list them) and the tray's keys,
  and checks each against `en.json`.
- `api.ts`: one `renderText(t, text)` used everywhere `err.message` /
  `res.message` is shown today.

### 4.4 Phase 4 — mobile strings, core errors and native text

- Screens and components to `t()`. `toLocale*` replaced.
- `describe()` to `src/app/i18n/errors.ts` (D11). Audit the 76 `TppError`
  sites: add a `reason` code only where the UI needs to tell cases apart.
- Native strings (D13): `strings.xml`, the Gradle generator, and
  `TransferService.kt` reading resources. JS-supplied notification text
  (`TransferModule`) is translated before it crosses the bridge.

### 4.5 Phase 5 — Hungarian

`desktop/internal/i18n/locales/hu.json` and `mobile/src/locales/hu.json`,
complete, so the check is green. Who writes the first draft is open question
Q1.

### 4.6 The translation check

`scripts/i18n/check.mjs <catalog-dir>…` (Node, no dependencies). For each
`<lang>.json` other than `en.json`, it compares against `en.json`:

| Check | Severity |
|---|---|
| Key in `en`, missing from `<lang>` | error |
| Key in `<lang>`, gone from `en` (stale) | error |
| `{{placeholder}}` set differs from `en`'s for the same key | error |
| A plural category `Intl.PluralRules(lang)` needs (`one`, `few`, …) is missing for a plural key | error |
| `_meta.name` missing, or invalid JSON | error |
| Empty string, or text identical to English (possibly untranslated) | warning, listed only |

Output: one GitHub annotation per problem, plus a job summary table with each
language's completeness, for example `hu — desktop 412/415 (99%), mobile 380/380`.
It exits non-zero if any error was found. `scripts/i18n_test.sh` runs it
against fixtures in `scripts/i18n/testdata/`, following the existing scripts.

`.github/workflows/i18n.yml`:
- `push` to `master` with a path filter on both locale folders and
  `scripts/i18n/**`, plus `workflow_dispatch`. It fails its own job, and the
  Beta jobs in `desktop.yml` and `android.yml` do not depend on it (review
  answer 1a).
- `pull_request`: open question Q2.
- `scripts.yml` also runs `i18n_test.sh`, like the other script tests.

### 4.7 Docs

- `docs/site`: a "Language" section in the desktop and Android settings
  pages, the first-run step in the getting-started pages, and a contributor
  page, "Translating TwoPlacePaste": copy `en.json`, translate, run the check,
  open a PR. Screenshots of the setup wizard and settings are retaken.
- `desktop/README.md`: the new `language` key of the settings file.
- `docs/internal/conventions.md`: "user-facing text goes through the catalog;
  the service sends keys, not sentences; never show a relay's message".
- `docs/internal/ROADMAP.md`: a localization entry, with its status kept up to date.

## 5. Tests

- Go: `internal/i18n` (match, fallback, plurals, interpolation),
  `internal/oslocale` (per-OS, behind build tags), config's effective-language
  rule (empty + `setup_done` → `en`; empty + not done → OS match; unknown tag →
  `en`), the key-coverage test (§4.3), and `localui` error bodies.
- UI and mobile: typecheck for keys (D16a), unit tests of the startup language
  rule and of `describe()` / `renderText`, and a test that a missing `hu` key
  renders the English text.
- The check script's own fixtures (§4.6).

## 6. Order of work

1. Phase 0, its own PR.
2. `i18n.yml` plus `scripts/i18n`, its own PR (shared contract).
3. Phase 1, one PR per app.
4. Phases 2 and 3 (desktop), then phase 4 (mobile). Each is one or more PRs
   by area; each leaves the app fully working in English.
5. Phase 5, Hungarian, last, so it is translated against final keys.

## 7. Open questions for review

- **Q1 — Hungarian draft.** Should the first `hu.json` be drafted with the
  code, for you to correct, or will you translate from `en.json` yourself?
- **Q2 — PRs.** Should `i18n.yml` also run on pull requests: blocking,
  informational (never fails), or not at all? Suggested: informational, so a PR
  that adds English keys shows what it leaves untranslated without being blocked.
- **Q3 — Release tags.** Should a `vX.Y.Z` release require complete
  translations, or ship with English fallback like the Beta does? Suggested:
  ship, and list the gaps in the release job summary.
- **Q4 — Android system language setting.** On Android 13+, the system
  settings will list the app's languages (D13). If the user changes it there,
  should the app adopt it? Suggested: yes. The app reads
  `AppCompatDelegate.getApplicationLocales()` at start, so there is one source
  of truth.
- **Q5 — Mobile "existing install".** D6 treats "in a group, or has parked
  groups" as set up. A user who installed an earlier build but never paired
  would see the language picker. Is that right?
- **Q6 — Local catalog overrides.** For your own translation tool later:
  should the desktop also load `<config dir>/locales/*.json` over the
  embedded ones, so a translator can test a file without a build? Suggested:
  not now. The format allows adding it later.
