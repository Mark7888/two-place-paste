// The client half of the localhost API (SPEC §7.2).
//
// The token arrives once, in the query string of the URL the tray opened, and
// is captured here on the first import: it is held in a module variable, put
// in a header on every request, and removed from the address bar immediately.
// It is never written to localStorage, sessionStorage or a cookie — those
// outlive the launch that the token is scoped to, and a token in the URL bar
// ends up in history, in a bookmark and in a screenshot.

const TOKEN_HEADER = "X-TPP-Token";

function captureToken(): string {
  const params = new URLSearchParams(window.location.search);
  const token = params.get("token") ?? "";
  if (token) {
    // The fragment stays: the tray opens #updates to land on that section.
    window.history.replaceState({}, "", window.location.pathname + window.location.hash);
  }
  return token;
}

const token = captureToken();

export type Direction = "auto" | "upload" | "download";

export interface ClipboardView {
  available: boolean;
  empty: boolean;
  kind?: string;
  filename?: string;
  bytes: number;
  preview?: string;
  changed_at?: string;
  error?: string;
  /** Set for a password manager's copy or a detector or filter match. The
   *  preview is then withheld; revealClipboard fetches it on request. */
  sensitive?: boolean;
  sensitive_reason?: string;
  /** The length of a masked text, in characters. */
  chars?: number;
}

/** SensitiveNote is the last copy auto-watch kept here because it was sensitive. */
export interface SensitiveNote {
  reason: string;
  at: string;
}

export interface EntryView {
  id: string;
  epoch: number;
  size: number;
  created_at: string;
  expires_at: string;
  /** From the entry's sealed header, read without fetching it. Absent when
   *  the type is unknown: no header, or one this device cannot open. */
  kind?: "text" | "image" | "file";
  content_type?: string;
  filename?: string;
  /** The plaintext size, when the type is known. */
  bytes?: number;
  /** A format the service can draw, within the auto-download limit;
   *  no_preview says why not. */
  previewable: boolean;
  no_preview?: string;
  /** How a previewable entry is drawn. */
  render?: Render;
  /** The store already holds this entry's content. */
  saved?: boolean;
}

/**
 * Render is how the service says an entry is drawn. Each value has a
 * component in src/preview; `kind` is separate, and says what the entry
 * becomes when copied.
 */
export type Render = "text" | "image" | "audio" | "video";

/** SkippedView is an automatic transfer the user's size limit held back. */
export interface SkippedView {
  direction: "upload" | "download";
  entry_id?: string;
  bytes: number;
  message: string;
  at: string;
}

export interface Status {
  /**
   * The latest automatic transfer held back by a size limit. The service keeps
   * it until it is dismissed or a newer entry lands on either side.
   */
  skipped?: SkippedView;
  sensitive_note?: SensitiveNote;
  in_group: boolean;
  connected: boolean;
  epoch: number;
  device_id: string;
  device_name: string;
  server_url: string;
  keystore: string;
  revoked: boolean;
  /** Set with `revoked` when the server admin deleted the group. */
  group_deleted: boolean;
  /** The group's display name, as the server admin set it. */
  group_name?: string;
  last_sync?: string;
  last_error?: string;
  clipboard: ClipboardView;
  latest?: EntryView;
  direction_known: boolean;
  suggested_direction: Direction;
}

export interface SyncResult {
  direction: Direction;
  changed: boolean;
  message: string;
  entry?: EntryView;
}

export interface DeviceView {
  id: string;
  name: string;
  created_at: string;
  last_seen?: string;
  this: boolean;
}

export interface RosterView {
  devices: DeviceView[];
  epoch: number;
}

export interface RevokePlan {
  id: string;
  target: DeviceView;
  remaining: DeviceView[];
  epoch: number;
  expires_at: string;
}

