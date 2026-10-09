import { useCallback, useEffect, useMemo, useState } from "react";
import { api, events, hasToken, type ServiceEvent, type Status } from "./api";
import { Icon, type IconName } from "./components/Icon";
import { SyncPanel } from "./panels/SyncPanel";
import { HistoryPanel } from "./panels/HistoryPanel";
import { DevicesPanel } from "./panels/DevicesPanel";
import { PairingPanel } from "./panels/PairingPanel";
import { SettingsPanel } from "./panels/SettingsPanel";

const tabs = [
  { id: "sync", label: "Sync", icon: "sync" },
  { id: "history", label: "History", icon: "history" },
  { id: "devices", label: "Devices", icon: "devices" },
  { id: "pairing", label: "Pairing", icon: "link" },
  { id: "settings", label: "Settings", icon: "settings" },
] as const satisfies readonly { id: string; label: string; icon: IconName }[];

type Tab = (typeof tabs)[number]["id"];

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
        setLastEvent(ev);
        void refresh();
      }),
    [refresh],
  );

  const connection = useMemo(() => {
    if (!status) return { cls: "", text: "connecting…" };
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
          {error ? (
            <div className="notice error">
              <Icon name="alert" />
              <span>{error}</span>
            </div>
          ) : null}
          {status?.revoked ? (
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
          {tab === "devices" ? (
            <DevicesPanel inGroup={status?.in_group ?? false} onChanged={refresh} />
          ) : null}
          {tab === "pairing" ? <PairingPanel status={status} onChanged={refresh} /> : null}
          {tab === "settings" ? (
            <SettingsPanel status={status} onChanged={refresh} lastEvent={lastEvent} />
          ) : null}
        </div>
      </main>
    </div>
  );
}
