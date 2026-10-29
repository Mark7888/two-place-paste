# Plan — Sensitive clipboard content and auto-upload filters (desktop)

**Status:** implemented on `claude/auto-upload-filters-wsfin5`, in the six commits of §6.
§8 records where the build differs from the plan below, and why; the plan itself is kept
as it was agreed.
**Touches:** `/desktop/**` (clipboard, config, service, localui, tray, UI),
`/docs/internal/SPECS.md` §7.2, `/docs/site/**`. No change to `/proto`, `/spec`, the
server, `pkg/tppclient` or mobile.

---

## 1. Goals

- A clipboard change can be **sensitive**. A sensitive change is **never uploaded by
  auto-watch**, and an upload the user starts by hand asks first: *"This is a protected
  entry. Push it anyway?"*
- **Detect secrets the OS already knows about**: password managers and other apps mark
  their copies with clipboard flags on Windows and pasteboard types on macOS. Detection
  is on by default, with a switch in Settings to turn it off.
- **Custom filters**: a Settings section shows how many filters are set; *Edit* opens a
  modal to manage them. A match on a text entry — of any size, including one that will
  travel as a blob — marks it sensitive.
- The flag is **local**. A pushed-anyway entry reaches other devices as an ordinary
  entry; nothing on the wire changes (§3, decision 3).

Mobile has no auto-upload and is out of scope.

## 2. What exists today

| Piece | Where | Relevant behaviour |
|---|---|---|
| Clipboard read | `desktop/internal/clipboard/clipboard_{windows,darwin}.go` | Returns `Content{ContentType, Filename, Body}`. Windows uses raw Win32 calls; macOS uses `pbpaste`/`osascript`, plus cgo for `changeCount` only. |
| Watcher | `desktop/internal/clipboard/watch.go` | Polls, suppresses self-writes, calls `OnChange` for user copies. |
| Auto-upload | `service.onClipboardChange` (`desktop/internal/service/service.go`) | Already skips copies over `AutoUploadLimit` and records a `SkippedView`. This is where the sensitive check goes. |
| Manual upload | `service.Sync` → `service.upload` | Called by the UI (`POST /api/sync`) and the tray (`syncFromTray` in `cmd/tppdesktop/main.go`). |
| Confirm-then-act pattern | `PrepareRevoke` / `ConfirmRevoke`, `plans` map with expiry | Reused for "push anyway". |
| Clipboard preview | `service.clipboardView` → `ClipboardView.Preview` | Shown on the Sync screen. |
| Tray notifications with buttons | `internal/tray/notify_{windows,darwin}.go` | Already used for the Upload/Download direction question. |
| Settings | `internal/config.Settings` (JSON, written via a `0600` temp file + rename in a `0700` directory), `localui.SettingsPatch` | New fields go here. |
| Modal | `desktop/ui/src/components/Modal.tsx` | Used for the filter editor and the confirm prompt. |

## 3. Decisions

These were agreed before the plan was written.

1. **OS detection is on by default, with a toggle** (`Settings → Sensitive content →
   Detect password managers`).
2. **Tray push of a sensitive entry asks with a notification** carrying a *Push anyway*
   button, the same mechanism as the direction question. With notifications off, a
   *Push protected entry anyway* menu item appears instead, as the direction items do.
3. **The flag stays local for now.** No crypto-frame or proto change. Carrying it to
   receivers (hide previews, skip auto-apply) is a possible follow-up and would be its
   own spec + vectors PR.
4. **The Sync screen masks a sensitive clipboard**: *"Sensitive text · 24 characters ·
   Password manager"*, with an eye button that reveals the preview on demand (like a
   password field). The reveal is UI-only; the masked text is fetched only when asked.
5. **Filter editor**: a modal with a *Custom* tab (add, edit, delete, switch on/off)
   and a *Built-in* tab (switch on/off only). Each tab has *Turn all on* / *Turn all
   off* buttons that flip that tab's switches; they are not a separate setting.
6. **Filter kinds**: contains, starts with, ends with, regex, **exact match stored
   hashed**, **built-in detectors**, and a **filename target** for copied files.
7. **Per-filter options**: case-insensitive toggle, enabled switch, optional label, and a
   live *Test* box in the modal (text pasted there is never saved or logged).
8. **Starts with / ends with compare against the whole text, trimmed** of leading and
   trailing whitespace. Per-line matching is available through a regex with `(?m)`.