// EntryPreview is one history entry rendered for a look rather than a paste.
// Fetching it decrypts that entry on this machine; nothing about it is stored,
// and an entry from before a rekey has none at all.
export interface EntryPreview {
  id: string;
  epoch: number;
  content_type: string;
  filename?: string;
  bytes: number;
  kind: "text" | "image" | "file";
  render: Render;
  /** The format's name for the screen, from the service's preview table. */
  format: string;
  text?: string;
  truncated?: boolean;
  image_data_url?: string;
  image_too_large?: boolean;
}

export interface HistoryPage {
  entries: EntryView[];
  next_before?: string;
}

export interface Invite {
  /** payload is the bare code; link is what a QR should carry (SPEC §6a). */
  payload: string;
  link: string;
  expires_at: string;
}

// Offer is a pairing code this device is showing while it waits for a member
// of some group to accept it. The code is both the QR contents and the
// copyable string.
export interface Offer {
  /** code is the bare code; link is what a QR should carry (SPEC §6a). */
  code: string;
  link: string;
  expires_at: string;
}

// OfferPlan is a scanned or pasted offer waiting for the user's confirmation.
// There is no endpoint that takes the code itself: admitting a device hands it
// the group key, so the plan id the dialog produces is the only way in.
export interface OfferPlan {
  id: string;
  device_name: string;
  fingerprint: string;
  expires_at: string;
}

export interface SettingsView {
  port: number;
  listen_port: number;
  auto_watch: boolean;
  auto_apply: boolean;
  /** Bytes; 0 is no limit. Manual transfers are never limited. */
  auto_upload_max_bytes: number;
  auto_download_max_bytes: number;
  autostart: boolean;
  autostart_supported: boolean;
  clipboard_supported: boolean;
  device_name: string;
  update_channel: UpdateChannel;
  auto_update: boolean;
  nightly_commit: string;
  restart_required: boolean;
  detect_sensitive: boolean;
  detectors: DetectorView[];
  filters: FilterView[];
}

/** DetectorView is a built-in detector: fixed, only switched on or off. */
export interface DetectorView {
  name: string;
  title: string;
  description: string;
  enabled: boolean;
}

export type FilterKind = "contains" | "prefix" | "suffix" | "regex" | "exact";
export type FilterTarget = "text" | "filename";

/**
 * FilterView is one of the user's filters. An exact filter's value is never
 * sent back by the service; sending one without a value keeps the saved hash.
 */
export interface FilterView {
  id: string;
  kind: FilterKind;
  target: FilterTarget;
  value: string;
  case_insensitive: boolean;
  enabled: boolean;
  label: string;
}

export interface FilterTest {
  text: string;
  filename: string;
  detectors?: string[];
  filters?: FilterView[];
}

export interface FilterMatch {
  source: "os" | "detector" | "filter";
  /** A detector's name or a filter's id. */
  rule: string;
  /** A sentence: "Matches custom filter “VPN”". */
  reason: string;
  /** The detector's title or the filter's label; absent for an unlabelled filter. */
  name?: string;
}

export interface SettingsPatch {
  port?: number;
  auto_watch?: boolean;
  auto_apply?: boolean;
  auto_upload_max_bytes?: number;
  auto_download_max_bytes?: number;
  autostart?: boolean;
  device_name?: string;
  update_channel?: UpdateChannel;
  auto_update?: boolean;
  nightly_commit?: string;
  detect_sensitive?: boolean;
  detectors?: string[];
  filters?: FilterView[];
}

export type UpdateChannel = "stable" | "beta" | "nightly";

/** BuildView names one build of the desktop app. */
export interface BuildView {
  version: string;
  channel: string;
  commit?: string;
  /** Seconds since 2025-01-01 when CI built it; a larger stamp is newer. */
  stamp: number;
}

/** BuildOrigin is what the Nightly confirmation shows about a commit. */
export interface BuildOrigin {
  repository: string;
  branch?: string;
  fork: boolean;
  pull_request?: number;
  pull_request_title?: string;
  commit_message?: string;
  author?: string;
  url: string;
  run_url?: string;
  changes_workflows: boolean;
}

