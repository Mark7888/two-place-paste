import { useCallback, useEffect, useMemo, useState } from "react";
import {
  api,
  events,
  hasToken,
  isSessionLost,
  onSessionLost,
  type ServiceEvent,
  type Status,
} from "./api";
import { Icon, type IconName } from "./components/Icon";
import { PauseNotice } from "./components/PauseNotice";
import { SessionLostDialog } from "./components/SessionLostDialog";
import { bytes } from "./format";
import { SyncPanel } from "./panels/SyncPanel";
import { HistoryPanel } from "./panels/HistoryPanel";
import { StorePanel } from "./panels/StorePanel";
import { DevicesPanel } from "./panels/DevicesPanel";
import { PairingPanel } from "./panels/PairingPanel";
import { SettingsPanel } from "./panels/SettingsPanel";

const tabs = [
  { id: "sync", label: "Sync", icon: "sync" },
  { id: "history", label: "History", icon: "history" },
  { id: "store", label: "Store", icon: "store" },
  { id: "devices", label: "Devices", icon: "devices" },
  { id: "pairing", label: "Pairing", icon: "link" },
  { id: "settings", label: "Settings", icon: "settings" },
] as const satisfies readonly { id: string; label: string; icon: IconName }[];

type Tab = (typeof tabs)[number]["id"];

/**
 * What the build that opened this page did just before: an update or a
 * rollback someone started from the Updates section relaunches the service,
 * which opens a fresh page with #updated or #rolled-back. It is read once and
 * taken out of the address bar, so it is not said again.
 */
type Arrival = "updated" | "rolled-back" | null;

function readArrival(): Arrival {
  const hash = window.location.hash;
  if (hash !== "#updated" && hash !== "#rolled-back") return null;
  window.history.replaceState({}, "", window.location.pathname + window.location.search);
  return hash === "#updated" ? "updated" : "rolled-back";
}

const arrival = readArrival();