9. **A skipped sensitive copy is quiet**: no tray icon state and no notification. The
   Sync screen shows a small note. Password copies are frequent, and a tray that flashes
   for each one teaches the user to ignore it.

Decisions made in this plan (not yet confirmed — flag in review if you disagree):

10. **Sensitive overrides size**: a sensitive entry under the size limit is still not
    auto-uploaded; one over it is reported as sensitive, not as too big.
11. **Filters apply to text and filenames only.** Images and file bodies are not
    scanned. OS flags apply to every kind.
12. **Matching runs in Go only.** The UI never evaluates a filter: Go's RE2 and
    JavaScript regexes differ, and the test box must answer exactly as the watcher will.
13. **Transient and auto-generated markers count as sensitive** on macOS (§4.1). The app
    that copied asked clipboard tools not to keep the content; uploading it to a relay
    is keeping it.

## 4. Work items

### 4.1 OS detection — `desktop/internal/clipboard`

Add a field to `Content`:

```go
// Concealed names why the copying application marked this content as not for
// clipboard tools (a password manager, most often), or is empty. It is read in
// the same clipboard session as the payload, so it describes these bytes and
// not the next copy. It is not part of Digest and never leaves this device.
Concealed string
```

`Digest` stays as it is: the same bytes re-copied without the marker are still the same
content to the watcher's change detection.

**Windows** (`clipboard_windows.go`), inside the `Read` session that already holds the
clipboard open, check registered formats with `RegisterClipboardFormatW` +
`IsClipboardFormatAvailable`:

| Format | Rule | Reason shown |
|---|---|---|
| `ExcludeClipboardContentFromMonitorProcessing` | present | "Marked private by the copying app" |
| `CanIncludeInClipboardHistory` | DWORD value `0` | same |
| `CanUploadToCloudClipboard` | DWORD value `0` | same |
| `Clipboard Viewer Ignore` | present (older KeePass and similar tools) | same |

These are Microsoft's documented clipboard-privacy formats; KeePass, KeePassXC,
1Password and others set one or more of them. The DWORD formats are read through the
existing `readBytes` helper, bounded to 4 bytes.

**macOS**: read `[[NSPasteboard generalPasteboard] types]` through cgo, next to the
existing `changeCount` helper (`changecount_darwin.go` → rename to
`pasteboard_darwin.go`). Markers, from the nspasteboard.org convention and known
managers:

- `org.nspasteboard.ConcealedType`
- `org.nspasteboard.TransientType`, `org.nspasteboard.AutoGeneratedType` (decision 13)
- `com.agilebits.onepassword`
- `de.petermaurer.TransientPasteboardType`, `com.typeit4me.clipping`,
  `Pasteboard generator type`

The type list and the content come from separate calls (the content still goes through
`pbpaste`/`osascript`), so `Read` brackets them with `changeCount`: read the count, the
types, the content, and the count again; if it moved, read again (at most twice, then
return the content without a marker and log at Debug). The `!cgo` build has no AppKit
access and reports no marker; release builds use cgo already, and the docs say so.

Tests: the marker table is a pure function (`concealedFromFormats(names []string,
dwords map[string]uint32) string`, `concealedFromTypes([]string) string`) with
table-driven tests that run on every OS. A Windows test writes an
`ExcludeClipboardContentFromMonitorProcessing` format alongside text and reads it back
(`clipboard_windows_test.go` already writes the real clipboard).

Manual test matrix before merge, recorded in the PR: KeePassXC, Bitwarden, 1Password on
both OSes; KeePass on Windows; macOS Passwords app; a plain copy from a text editor
(must not be marked).

### 4.2 Filters — new package `desktop/internal/sensitive`

```go
// Classifier decides whether clipboard content is sensitive. It is built from
// the settings and rebuilt when they change; Classify is safe for concurrent use.
type Classifier struct { ... }

type Verdict struct {
	Sensitive bool
	Source    Source // SourceOS, SourceFilter, SourceDetector
	Reason    string // shown to the user: a label, a kind, or the OS reason; never the pattern
	FilterID  string // which filter matched, for the UI
}

func New(cfg Config) (*Classifier, error)
func (c *Classifier) Classify(ctx context.Context, content clipboard.Content) Verdict
```

Order: OS marker (if detection is on) → enabled built-in detectors → enabled custom
filters. The first match wins.

**Filter model** (also the settings JSON):

