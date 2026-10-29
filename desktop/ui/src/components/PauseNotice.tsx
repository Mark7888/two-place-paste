/**
 * The notice for a paused auto-sync, shown above every tab: what is held, how
 * long for, and the button that ends it now. It counts down on its own, and
 * goes when the service says the pause has ended.
 */

import { useState } from "react";
import { api, type PauseView } from "../api";
import { useNow } from "../format";
import { Icon } from "./Icon";

/** clock renders a timestamp as the time of day alone. */
function clock(iso: string): string {
  return new Date(iso).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" });
}

/** left renders how long until a timestamp, rounded up to a minute. */
function left(iso: string): string {
  const mins = Math.max(1, Math.ceil((new Date(iso).getTime() - Date.now()) / 60_000));
  return mins === 1 ? "1 min" : `${mins} min`;
}

export function PauseNotice({
  pause,
  onChanged,
  onError,
}: {
  pause: PauseView;
  onChanged: () => void;
  onError: (message: string) => void;
}) {
  const [busy, setBusy] = useState(false);
  useNow(15_000);

  if (!pause.paused || !pause.until) return null;

  const resume = async () => {
    setBusy(true);
    try {
      await api.setPaused(false);
      onChanged();
    } catch (err) {
      onError(err instanceof Error ? err.message : String(err));
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="notice warn" role="status">
      <Icon name="pause" />
      <span className="grow">
        <strong>Auto-sync is paused</strong> until {clock(pause.until)} (another {left(pause.until)}).
        Nothing is uploaded or downloaded on its own until then; Upload and Download still work.
      </span>
      <button type="button" className="action quiet" disabled={busy} onClick={() => void resume()}>
        <Icon name="play" size={15} />
        Resume
      </button>
    </div>
  );
}