export interface UpdateCandidate extends BuildView {
  older: boolean;
  signed: boolean;
  needs_confirmation: boolean;
  building?: boolean;
  origin?: BuildOrigin;
}

/** TokenView says whether a GitHub token is stored, never what it is. */
export interface TokenView {
  set: boolean;
  source?: "keystore" | "environment";
  invalid?: boolean;
  /** When the token stops working. Unset until GitHub has answered a call made with it. */
  expires_at?: string;
  /** The token was made with no expiration. */
  no_expiry?: boolean;
}

export type UpdateState =
  | "idle"
  | "checking"
  | "up_to_date"
  | "available"
  | "building"
  | "downloading"
  | "installing"
  | "restarting"
  | "error";

/** DownloadProgress is how far an update download has got; `total` is 0 when unknown. */
export interface DownloadProgress {
  done: number;
  total: number;
}

export interface UpdateView {
  current: BuildView;
  channel: UpdateChannel;
  auto_update: boolean;
  nightly_commit?: string;
  state: UpdateState;
  message?: string;
  /** The GitHub Actions run behind a failed Nightly build, when `state` is "error" for that. */
  failed_run?: string;
  /** The download in progress while `state` is "downloading", once bytes arrive. */
  progress?: DownloadProgress;
  available?: UpdateCandidate;
  last_checked?: string;
  can_install: boolean;
  cannot_install?: string;
  previous?: BuildView;
  token: TokenView;
}

export interface InstallRequest {
  /** Accepts an unsigned Nightly build after its origin was shown. */
  confirm?: boolean;
  /** Accepts a build older than the running one. */
  allow_older?: boolean;
}

export interface ServiceEvent {
  kind: string;
  at: string;
  message?: string;
  epoch?: number;
  device_id?: string;
  name?: string;
  /** The entry an "evicted" or "progress" event is about. */
  entry_id?: string;
  /** "progress" only: which way, and ciphertext bytes so far and in all. */
  direction?: "upload" | "download";
  done?: number;
  total?: number;
}

// The store (SPEC §6b): items the group keeps on purpose, beside its 24-hour
// history. The service opens them; the relay sees none of this.

export type StoreSort = "newest" | "oldest" | "name" | "name_desc" | "largest" | "smallest";

export interface StoreDetails {
  name: string;
  description: string;
  tags: string[];
}

export interface StoreItemView extends StoreDetails {
  id: string;
  saved_at: string;
  kind: "text" | "image" | "file";
  content_type: string;
  filename?: string;
  /** The plaintext size. */
  bytes: number;
  /** As for a history entry: whether a preview is offered, why not, and
   *  how it is drawn. */
  previewable: boolean;
  no_preview?: string;
  render?: Render;
}

export interface StoreLimitsView {
  max_bytes: number;
  used_bytes: number;
  /** The largest item that still fits. */
  available_bytes: number;
  max_items: number;
  used_items: number;
  max_item_bytes: number;
}

export interface StoreView {
  /** False while the server admin has the store off for this group. */
  enabled: boolean;
  limits?: StoreLimitsView;
  items: StoreItemView[];
  /** Every item, matching the query or not. */
  total: number;
  tags: { tag: string; count: number }[];
  unreadable: { id: string; saved_at: string; reason: string }[];
}

/** What a save starts from: the clipboard or a history entry. A file goes
 *  through api.uploadToStore instead. */
export type StoreSource = { source: "clipboard" } | { source: "entry"; entry_id: string };

/** The limits /spec/crypto.md §7a.5 puts on what the user types, in bytes of
 *  UTF-8. */
export const storeLimits = { name: 200, description: 800, tags: 16, tag: 32 } as const;

export class ApiError extends Error {
  readonly status: number;
  /** A refused save's duplicates, when the store already holds its content. */
  readonly duplicates?: StoreItemView[];
  /** The rest of the error body's strings: code, and what goes with it. */
  readonly details: Record<string, string>;