export function App() {
  const [status, setStatus] = useState<Status | null>(null);
  const [error, setError] = useState<string>("");
  // The tray's "Check for updates…" opens #updates when it finds one.
  const [tab, setTab] = useState<Tab>(() =>
    window.location.hash === "#updates" ? "settings" : "sync",
  );

  // lastEvent is how a panel knows the service did something without each of
  // them opening its own socket. It changes identity on every push, which is
  // all an effect needs to depend on.
  const [lastEvent, setLastEvent] = useState<ServiceEvent | null>(null);

  // transfer is the chunked upload or download in flight — an entry larger
  // than one frame — or null. It is one strip for every tab, because a copy
  // from History and an upload from Sync are the same wait.
  const [transfer, setTransfer] = useState<ServiceEvent | null>(null);

  // The version an update or rollback just brought in, for the notice that
  // says so; null once dismissed or when this launch was not one.
  const [arrived, setArrived] = useState<{ kind: Exclude<Arrival, null>; version: string } | null>(
    null,
  );

  useEffect(() => {
    if (!arrival) return;
    api
      .updateStatus()
      .then((v) => setArrived({ kind: arrival, version: v.current.version }))
      .catch(() => setArrived({ kind: arrival, version: "" }));
  }, []);

  // Set once the service stops accepting this page's token: it restarted
  // (after an update, say) or quit. Nothing here works after that, so a
  // dialog that cannot be closed says to reopen the app from the tray.
  const [lost, setLost] = useState(isSessionLost);
  useEffect(() => onSessionLost(() => setLost(true)), []);

  const refresh = useCallback(async () => {
    try {
      setStatus(await api.status());
      setError("");
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    }
  }, []);

  useEffect(() => {
    void refresh();
  }, [refresh]);

  // The service pushes; the UI does not poll. Every event is a refresh hint,
  // never content.
  useEffect(
    () =>
      events((ev) => {
        if (ev.kind === "progress") {
          // One per chunk: a progress bar, not a reason to re-read the status.
          setTransfer(ev.total && ev.done !== undefined && ev.done < ev.total ? ev : null);
          return;
        }
        setLastEvent(ev);
        void refresh();
      }),
    [refresh],
  );

  const connection = useMemo(() => {
    if (!status) return { cls: "", text: "connecting…" };
    if (status.group_deleted) return { cls: "off", text: "group deleted" };
    if (status.revoked) return { cls: "off", text: "revoked" };
    if (!status.in_group) return { cls: "", text: "no group" };
    return status.connected ? { cls: "on", text: "connected" } : { cls: "off", text: "offline" };
  }, [status]);

  if (!hasToken) {
    return (
      <div className="app" style={{ display: "block", padding: "3rem 1rem" }}>
        <div className="page">
          <div className="notice error">
            <Icon name="alert" />
            <span>
              This page was opened without a token. Open TwoPlacePaste from the tray icon: the token
              is generated per launch and the tray is the only thing that has it.
            </span>
          </div>
        </div>
      </div>
    );
  }

  return (
    <div className="app">
      <header className="titlebar">
        <span className="name">TwoPlacePaste</span>
        <span className="spacer" />
        {status?.group_name ? (
          <span className="group-name" title="This device's group">
            {status.group_name}
          </span>
        ) : null}
        {status?.device_name ? <span className="device-name">{status.device_name}</span> : null}
        <span className={`pill ${connection.cls}`}>
          <span className="dot" />
          {connection.text}
        </span>
      </header>

      {/*
        One nav element, one source of truth. The previous version set
        aria-selected on plain buttons with role="tab" and no tablist around
        them, which is why assistive tech and the stylesheet could disagree
        about which one was current — and why a mis-sized hit area could leave
        a tab looking clickable without being it. Here the button is the whole
        row, and `aria-current` is set from the same state that renders it.
      */}
      <nav className="rail" aria-label="Sections">
        {tabs.map((t) => (
          <button
            key={t.id}
            type="button"
            aria-current={tab === t.id ? "page" : undefined}
            title={t.label}
            onClick={() => setTab(t.id)}
          >
            <Icon name={t.icon} size={18} />
            <span>{t.label}</span>
          </button>
        ))}
      </nav>

      <main className="content">
        <div className="page">
          {/*
            The latest automatic transfer a size limit held back. It is the
            service's to keep, not this tab's: it was most likely skipped while
            no window was open, and it stays until dismissed or until a newer
            entry lands on either side.
          */}
          {arrived ? (
            <div className="notice ok centered" role="status">
              <Icon name="check" />
              <span className="grow">
                {arrived.kind === "updated"
                  ? "TwoPlacePaste has been updated"
                  : "TwoPlacePaste has been rolled back"}
                {arrived.version ? ` to ${arrived.version}` : ""}.
              </span>
              <button
                type="button"
                className="icon-button"
                aria-label="Dismiss"
                title="Dismiss"
                onClick={() => setArrived(null)}
              >
                <Icon name="close" size={16} />
              </button>
            </div>
          ) : null}
          {status?.pause ? (
            <PauseNotice pause={status.pause} onChanged={() => void refresh()} onError={setError} />
          ) : null}
          {status?.skipped ? (
            <div className="notice info" role="status">
              <Icon name="alert" />
              <span className="grow">{status.skipped.message}</span>
              <button
                type="button"
                className="action quiet"
                onClick={() => {
                  void api
                    .dismissSkipped()
                    .then(setStatus)
                    .catch((err: unknown) => setError(err instanceof Error ? err.message : String(err)));
                }}
              >
                Dismiss
              </button>
            </div>
          ) : null}
          {transfer ? (
            <div className="transfer" role="status">
              <span>
                {transfer.direction === "upload" ? "Uploading" : "Downloading"} a large item —{" "}
                {bytes(transfer.done ?? 0)} of {bytes(transfer.total ?? 0)}
              </span>
              <progress max={transfer.total} value={transfer.done} />
            </div>
          ) : null}
          {error ? (
            <div className="notice error">
              <Icon name="alert" />
              <span>{error}</span>
            </div>
          ) : null}
          {status?.group_deleted ? (
            <div className="notice error">
              <Icon name="alert" />
              <span>
                The server admin deleted this device&rsquo;s group. Nothing more syncs here. Leave the
                group in Settings, then create a new group with a fresh token or pair with a device in
                another group.
              </span>
            </div>
          ) : status?.revoked ? (
            <div className="notice error">
              <Icon name="alert" />
              <span>
                This device was revoked. Its credentials are gone; pair it again from a device that
                is still in the group.
              </span>
            </div>
          ) : null}

          {tab === "sync" ? <SyncPanel status={status} onChanged={refresh} /> : null}
          {tab === "history" ? (
            <HistoryPanel
              inGroup={status?.in_group ?? false}
              epoch={status?.epoch ?? 0}
              lastEvent={lastEvent}
            />
          ) : null}
          {tab === "store" ? (
            <StorePanel inGroup={status?.in_group ?? false} lastEvent={lastEvent} />
          ) : null}
          {tab === "devices" ? (
            <DevicesPanel inGroup={status?.in_group ?? false} onChanged={refresh} />
          ) : null}
          {tab === "pairing" ? <PairingPanel status={status} onChanged={refresh} /> : null}
          {tab === "settings" ? (
            <SettingsPanel status={status} onChanged={refresh} lastEvent={lastEvent} />
          ) : null}
        </div>
      </main>

      <SessionLostDialog open={lost} />
    </div>
  );
}