```go
type Filter struct {
	ID              string `json:"id"`               // random, assigned on save
	Kind            Kind   `json:"kind"`             // contains | prefix | suffix | regex | exact
	Target          Target `json:"target"`           // text | filename
	Value           string `json:"value,omitempty"`  // empty for exact
	Hash            string `json:"hash,omitempty"`   // exact only
	CaseInsensitive bool   `json:"case_insensitive"` // default true for contains/prefix/suffix
	Enabled         bool   `json:"enabled"`
	Label           string `json:"label,omitempty"`
}
```

Matching rules:

- **contains / prefix / suffix**: `strings.Contains` / `HasPrefix` / `HasSuffix` on the
  text trimmed with `strings.TrimSpace` (decision 8). Case-insensitive uses a
  pre-compiled `(?i)` + `regexp.QuoteMeta` regex for `contains`, so a 50 MB text is not
  copied by `ToLower`; prefix/suffix compare only the needed span with `strings.EqualFold`.
- **regex**: Go `regexp` (RE2, linear time, so a hostile pattern cannot hang the
  watcher). Compiled on save; an invalid pattern is refused with the compiler's message.
  Pattern length capped at 1 KiB.
- **exact (hashed)**: the user types the secret once; the service stores
  `argon2id(installSalt, TrimSpace(text))` and never returns or logs the value. One
  random 16-byte install-wide salt in the settings means one KDF run per clipboard
  change no matter how many exact filters exist. Only text of at most 1 KiB is hashed
  (secrets are short; a long text cannot equal one). Parameters: OWASP's argon2id
  baseline (19 MiB, t=2, p=1), roughly 20–40 ms per change. `golang.org/x/crypto` is
  already in the module graph (indirect); it becomes a direct dependency (BSD-3,
  allowed). A plain SHA-256 would let anyone who reads the settings file brute-force
  short passwords cheaply; that is why it is a KDF.
- **filename target**: the same kinds applied to `Content.Filename` (e.g. suffix `.pem`,
  exact name `id_rsa`, contains `.env`). Exact-hashed is text-only.

**Built-in detectors**, each with its own switch on the modal's *Built-in* tab (§4.7),
fixed in code and not editable, all **off** by default except private keys (zero false
positives):

| Detector | Rule |
|---|---|
| Private keys | `-----BEGIN [A-Z ]*PRIVATE KEY-----` (PEM, OpenSSH, PGP private blocks) |
| JSON Web Tokens | `eyJ[A-Za-z0-9_-]+\.eyJ[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+` |
| Cloud and API tokens | AWS access key ids (`AKIA…`/`ASIA…`), GitHub (`ghp_`, `gho_`, `ghs_`, `github_pat_`), Slack (`xox[abprs]-`), Stripe (`sk_live_`), Anthropic (`sk-ant-`), OpenAI (`sk-proj-`), Google API keys (`AIza…`) |
| Payment cards | 13–19 digits with optional spaces/dashes **and** a valid Luhn checksum |
| IBAN | country code + check digits + BBAN, **and** a valid mod-97 checksum |

Detectors scan text of any size; card and IBAN look only at runs that already look like
numbers before checksumming.

**Large text**: the clipboard body is already in memory when `OnChange` fires, so
matching costs CPU, not memory. Every rule above is linear. A benchmark test pins the
cost of a full default configuration on 50 MB of text.

**Logging**: a match is logged as `"clipboard change is sensitive", "source", ...,
"filter_id", ...` — never the pattern, the label (a label may be the secret), the
reason text, or the content.

### 4.3 Settings — `desktop/internal/config`

```go
// DetectSensitive marks content that a password manager or other app flagged
// as private. Absent means on.
DetectSensitive *bool `json:"detect_sensitive,omitempty"`

// Detectors are the built-in detectors the user turned on, by name.
// Absent means the defaults (private keys only).
Detectors *[]string `json:"detectors,omitempty"`

// Filters are the user's own rules.
Filters []sensitive.Filter `json:"filters,omitempty"`

// FilterSalt salts exact-match hashes. Created on first use.
FilterSalt string `json:"filter_salt,omitempty"`
```

`Validate` checks kinds, targets, regex compilation, sizes (≤ 200 filters, value ≤ 1 KiB)
and that an exact filter has a hash and no value. The settings file stays private to the
user (it already is written in a `0700` directory); the docs note that filter values
other than exact matches are stored in plain text there, and recommend exact match for
literal secrets.