  constructor(
    status: number,
    message: string,
    duplicates?: StoreItemView[],
    details: Record<string, string> = {},
  ) {
    super(message);
    this.status = status;
    this.details = details;
    this.name = "ApiError";
    this.duplicates = duplicates;
  }
}

// The token is good for one launch, and the service is on this machine. When
// the service quits, every request fails outright; when it restarts (an
// update, a rollback, a quit and reopen) it answers this page's token with
// 401. Either way nothing on the page will work again, even once a service is
// back: only the tray holds the new token. The first such answer marks the
// session lost for good, and the app says to reopen it.
//
// It does not matter which of these happened, so nothing tries to tell them
// apart.
let sessionLost = false;
const lostListeners = new Set<() => void>();

function markLost(): void {
  if (sessionLost) return;
  sessionLost = true;
  for (const fn of lostListeners) fn();
}

/** isSessionLost reports whether the service has stopped accepting this page's token. */
export function isSessionLost(): boolean {
  return sessionLost;
}

/** onSessionLost calls fn once the service stops accepting this page's token,
 *  and returns a function that stops listening. */
export function onSessionLost(fn: () => void): () => void {
  lostListeners.add(fn);
  return () => {
    lostListeners.delete(fn);
  };
}

/**
 * call is fetch, watching for the two answers that mean this page's session
 * is over: no answer at all (the service quit), and 401 (it restarted).
 */
async function call(path: string, init: RequestInit): Promise<Response> {
  let res: Response;
  try {
    res = await fetch(path, init);
  } catch (err) {
    // A request this page cancelled is not the service going away.
    if (!(err instanceof DOMException && err.name === "AbortError")) markLost();
    throw err;
  }
  if (res.status === 401) markLost();
  return res;
}

/** ProtectedEntry is a manual upload the service held back as sensitive. */
export interface ProtectedEntry {
  planId: string;
  reason: string;
}

/**
 * protectedEntry recognises the service's "push anyway?" answer: a 409 with
 * code "sensitive" and the plan id that confirms it.
 */
export function protectedEntry(err: unknown): ProtectedEntry | null {
  if (err instanceof ApiError && err.status === 409 && err.details.code === "sensitive" && err.details.plan_id) {
    return { planId: err.details.plan_id, reason: err.details.reason ?? "" };
  }
  return null;
}

function send(method: string, path: string, body?: unknown): Promise<Response> {
  return call(path, {
    method,
    headers: {
      [TOKEN_HEADER]: token,
      ...(body === undefined ? {} : { "Content-Type": "application/json" }),
    },
    body: body === undefined ? undefined : JSON.stringify(body),
    credentials: "omit",
    cache: "no-store",
  });
}

/** failure turns a refused response's JSON error into an ApiError. */
function failure(status: number, text: string): ApiError {
  let parsed: unknown = {};
  try {
    parsed = text ? (JSON.parse(text) as unknown) : {};
  } catch {
    // Not JSON: fall back to the status below.
  }
  const message =
    typeof parsed === "object" && parsed !== null && "error" in parsed
      ? String((parsed as { error: unknown }).error)
      : `the service answered ${status}`;
  const duplicates =
    typeof parsed === "object" && parsed !== null && "duplicates" in parsed
      ? ((parsed as { duplicates: StoreItemView[] }).duplicates ?? undefined)
      : undefined;
  const details: Record<string, string> = {};
  if (typeof parsed === "object" && parsed !== null) {
    for (const [k, v] of Object.entries(parsed)) {
      if (typeof v === "string") details[k] = v;
    }
  }
  return new ApiError(status, message, duplicates, details);
}

async function request<T>(method: string, path: string, body?: unknown): Promise<T> {
  const res = await send(method, path, body);
  const text = await res.text();
  if (!res.ok) throw failure(res.status, text);
  return (text ? JSON.parse(text) : {}) as T;
}

/** requestBlob is request for an endpoint that answers with bytes, not JSON. */
async function requestBlob(method: string, path: string, body?: unknown): Promise<Blob> {
  const res = await send(method, path, body);
  if (!res.ok) throw failure(res.status, await res.text());
  return res.blob();
}

