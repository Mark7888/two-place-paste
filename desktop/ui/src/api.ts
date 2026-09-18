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
    window.history.replaceState({}, "", window.location.pathname);
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
}

export interface EntryView {
  id: string;
  epoch: number;
  size: number;
  created_at: string;
  expires_at: string;
}

export interface Status {
  in_group: boolean;
  connected: boolean;
  epoch: number;
  device_id: string;
  device_name: string;
  server_url: string;
  keystore: string;
  revoked: boolean;
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

export interface HistoryPage {
  entries: EntryView[];
  next_before?: string;
}

export interface Invite {
  payload: string;
  expires_at: string;
}

// Offer is a pairing code this device is showing while it waits for a member
// of some group to accept it. The code is both the QR contents and the
// copyable string.
export interface Offer {
  code: string;
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
  autostart: boolean;
  autostart_supported: boolean;
  clipboard_supported: boolean;
  device_name: string;
  restart_required: boolean;
}

export interface SettingsPatch {
  port?: number;
  auto_watch?: boolean;
  autostart?: boolean;
  device_name?: string;
}

export interface ServiceEvent {
  kind: string;
  at: string;
  message?: string;
  epoch?: number;
  device_id?: string;
  name?: string;
}

export class ApiError extends Error {
  readonly status: number;

  constructor(status: number, message: string) {
    super(message);
    this.status = status;
    this.name = "ApiError";
  }
}

async function request<T>(method: string, path: string, body?: unknown): Promise<T> {
  const res = await fetch(path, {
    method,
    headers: {
      [TOKEN_HEADER]: token,
      ...(body === undefined ? {} : { "Content-Type": "application/json" }),
    },
    body: body === undefined ? undefined : JSON.stringify(body),
    credentials: "omit",
    cache: "no-store",
  });
  const text = await res.text();
  const parsed = text ? (JSON.parse(text) as unknown) : {};
  if (!res.ok) {
    const message =
      typeof parsed === "object" && parsed !== null && "error" in parsed
        ? String((parsed as { error: unknown }).error)
        : `the service answered ${res.status}`;
    throw new ApiError(res.status, message);
  }
  return parsed as T;
}

export const api = {
  status: () => request<Status>("GET", "/api/status"),
  sync: (direction: Direction) => request<SyncResult>("POST", "/api/sync", { direction }),
  history: (before?: string) =>
    request<HistoryPage>(
      "GET",
      `/api/history?limit=50${before ? `&before=${encodeURIComponent(before)}` : ""}`,
    ),
  copyEntry: (entryId: string) => request<SyncResult>("POST", "/api/entries/copy", { entry_id: entryId }),
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
};

// events opens the push stream. The token goes in the query string because a
// browser cannot set a header on a WebSocket upgrade; the service checks it,
// and the Origin, exactly as it does for every other request.
export function events(onEvent: (ev: ServiceEvent) => void): () => void {
  let socket: WebSocket | null = null;
  let closed = false;
  let retry: number | undefined;

  const open = () => {
    if (closed) return;
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
      if (!closed) retry = window.setTimeout(open, 1500);
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
