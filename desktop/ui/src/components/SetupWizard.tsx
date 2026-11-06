/**
 * The first-run setup: a few questions in one dialog, asked before the user
 * pairs, because the defaults (everything off) are safe but leave the app
 * doing nothing.
 *
 * Nothing is saved until the last page, and then in one request together with
 * setup_done. Back is free that way, and a choice the service refuses (the
 * login item, most likely) leaves setup unfinished so it is asked again rather
 * than half applied.
 */

import { useMemo, useState } from "react";
import { api, type SettingsPatch, type SettingsView } from "../api";
import { ChoiceButtons, onOff } from "./ChoiceButtons";
import { Icon } from "./Icon";
import { Modal } from "./Modal";

type Step = "autostart" | "updates" | "sync" | "name" | "done";

export function SetupWizard({
  open,
  settings,
  onDone,
}: {
  open: boolean;
  settings: SettingsView;
  /** onDone gets the settings as saved. */
  onDone: (saved: SettingsView) => void;
}) {
  // A page for something this build cannot do would be a question with no
  // answer, so it is left out.
  const steps = useMemo(() => {
    const s: Step[] = [];
    if (settings.autostart_supported) s.push("autostart");
    s.push("updates");
    if (settings.clipboard_supported) s.push("sync");
    s.push("name", "done");
    return s;
  }, [settings.autostart_supported, settings.clipboard_supported]);

  const [index, setIndex] = useState(0);
  const step = steps[index];

  // null is "not answered yet": these two have to be picked on purpose.
  const [autostart, setAutostart] = useState<boolean | null>(null);
  const [autostartOffConfirmed, setAutostartOffConfirmed] = useState(false);
  const [autoUpdate, setAutoUpdate] = useState<boolean | null>(null);
  // Sending and taking clipboards start off, as they are off by default.
  const [watch, setWatch] = useState(false);
  const [apply, setApply] = useState(false);
  const [name, setName] = useState(settings.device_name);

  const [saving, setSaving] = useState(false);
  const [error, setError] = useState("");

  const pickAutostart = (on: boolean) => {
    setAutostart(on);
    setAutostartOffConfirmed(false);
  };

  const canContinue = (() => {
    switch (step) {
      case "autostart":
        return autostart === true || (autostart === false && autostartOffConfirmed);
      case "updates":
        return autoUpdate !== null;
      case "name":
        return name.trim() !== "";
      default:
        return true;
    }
  })();

  const finish = async () => {
    setSaving(true);
    setError("");
    const patch: SettingsPatch = {
      auto_update: autoUpdate ?? false,
      device_name: name.trim(),
      setup_done: true,
    };
    if (settings.autostart_supported) patch.autostart = autostart ?? false;
    if (settings.clipboard_supported) {
      patch.auto_watch = watch;
      patch.auto_apply = apply;
    }
    try {
      onDone(await api.updateSettings(patch));
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      setSaving(false);
    }
  };

  const next = () => {
    if (!canContinue) return;
    if (step === "done") void finish();
    else setIndex((i) => Math.min(i + 1, steps.length - 1));
  };

  const onOffText = (on: boolean | null) => (on ? "On" : "Off");

  return (
    <Modal
      open={open}
      title="Set up TwoPlacePaste"
      onClose={() => undefined}
      dismissable={false}
      footer={
        <>
          <span className="muted small grow-left">
            Step {index + 1} of {steps.length}
          </span>
          {index > 0 ? (
            <button
              type="button"
              className="action quiet"
              disabled={saving}
              onClick={() => {
                setError("");
                setIndex((i) => i - 1);
              }}
            >
              Back
            </button>
          ) : null}
          <button
            type="button"
            className="action primary"
            disabled={!canContinue || saving}
            onClick={next}
          >
            {saving ? <span className="spinner" /> : null}
            {step === "done" ? (error ? "Try again" : "Pair this device") : "Continue"}
          </button>
        </>
      }
    >
      <form
        className="setup"
        onSubmit={(e) => {
          e.preventDefault();
          next();
        }}
      >
        {step === "autostart" ? (
          <>
            <h3>Start TwoPlacePaste when you log in?</h3>
            <p className="muted">It runs in the tray and syncs in the background.</p>
            <ChoiceButtons
              label="Start at login"
              options={onOff(true)}
              value={autostart}
              onChange={pickAutostart}
            />
            {autostart === false ? (
              <div className="notice warn setup-confirm">
                <Icon name="alert" />
                <div className="stack">
                  <span>
                    Are you sure? TwoPlacePaste only syncs while it is running. With this off,
                    nothing syncs after a restart until you open it yourself.
                  </span>
                  <div className="row tight">
                    <button
                      type="button"
                      className="action small"
                      aria-pressed={autostartOffConfirmed}
                      onClick={() => setAutostartOffConfirmed(true)}
                    >
                      {autostartOffConfirmed ? <Icon name="check" size={14} /> : null}
                      Keep it off
                    </button>
                    <button
                      type="button"
                      className="action small primary"
                      onClick={() => pickAutostart(true)}
                    >
                      Turn it on
                    </button>
                  </div>
                </div>
              </div>
            ) : null}
          </>
        ) : null}

        {step === "updates" ? (
          <>
            <h3>Install updates automatically?</h3>
            <p className="muted">
              Stable releases install in the background. Off means you are told about each one
              and choose when. The channel can be changed in Settings → Updates.
            </p>
            <ChoiceButtons
              label="Install updates automatically"
              options={onOff(true)}
              value={autoUpdate}
              onChange={setAutoUpdate}
            />
          </>
        ) : null}

        {step === "sync" ? (
          <>
            <h3>What should sync on its own?</h3>
            <p className="muted">
              With both off, nothing leaves or reaches this clipboard until you press Upload or
              Download.
            </p>
            <div className="setup-row">
              <div className="text">
                <strong>Send what I copy</strong>
                <span className="why">Anything you copy here is encrypted and uploaded.</span>
              </div>
              <ChoiceButtons
                label="Send what I copy"
                options={onOff()}
                value={watch}
                onChange={setWatch}
              />
            </div>
            <div className="setup-row">
              <div className="text">
                <strong>Take what other devices send</strong>
                <span className="why">A new entry replaces this clipboard as it arrives.</span>
              </div>
              <ChoiceButtons
                label="Take what other devices send"
                options={onOff()}
                value={apply}
                onChange={setApply}
              />
            </div>
          </>
        ) : null}

        {step === "name" ? (
          <>
            <h3>What should this device be called?</h3>
            <p className="muted">Your other devices show this name in their device list.</p>
            <label className="field">
              <span className="label">Device name</span>
              <input
                type="text"
                value={name}
                maxLength={64}
                onChange={(e) => setName(e.target.value)}
              />
            </label>
          </>
        ) : null}

        {step === "done" ? (
          <>
            <h3>All set up</h3>
            <p className="muted">
              Next, pair this device with one you already use, or create a group. Everything here
              can be changed later in Settings.
            </p>
            <dl className="facts">
              {settings.autostart_supported ? (
                <>
                  <dt>Start at login</dt>
                  <dd>{onOffText(autostart)}</dd>
                </>
              ) : null}
              <dt>Automatic updates</dt>
              <dd>{onOffText(autoUpdate)}</dd>
              {settings.clipboard_supported ? (
                <>
                  <dt>Send what I copy</dt>
                  <dd>{onOffText(watch)}</dd>
                  <dt>Take what others send</dt>
                  <dd>{onOffText(apply)}</dd>
                </>
              ) : null}
              <dt>Device name</dt>
              <dd>{name.trim()}</dd>
            </dl>
            {error ? (
              <div className="notice error" style={{ marginTop: "0.9rem" }}>
                <Icon name="alert" />
                <span>{error}</span>
              </div>
            ) : null}
          </>
        ) : null}
      </form>
    </Modal>
  );
}