export const api = {
  status: () => request<Status>("GET", "/api/status"),
  // planId confirms an upload the service refused as a protected entry.
  sync: (direction: Direction, planId?: string) =>
    request<SyncResult>("POST", "/api/sync", planId ? { direction, plan_id: planId } : { direction }),
  // A POST, like previewEntry: it returns clipboard text the status withheld.
  revealClipboard: () => request<{ preview: string }>("POST", "/api/clipboard/reveal", {}),
  testFilters: (t: FilterTest) => request<{ matches: FilterMatch[] }>("POST", "/api/filters/test", t),
  dismissSkipped: () => request<Status>("DELETE", "/api/skipped"),
  history: (before?: string) =>
    request<HistoryPage>(
      "GET",
      `/api/history?limit=50${before ? `&before=${encodeURIComponent(before)}` : ""}`,
    ),
  copyEntry: (entryId: string) => request<SyncResult>("POST", "/api/entries/copy", { entry_id: entryId }),
  // A POST, not a GET: it decrypts a body, and a URL that does that is a URL a
  // browser may prefetch, revisit from history or keep in a cache.
  previewEntry: (entryId: string) =>
    request<EntryPreview>("POST", "/api/entries/preview", { entry_id: entryId }),
  // The decrypted bytes of an audio or video entry, for the page to play from
  // a blob URL: a media element cannot send the token header itself.
  entryContent: (entryId: string) =>
    requestBlob("POST", "/api/entries/content", { entry_id: entryId }),
  devices: () => request<RosterView>("GET", "/api/devices"),
  prepareRevoke: (deviceId: string) =>
    request<RevokePlan>("POST", "/api/devices/revoke/prepare", { device_id: deviceId }),
  confirmRevoke: (planId: string) =>
    request<RosterView>("POST", "/api/devices/revoke/confirm", { plan_id: planId }),
  startPairing: () => request<Invite>("POST", "/api/pairing/start", {}),
  joinPairing: (payload: string) => request<{ ok: boolean }>("POST", "/api/pairing/join", { payload }),
  startOffer: (serverUrl: string) =>
    request<Offer>("POST", "/api/pairing/offer/start", { server_url: serverUrl }),
  cancelOffer: () => request<{ ok: boolean }>("POST", "/api/pairing/offer/cancel", {}),
  prepareAcceptOffer: (code: string) =>
    request<OfferPlan>("POST", "/api/pairing/offer/accept/prepare", { code }),
  confirmAcceptOffer: (planId: string) =>
    request<DeviceView>("POST", "/api/pairing/offer/accept/confirm", { plan_id: planId }),
  createGroup: (creationUrl: string) =>
    request<{ ok: boolean }>("POST", "/api/group/create", { creation_url: creationUrl }),
  // forgetGroup takes no argument: a device holds one group key at a time, so
  // there is nothing to name. It is destructive and is never called without
  // the confirmation the Settings panel puts in front of it.
  forgetGroup: () => request<{ ok: boolean }>("POST", "/api/group/forget", {}),
  settings: () => request<SettingsView>("GET", "/api/settings"),
  updateSettings: (patch: SettingsPatch) => request<SettingsView>("POST", "/api/settings", patch),
  updateStatus: () => request<UpdateView>("GET", "/api/update"),
  checkUpdate: () => request<UpdateView>("POST", "/api/update/check", {}),
  installUpdate: (req: InstallRequest) => request<UpdateView>("POST", "/api/update/install", req),
  rollbackUpdate: () => request<UpdateView>("POST", "/api/update/rollback", {}),
  // The token goes in a body, never a URL, and is never sent back.
  setUpdateToken: (token: string) => request<UpdateView>("PUT", "/api/update/token", { token }),
  deleteUpdateToken: () => request<UpdateView>("DELETE", "/api/update/token"),

  store: (q: { text?: string; tags?: string[]; sort?: StoreSort } = {}) => {
    const params = new URLSearchParams();
    if (q.text) params.set("q", q.text);
    for (const t of q.tags ?? []) params.append("tag", t);
    if (q.sort && q.sort !== "newest") params.set("sort", q.sort);
    const qs = params.toString();
    return request<StoreView>("GET", `/api/store${qs ? `?${qs}` : ""}`);
  },
  saveToStore: (src: StoreSource, details: StoreDetails, allowDuplicate = false) =>
    request<StoreItemView>("POST", "/api/store/save", {
      ...src,
      ...details,
      allow_duplicate: allowDuplicate,
    }),
  uploadToStore: (file: File, details: StoreDetails, allowDuplicate = false) =>
    upload(file, details, allowDuplicate),
  editStoreItem: (itemId: string, details: StoreDetails) =>
    request<StoreItemView>("POST", "/api/store/edit", { item_id: itemId, ...details }),
  deleteStoreItem: (itemId: string) =>
    request<Record<string, never>>("POST", "/api/store/delete", { item_id: itemId }),
  copyStoreItem: (itemId: string) => request<SyncResult>("POST", "/api/store/copy", { item_id: itemId }),
  // Save as file: the decrypted bytes, for the page to hand to the browser's
  // own download. A POST, for the same reason as previewEntry.
  storeItemContent: (itemId: string) =>
    requestBlob("POST", "/api/store/content", { item_id: itemId }),
  // A store item's preview, drawn like a history entry's, and the bytes an
  // audio or video preview plays (typed by the service's preview table).
  previewStoreItem: (itemId: string) =>
    request<EntryPreview>("POST", "/api/store/preview", { item_id: itemId }),
  storeItemMedia: (itemId: string) =>
    requestBlob("POST", "/api/store/media", { item_id: itemId }),
};