### 4.4 Service — `desktop/internal/service`

- Hold a `*sensitive.Classifier` under `s.mu`; rebuild it in `UpdateSettings`.
- **`onClipboardChange`**: classify first. If sensitive: log (§4.2), set a new
  `s.sensitiveNote` (not `skipped`, so the tray icon is untouched — decision 9), publish
  `EventClipboard`, return. The watcher's `LastChange` still moves: the sensitive copy
  *is* the newest local content, so a later manual *Sync* still picks upload and then
  asks.
- **`upload`** (manual): classify the content it read. If sensitive and the request
  carries no confirmation, store a plan `{digest, expires: now+2m}` in the existing
  plans map and return `409` with code `sensitive`, the plan id and the reason. A
  confirmed request (`plan_id`) re-reads the clipboard and uploads only if the digest
  still matches; otherwise it classifies again (and may ask again). The digest never
  leaves the service: the UI and tray hold only the random plan id.
- **`clipboardView`**: add `Sensitive bool`, `SensitiveReason string`; when sensitive,
  `Preview` is empty and `Chars` is set instead.
- New `RevealClipboard(ctx)` returns the preview text of the current clipboard, used only
  by the eye button.
- New `TestFilters(ctx, text, filename)` returns which filters and detectors match
  (ids + reasons). Input is bounded (1 MiB) and never logged or stored.

### 4.5 Local API — `desktop/internal/localui`

| Route | Change |
|---|---|
| `POST /api/sync` | Body gains `plan_id` (optional). A sensitive upload without one returns `409 {code: "sensitive", plan_id, reason}`. |
| `GET /api/status` | `clipboard.sensitive`, `clipboard.sensitive_reason`, `clipboard.chars`; `sensitive_note` (last skipped sensitive copy: reason + time, no content). |
| `POST /api/clipboard/reveal` | Returns the masked preview. `POST`, not `GET`, so it goes through the scripted-origin guard and never sits in a browser cache. |
| `GET /api/settings` / `POST /api/settings` | `detect_sensitive`, `detectors`, `filters` (the full list replaces the old one; an exact filter is sent as `{id}` to keep its hash or `{kind:"exact", value}` to set a new one, and is always returned without a value). |
| `POST /api/filters/test` | `{text, filename, filters?}` → matches. Accepts an unsaved filter list so the modal can test edits before saving. |

All routes use the existing `guard(originScripted, tokenRequired, …)`. Errors on invalid
filters name the filter by position and say what is wrong, never echo the value.

### 4.6 Tray — `desktop/internal/tray`, `cmd/tppdesktop/main.go`

- `syncFromTray` handles the `sensitive` error: it keeps the plan id in memory and calls
  a new `tray.AskSensitive(reason)`, which posts a notification *"Protected entry —
  copied from a password manager. Push it anyway?"* with a **Push anyway** action. The
  action runs a new `Options.PushSensitive` callback that retries the sync with the plan
  id. The reason text is the generic reason or the filter's label — never the content.
- When notifications are unavailable, a **Push protected entry anyway** menu item
  appears (as the Upload/Download items do) until the plan expires or the clipboard
  changes.
- Windows toast and macOS category registration get one new action id alongside the
  existing upload/download ones; `notify_windows_test.go` covers the new action list.

### 4.7 UI — `desktop/ui`

- **Sync panel**: a sensitive clipboard shows a lock badge, the masked line, and an eye
  button (`aria-label="Show"`/`"Hide"`) that calls `/api/clipboard/reveal`; the revealed
  text is dropped from state on hide and on the next clipboard event. The quiet
  "Not uploaded: sensitive (reason)" note sits under the clipboard card.
- **Upload / Sync** buttons: on a `409 sensitive`, open a confirm `Modal`: *"This is a
  protected entry (reason). Push it anyway?"* — **Cancel** (default focus) / **Push
  anyway**. Confirm resends with the plan id.
- **Settings → Sensitive content** section:
  - switch *Detect password managers and private copies*;
  - *Filters — N custom (M on) · K of 5 built-in on* with an **Edit** button.
