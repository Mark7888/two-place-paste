import { useState } from "react";
import { useUpdateCheck } from "../hooks/useUpdateCheck";
import { BUILD, updateApp } from "../lib/build";

/** UpdateBar appears at the bottom of every screen when the relay serves a
 *  newer build of the admin than the one running — most often an installed
 *  app left open across an upgrade. Update loads the new build; Later hides
 *  the bar until the relay has yet another one. The app never reloads on its
 *  own: that would throw away whatever is half typed. */
export function UpdateBar({ running = BUILD }: { running?: string }) {
  const latest = useUpdateCheck(running);
  const [later, setLater] = useState<string | null>(null);
  const [updating, setUpdating] = useState(false);

  if (!latest || latest === later) return null;
  return (
    <div className="update-bar" role="status">
      <p>A new version of the admin is available.</p>
      <div className="update-actions">
        <button type="button" className="secondary" onClick={() => setLater(latest)}>
          Later
        </button>
        <button
          type="button"
          disabled={updating}
          onClick={() => {
            setUpdating(true);
            void updateApp();
          }}
        >
          {updating ? "Updating…" : "Update"}
        </button>
      </div>
    </div>
  );
}