/** upload sends a file the user dropped or picked as multipart form data. */
async function upload(file: File, details: StoreDetails, allowDuplicate: boolean): Promise<StoreItemView> {
  const form = new FormData();
  form.append("details", JSON.stringify(details));
  if (allowDuplicate) form.append("allow_duplicate", "true");
  form.append("file", file, file.name);
  const res = await call("/api/store/upload", {
    method: "POST",
    headers: { [TOKEN_HEADER]: token },
    body: form,
    credentials: "omit",
    cache: "no-store",
  });
  const text = await res.text();
  if (!res.ok) throw failure(res.status, text);
  return JSON.parse(text) as StoreItemView;
}

// events opens the push stream. The token goes in the query string because a
// browser cannot set a header on a WebSocket upgrade; the service checks it,
// and the Origin, exactly as it does for every other request.
export function events(onEvent: (ev: ServiceEvent) => void): () => void {
  let socket: WebSocket | null = null;
  let closed = false;
  let retry: number | undefined;

  // A browser hides why a WebSocket upgrade failed, so a refused token looks
  // like any dropped connection. Before reconnecting, one ordinary request
  // asks: no answer means the service quit, and 401 means it restarted (send
  // marks the session lost either way), and there is nothing to reconnect to.
  // Without this, an open page would not notice until someone clicked.
  const reconnect = async () => {
    try {
      const res = await send("GET", "/api/status");
      if (res.status === 401) return;
    } catch {
      return;
    }
    open();
  };

  const open = () => {
    if (closed || sessionLost) return;
    const url = `${window.location.origin.replace(/^http/, "ws")}/api/events?token=${encodeURIComponent(token)}`;
    socket = new WebSocket(url);
    socket.onmessage = (msg) => {
      try {
        onEvent(JSON.parse(msg.data as string) as ServiceEvent);
      } catch {
        // A frame this build cannot parse is a frame from a newer service.
      }
    };
    socket.onclose = () => {
      if (!closed && !sessionLost) retry = window.setTimeout(() => void reconnect(), 1500);
    };
  };
  open();

  return () => {
    closed = true;
    if (retry) window.clearTimeout(retry);
    socket?.close();
  };
}

export const hasToken = token !== "";
