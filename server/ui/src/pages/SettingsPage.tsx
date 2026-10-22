import { useId, useState } from "react";
import { api, type Settings } from "../api";
import { ErrorNotice } from "../components/Notice";
import { useAction, type Action } from "../hooks/useAction";
import { useDocumentTitle } from "../hooks/useDocumentTitle";
import { useResource } from "../hooks/useResource";
import { useRouter } from "../router";

/** SettingsPage holds the settings that change without a restart. Everything
 *  else is configured in the relay's environment. */
export function SettingsPage() {
  useDocumentTitle("Settings");
  const { data, error, version, reload } = useResource((signal) => api.settings(signal), []);
  const { notify } = useRouter();
  const action = useAction();

  const save = (staleDays: number) =>
    action.run(async () => {
      await api.saveSettings(staleDays);
      notify("Settings saved.");
      reload();
    });

  return (
    <>
      <ErrorNotice error={action.error ?? error?.message} />
      {data && (
        <>
          <h1>Settings</h1>
          <p className="lede">
            Server settings you can change without a restart. Everything else is configured in the relay's environment.
          </p>
          <SettingsForm key={version} settings={data} action={action} onSave={save} />
        </>
      )}
    </>
  );
}

function SettingsForm({
  settings,
  action,
  onSave,
}: {
  settings: Settings;
  action: Action;
  onSave: (staleDays: number) => void;
}) {
  const [staleDays, setStaleDays] = useState(String(settings.stale_days));
  const id = useId();
  return (
    <form
      className="card"
      onSubmit={(event) => {
        event.preventDefault();
        // An empty or partial number goes to the relay as zero, and it says
        // what it accepts.
        onSave(Number(staleDays) || 0);
      }}
    >
      <div className="form-row">
        <div className="field">
          <label htmlFor={id}>Mark a group stale after</label>
          <div className="with-unit">
            <input
              type="number"
              id={id}
              min={1}
              max={settings.max_days}
              required
              value={staleDays}
              onChange={(e) => setStaleDays(e.target.value)}
            />
            <span>days without any device connecting</span>
          </div>
        </div>
        <button type="submit" disabled={action.busy}>
          Save
        </button>
      </div>
      <span className="hint">
        The dashboard marks such groups and offers to delete them. The default is {settings.default_days} days.
      </span>
    </form>
  );
}
