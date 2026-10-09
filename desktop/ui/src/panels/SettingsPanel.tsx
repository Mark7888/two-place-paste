import { useEffect, useState } from "react";
import { api, type ServiceEvent, type SettingsView, type Status } from "../api";
import { Icon } from "../components/Icon";
import { Modal } from "../components/Modal";
import { UpdatesSection } from "./UpdatesSection";

/**
 * Settings.
 *
 * Every switch used to carry a paragraph, and every group of two fields its own
 * border, so the screen read as a stack of leaflets. The rule here: a control
 * gets one line saying what it does, the group gets a heading, and anything
 * longer than that belongs where the consequence is — the leave dialog spells
 * out what leaving does, because that is where the user is deciding.
 */
export function SettingsPanel({
  status,
  onChanged,
  lastEvent,
}: {
  status: Status | null;
  onChanged: () => void;
  lastEvent: ServiceEvent | null;
}) {
  const [settings, setSettings] = useState<SettingsView | null>(null);
  const [port, setPort] = useState("");
  const [name, setName] = useState("");
  const [error, setError] = useState("");
  const [notice, setNotice] = useState("");
  const [confirmingLeave, setConfirmingLeave] = useState(false);
  const [leaving, setLeaving] = useState(false);

  useEffect(() => {
    void (async () => {
      try {
        const s = await api.settings();
        setSettings(s);
        setPort(s.port ? String(s.port) : "");
        setName(s.device_name);
      } catch (err) {
        setError(err instanceof Error ? err.message : String(err));
      }
    })();
  }, []);

  // Opened from the tray's "Check for updates…": bring that section into view.
  useEffect(() => {
    if (settings && window.location.hash === "#updates") {
      document.getElementById("updates")?.scrollIntoView();
    }
  }, [settings]);

  const patch = async (body: Parameters<typeof api.updateSettings>[0]): Promise<boolean> => {
    setError("");
    setNotice("");
    try {
      const s = await api.updateSettings(body);
      setSettings(s);
      setPort(s.port ? String(s.port) : "");
      setName(s.device_name);
      setNotice(s.restart_required ? "Saved. The port changes at the next launch." : "Saved.");
      return true;
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
      return false;
    }
  };

  // Leaving is two steps, not one, and the second spells out what does and does
  // not happen: the keys go from this machine, and nothing at all is removed
  // from the relay, which still lists this device until another one revokes it
  // (SPEC §3.3).
  const leave = async () => {
    setError("");
    setNotice("");
    setLeaving(true);
    try {
      await api.forgetGroup();
      setConfirmingLeave(false);
      setNotice("This device left the group. Its keys are gone; pair again from Pairing.");
      onChanged();
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      setLeaving(false);
    }
  };

  if (!settings) {
    return (
      <>
        <h1>Settings</h1>
        {error ? (
          <div className="notice error">
            <Icon name="alert" />
            <span>{error}</span>
          </div>
        ) : (
          <div className="row">
            <span className="spinner" />
            <span className="muted">Loading…</span>
          </div>
        )}
      </>
    );
  }

  return (
    <>
      <h1>Settings</h1>
      <p className="lede">This installation, and what you can change about it.</p>

      {error ? (
        <div className="notice error">
          <Icon name="alert" />
          <span>{error}</span>
        </div>
      ) : null}
      {notice ? (
        <div className="notice ok" role="status">
          <Icon name="check" />
          <span>{notice}</span>
        </div>
      ) : null}

      <section className="group">
        <h2>Behaviour</h2>
        <div className="list">
          <label className="toggle">
            <input
              type="checkbox"
              checked={settings.auto_watch}
              disabled={!settings.clipboard_supported}
              onChange={(e) => void patch({ auto_watch: e.target.checked })}
            />
            <span className="text">
              <strong>Watch the clipboard</strong>
              <span className="why">
                {settings.clipboard_supported
                  ? "Anything you copy is encrypted and uploaded as the group’s latest entry. Off by default."
                  : "This build cannot reach a clipboard."}
              </span>
            </span>
          </label>
          <label className="toggle">
            <input
              type="checkbox"
              checked={settings.auto_apply}
              disabled={!settings.clipboard_supported}
              onChange={(e) => void patch({ auto_apply: e.target.checked })}
            />
            <span className="text">
              <strong>Take what other devices upload</strong>
              <span className="why">
                {settings.clipboard_supported
                  ? "A new entry from another device replaces this clipboard as soon as it arrives. Off by default."
                  : "This build cannot reach a clipboard."}
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
              <span className="why">
                {settings.autostart_supported
                  ? "Installs a login item for your account only. Off by default."
                  : "This platform has no login item in this build."}
              </span>
            </span>
          </label>
        </div>
      </section>

      <section className="group">
        <h2>This device</h2>
        <div className="card stack" style={{ gap: "1rem" }}>
          <div className="inline-form">
            <label className="field">
              <span className="label">Name</span>
              <input type="text" value={name} onChange={(e) => setName(e.target.value)} />
            </label>
            <button
              className="action"
              disabled={name.trim() === "" || name === settings.device_name}
              onClick={() => void patch({ device_name: name.trim() })}
            >
              Save
            </button>
          </div>
          <p className="muted small">
            Shown in the revocation dialog on your other devices. Applies at the next launch.
          </p>

          <div className="inline-form">
            <label className="field" style={{ maxWidth: "12rem" }}>
              <span className="label">Localhost port</span>
              <input
                type="text"
                inputMode="numeric"
                value={port}
                placeholder="47821"
                onChange={(e) => setPort(e.target.value.replace(/[^0-9]/g, ""))}
              />
            </label>
            <button
              className="action"
              onClick={() => void patch({ port: port === "" ? 0 : Number(port) })}
            >
              Save
            </button>
          </div>
          <p className="muted small">
            Listening on 127.0.0.1:{settings.listen_port}. Empty means the default, 47821. Applies at
            the next launch.
          </p>
          {settings.restart_required ? (
            <div className="notice warn">
              <Icon name="alert" />
              <span>
                Saved port {settings.port} differs from the one in use; restart TwoPlacePaste to
                apply it.
              </span>
            </div>
          ) : null}

          {status ? (
            <dl className="facts">
              <dt>Keys stored in</dt>
              <dd>{status.keystore}</dd>
            </dl>
          ) : null}
        </div>
      </section>

      <UpdatesSection settings={settings} onPatch={patch} lastEvent={lastEvent} />

      <section className="group">
        <h2>Group</h2>
        <div className="card stack" style={{ gap: "0.85rem" }}>
          {status?.in_group ? (
            <dl className="facts">
              <dt>Relay</dt>
              <dd className="mono">{status.server_url || "—"}</dd>
              <dt>Key generation</dt>
              <dd>epoch {status.epoch}</dd>
            </dl>
          ) : (
            <p className="muted">
              This device is not in a group. Create one or pair with a device that is, from Pairing.
            </p>
          )}
          <div>
            <button
              className="action danger"
              disabled={!status?.in_group}
              onClick={() => setConfirmingLeave(true)}
            >
              <Icon name="trash" size={15} />
              Leave the group…
            </button>
          </div>
        </div>
      </section>

      <Modal
        open={confirmingLeave}
        title="Leave the group?"
        onClose={() => (leaving ? undefined : setConfirmingLeave(false))}
        footer={
          <>
            <button
              className="action"
              disabled={leaving}
              onClick={() => setConfirmingLeave(false)}
            >
              Cancel
            </button>
            <button className="action solid-danger" disabled={leaving} onClick={() => void leave()}>
              {leaving ? <span className="spinner" /> : <Icon name="trash" size={15} />}
              {leaving ? "Leaving…" : "Delete this device’s keys"}
            </button>
          </>
        }
      >
        <p>
          This device&apos;s private key and the group key are deleted from this machine, and a new
          identity is generated. Nothing here can read what the group writes next.
        </p>
        <p className="muted">
          Nothing is removed from the relay. The group still lists this device, and the group key it
          held is still the group&apos;s key — to change that, revoke this device from another one,
          which re-keys the group. Your clipboard is not touched.
        </p>
        <p className="muted">
          This is also how the device is moved to another group: it can hold only one group key at a
          time.
        </p>
      </Modal>
    </>
  );
}