- **Filter editor modal**, with two tabs. Both tabs edit one draft, saved together by a
  single *Save*; unsaved changes ask before closing.
  - **Custom** tab: one row per filter — enabled switch, kind select, target select
    (text/filename), value input (password-style for exact, showing "set" once saved),
    case-insensitive toggle (hidden for regex, which uses `(?i)`, and for exact), label,
    delete. An *Add filter* button, and validation errors on their rows.
  - **Built-in** tab: one read-only row per detector — name, a one-line description
    of what it catches, and an enabled switch. Detectors cannot be added, deleted or
    edited.
  - Each tab's header has **Turn all on** and **Turn all off** buttons. They are not a
    setting of their own: they flip every switch on that tab in the draft, the same as
    clicking each one, and a later single switch overrides them. A button is disabled
    when every switch on the tab is already in that state.
  - A **Test** box under the tabs, shared by both: paste text (and optionally a
    filename) and the matching rows highlight, debounced through `/api/filters/test`
    with the unsaved draft. A tab whose rows match shows a dot on its tab label, so a
    match on the hidden tab is not missed.
- `api.ts` types mirror the Go types; `npm run typecheck` covers them (the UI has no
  test runner today; behaviour is tested on the Go side of each route).

### 4.8 Docs

- `docs/internal/SPECS.md` §7.2: a *Sensitive content* bullet stating the rules
  (detection default on, filters local, sensitive never auto-uploaded, manual asks).
- `docs/site/src/content/docs/guides/settings.mdx`: the new section, every detector, the
  filter kinds with examples, the plain-text-storage note.
- `guides/sync.mdx`: the masked preview, the eye button, the confirm prompt.
- `guides/tray.mdx`: the *Push anyway* notification and menu item.
- `security.md` / `faq.md`: which password managers are known to be detected, that the
  `!cgo` macOS build cannot detect, and that the flag does not travel to other devices.
- Retake the Sync and Settings screenshots; add one of the filter editor.
- `docs/internal/ROADMAP.md`: add the item and mark it when it lands.

## 5. Tests

- `clipboard`: marker tables (all OSes); a Windows round-trip with the exclusion format.
- `sensitive`: table-driven per kind × target × case; trimming; regex refusal; exact hash
  round-trip and that `Value` is never kept; each detector with true and false samples
  (valid vs. invalid Luhn/mod-97); first-match order; a benchmark on 50 MB.
- `config`: validation, defaults when fields are absent, salt created once.
- `service`: auto-upload skipped for OS-marked and filter-matched content (fake
  clipboard + fake relay, as `autolimit_test.go` does); manual upload returns the plan;
  confirmed upload succeeds; a changed clipboard invalidates the plan; an expired plan
  is refused; the log output of a sensitive skip contains no content, value or label
  (captured `slog` handler).
- `localui`: route guards for the new routes (`server_test.go` pattern); `409` shape.
- `tray`: notification action list.

## 6. Order of work

One PR, in this commit order, each passing `make test lint` and `npm run typecheck`:

1. `sensitive` package + config fields (no behaviour change yet).
2. OS markers in `clipboard` (Windows, then macOS cgo).
3. Service: auto-upload skip, manual-upload plan, clipboard view, reveal, test endpoint;
   localui routes.
4. Tray notification and menu item.
5. UI: Sync panel, confirm modal, Settings section, filter editor.
6. Docs site, SPEC §7.2, screenshots, roadmap.

## 7. Follow-ups (not in this PR)

- Carry the flag inside the encrypted frame so receivers can hide previews and skip
  auto-apply (spec + vectors + Go + TS).
- A *source app* filter (foreground app at copy time), if wanted later.
- Mobile, if it ever gains auto-upload.

## 8. As built

Everything in §4 and §5 was built. These are the places the code differs from the text
above, and why.

**OS detection (§4.1)**

- The reason the user sees is *"Marked private by the app that copied it"*, and macOS's
  transient and auto-generated types read as *"Marked temporary by the app that copied
  it"*, so the two are told apart.
- macOS, when the change count moves during a read twice in a row: the marker is taken
  from *both* type lists rather than dropped. A race can then mark a plain copy as
  private (it costs a question) but can never unmark a password (it would cost a leak).
- Windows reads a DWORD format through a dedicated `readDWORD`, which copies four bytes
  and no more, instead of the general `readBytes`.
- **Not done here:** the manual test matrix with real password managers, and compiling
  the macOS cgo code (`pasteboard_darwin.go`, `notify_darwin.m`), which this Linux
  environment cannot do. Both need a Mac and a Windows machine before merge.

**Classifier (§4.2)**

