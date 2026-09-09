import { useCallback, useEffect, useMemo, useState } from "react";
import { api, events, hasToken, type Status } from "./api";
import { SyncPanel } from "./panels/SyncPanel";
import { HistoryPanel } from "./panels/HistoryPanel";
import { DevicesPanel } from "./panels/DevicesPanel";
import { PairingPanel } from "./panels/PairingPanel";
import { SettingsPanel } from "./panels/SettingsPanel";

const tabs = ["Sync", "History", "Devices", "Pairing", "Settings"] as const;
type Tab = (typeof tabs)[number];

export function App() {
  const [status, setStatus] = useState<Status | null>(null);
  const [error, setError] = useState<string>("");
  const [tab, setTab] = useState<Tab>("Sync");

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
  useEffect(() => events(() => void refresh()), [refresh]);

  const connection = useMemo(() => {
    if (!status) return { cls: "", text: "connecting…" };
    if (status.revoked) return { cls: "off", text: "revoked" };
    if (!status.in_group) return { cls: "", text: "no group yet" };
    return status.connected ? { cls: "on", text: "connected" } : { cls: "off", text: "offline" };
  }, [status]);

  if (!hasToken) {
    return (
      <div className="app">
        <div className="notice error">
          This page was opened without a token. Open TwoPlacePaste from the tray icon: the token is
          generated per launch and the tray is the only thing that has it.
        </div>
      </div>
    );
  }

  return (
    <div className="app">
      <header className="top">
        <h1>TwoPlacePaste</h1>
        <span className={`dot ${connection.cls}`} />
        <span className="muted">{connection.text}</span>
        {status?.device_name ? <span className="muted">· {status.device_name}</span> : null}
      </header>

      {error ? <div className="notice error">{error}</div> : null}
      {status?.revoked ? (
        <div className="notice error">
          This device was revoked. Its credentials are gone; pair it again from a device that is still
          in the group.
        </div>
      ) : null}

      <nav className="tabs">
        {tabs.map((t) => (
          <button key={t} role="tab" aria-selected={tab === t} onClick={() => setTab(t)}>
            {t}
          </button>
        ))}
      </nav>

      {tab === "Sync" ? <SyncPanel status={status} onChanged={refresh} /> : null}
      {tab === "History" ? <HistoryPanel inGroup={status?.in_group ?? false} /> : null}
      {tab === "Devices" ? <DevicesPanel inGroup={status?.in_group ?? false} onChanged={refresh} /> : null}
      {tab === "Pairing" ? <PairingPanel status={status} onChanged={refresh} /> : null}
      {tab === "Settings" ? <SettingsPanel status={status} /> : null}
    </div>
  );
}
