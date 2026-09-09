import { useEffect, useState } from "react";
import { api, type SettingsView, type Status } from "../api";

export function SettingsPanel({ status }: { status: Status | null }) {
  const [settings, setSettings] = useState<SettingsView | null>(null);
  const [port, setPort] = useState("");
  const [error, setError] = useState("");
  const [notice, setNotice] = useState("");

  useEffect(() => {
    void (async () => {
      try {
        const s = await api.settings();
        setSettings(s);
        setPort(s.port ? String(s.port) : "");
      } catch (err) {
        setError(err instanceof Error ? err.message : String(err));
      }
    })();
  }, []);

  const patch = async (body: Parameters<typeof api.updateSettings>[0]) => {
    setError("");
    setNotice("");
    try {
      const s = await api.updateSettings(body);
      setSettings(s);
      if (s.restart_required) setNotice("The port changes at the next launch.");
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    }
  };

  if (!settings) return <div className="panel">{error || "Loading…"}</div>;

  return (
    <div className="panel">
      <h2>Settings</h2>
      {error ? <div className="notice error">{error}</div> : null}
      {notice ? <div className="notice info">{notice}</div> : null}

      <label className="toggle">
        <input
          type="checkbox"
          checked={settings.auto_watch}
          disabled={!settings.clipboard_supported}
          onChange={(e) => void patch({ auto_watch: e.target.checked })}
        />
        <span className="text">
          <strong>Watch the clipboard</strong>
          <br />
          <span className="muted">
            Off by default. When on, anything you copy is encrypted and uploaded as the group&apos;s
            latest entry. What this service writes to your clipboard is never uploaded back.
          </span>
        </span>
      </label>

      <label className="toggle">
        <input
          type="checkbox"
          checked={settings.autostart}
          disabled={!settings.autostart_supported}
          onChange={(e) => void patch({ autostart: e.target.checked })}
        />
        <span className="text">
          <strong>Start at login</strong>
          <br />
          <span className="muted">
            Off by default.{" "}
            {settings.autostart_supported
              ? "Installs a login item for your account only."
              : "This platform has no login item in this build."}
          </span>
        </span>
      </label>

      <div style={{ marginTop: "1rem" }}>
        <strong>Localhost port</strong>
        <p className="muted">
          Currently listening on 127.0.0.1:{settings.listen_port}. Leave empty for the default
          (47821). A change takes effect at the next launch.
        </p>
        <div className="row">
          <input
            type="text"
            inputMode="numeric"
            value={port}
            placeholder="47821"
            onChange={(e) => setPort(e.target.value.replace(/[^0-9]/g, ""))}
            style={{ maxWidth: "10rem" }}
          />
          <button
            className="action"
            onClick={() => void patch({ port: port === "" ? 0 : Number(port) })}
          >
            Save port
          </button>
        </div>
        {settings.restart_required ? (
          <p className="notice info" style={{ marginTop: "0.75rem" }}>
            Saved port {settings.port} differs from the one in use; restart TwoPlacePaste to apply it.
          </p>
        ) : null}
      </div>

      <div style={{ marginTop: "1rem" }}>
        <strong>This device</strong>
        <p className="muted">
          Shown in the revocation dialog on your other devices. A change applies at the next launch.
        </p>
        <div className="row">
          <input
            type="text"
            value={settings.device_name}
            onChange={(e) => setSettings({ ...settings, device_name: e.target.value })}
            style={{ maxWidth: "18rem" }}
          />
          <button className="action" onClick={() => void patch({ device_name: settings.device_name })}>
            Save name
          </button>
        </div>
        {status ? (
          <p className="muted mono" style={{ marginTop: "0.5rem" }}>
            keys in {status.keystore}
          </p>
        ) : null}
      </div>
    </div>
  );
}
