import { useCallback, useEffect, useState } from "react";
import { api, type RevokePlan, type RosterView } from "../api";
import { relative, when } from "../format";

// The revocation dialog of SPEC §3.3 step 2.
//
// Confirming requires a plan, and a plan only exists once the service has
// fetched the roster and this component has rendered the devices it names. The
// button below is therefore not merely disabled until the roster loads: there
// is nothing to confirm with until it does.
export function DevicesPanel({ inGroup, onChanged }: { inGroup: boolean; onChanged: () => void }) {
  const [roster, setRoster] = useState<RosterView | null>(null);
  const [plan, setPlan] = useState<RevokePlan | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");

  const load = useCallback(async () => {
    if (!inGroup) return;
    setError("");
    try {
      setRoster(await api.devices());
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    }
  }, [inGroup]);

  useEffect(() => {
    void load();
  }, [load]);

  const prepare = async (deviceId: string) => {
    setError("");
    try {
      setPlan(await api.prepareRevoke(deviceId));
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    }
  };

  const confirm = async () => {
    if (!plan) return;
    setBusy(true);
    try {
      setRoster(await api.confirmRevoke(plan.id));
      setPlan(null);
      onChanged();
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      setBusy(false);
    }
  };

  if (!inGroup) return <div className="panel muted">This device is not in a group yet.</div>;

  return (
    <div className="panel">
      <div className="spread">
        <h2>Devices {roster ? <span className="muted">· epoch {roster.epoch}</span> : null}</h2>
        <button className="action" onClick={() => void load()}>
          Refresh
        </button>
      </div>
      {error ? <div className="notice error">{error}</div> : null}
      {!roster ? (
        <p className="muted">Loading the group&apos;s devices…</p>
      ) : (
        <table>
          <thead>
            <tr>
              <th>Name</th>
              <th>Added</th>
              <th>Last seen</th>
              <th />
            </tr>
          </thead>
          <tbody>
            {roster.devices.map((d) => (
              <tr key={d.id}>
                <td>
                  {d.name} {d.this ? <span className="muted">· this device</span> : null}
                </td>
                <td>{when(d.created_at)}</td>
                <td>{relative(d.last_seen)}</td>
                <td style={{ textAlign: "right" }}>
                  {d.this ? null : (
                    <button className="action danger" onClick={() => void prepare(d.id)}>
                      Revoke…
                    </button>
                  )}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      )}

      {plan ? (
        <div className="panel" style={{ marginTop: "1rem" }}>
          <h2>Revoke {plan.target.name}?</h2>
          <p>
            A new group key will be generated and given to the devices below. {plan.target.name} will
            lose access immediately and cannot rejoin without pairing again.
          </p>
          <p className="muted">Devices that keep access:</p>
          <ul>
            {plan.remaining.map((d) => (
              <li key={d.id}>
                {d.name}
                {d.this ? " (this device)" : ""}
              </li>
            ))}
          </ul>
          <p className="muted">
            Your local clipboard is not touched by this. Entries written before the change stay
            readable only to whoever already had them, and expire within 24 hours.
          </p>
          <div className="row">
            <button className="action danger" disabled={busy} onClick={() => void confirm()}>
              {busy ? "Revoking…" : `Revoke ${plan.target.name}`}
            </button>
            <button className="action" disabled={busy} onClick={() => setPlan(null)}>
              Cancel
            </button>
          </div>
        </div>
      ) : null}
    </div>
  );
}