- `Classify(content)` takes no context: it does no I/O. `Verdict.Rule` replaces
  `FilterID` and also names a detector. `Matches` returns every match, for the test box.
- The first benchmark ran 18 s on 50 MB of text. Three rules were rewritten to bring a
  full configuration (every detector, four filters) to about 0.7 s:
  - API tokens and JWTs jump between occurrences of each literal prefix and run an
    anchored pattern on a short window there, instead of one alternation tried at every
    byte. The token list also gained GitHub's `ghu_`/`ghr_`, Stripe's `rk_live_`,
    GitLab, npm and SendGrid.
  - Cards and IBANs are byte scanners, not regular expressions with counted repetition.
    The IBAN scanner tries every group boundary as the end, so a trailing capitalised
    word does not hide the number before it.
  - Case-insensitive *contains* lower-cases the text once, shared by every such filter,
    instead of a `(?i)` regular expression per filter. *Starts with* uses an anchored
    `(?i)` pattern; *ends with* runs one on the last `4 × len(value)` bytes.
- The log line is `"clipboard change not uploaded automatically: sensitive"` with
  `source` and `rule`.

**Service and API (§4.4, §4.5)**

- Push plans have their own map, `pushPlans`, rather than sharing the revocation plans'.
- A classification is cached by content digest and classifier, so the status refresh
  and the watcher do not hash an exact filter twice for one copy.
- A sensitive skip publishes `EventSensitive` (not `EventClipboard`) and clears an
  upload `Skipped` notice, which was about the copy it replaced.
- `POST /api/filters/test` also takes the draft's detectors, so the Built-in tab's
  switches are tested before saving. New filters get their id in the editor, so a match
  can point at an unsaved row.
- A full filter list (200 × 1 KiB) and a 1 MiB test sample exceed the API's 64 KiB body
  limit; `POST /api/settings` and `POST /api/filters/test` have limits of their own.

**Tray (§4.6)**

- `AskSensitive(planID, reason)` keeps the plan id in the tray. The question is withdrawn
  after two minutes, the plan's life, rather than when the clipboard changes; confirming
  after a change gets a new question about the new copy, which is the same outcome.
- The notification reads *"This is a protected entry"* / *"<reason>. Push it to your
  other devices anyway?"*.

**UI (§4.7)**

- A saved exact filter shows the placeholder *"Saved — type to replace"* rather than
  "set". The kind is labelled *"Exact match"*; *"only a hash is kept"* is in its
  placeholder, because the long label was cut off in the select.
- Only the rows scroll inside the editor, so the tabs and the test box stay in view.
- With unsaved changes the editor cannot be dismissed with Escape or the backdrop;
  Cancel asks *"Discard your changes?"*.
- `Modal` now focuses an element marked `data-autofocus` when it opens: React's
  `autoFocus` ran while the dialog was still closed, so the close button took the focus
  and the "push anyway?" dialog did not default to Cancel.
- The UI has no test runner; a Playwright script against the built UI with a mocked API
  checked the masked clipboard, reveal, push anyway, both tabs, turn all on/off, the
  discard question and what Save sends. It is not committed.

**Docs (§4.8)**

- The detectors and filter kinds have a page of their own,
  `guides/sensitive-content.mdx`, which Settings, Sync, Tray, Troubleshooting, Security
  and the FAQ link to. The `!cgo` note is in `desktop/README.md`, where a developer
  building without cgo will read it.
- `desktop-sync.webp` is unchanged (its state did not change); the masked clipboard and
  the protected-entry dialog have new screenshots, and Settings was retaken.

## 9. After review

- **Store (built in parallel, PR #43–#48).** Saving the clipboard to the store asks
  first, like Upload, with a plan scoped to the store: an Upload answer does not let a
  copy into the store, nor the reverse. The plan survives the store's duplicate
  question and is used up when the save succeeds. Saving a history entry or a dropped
  file does not ask: the entry is already the group's, and a file is not the clipboard.
- **Pause auto-sync.** One switch in the Sync tab and the tray holds both automatic
  directions for five minutes, for copies nothing can detect — a password from a
  browser extension, which can only write plain text.
- **Still not covered, by design for now:** the flag stays local, so History and Store
  previews of an entry pushed anyway show it like any other; and Android, which has no
  auto-upload, does not check its own sensitive-clip marker
  (`ClipDescription.EXTRA_IS_SENSITIVE`) on Upload or Save clipboard.
