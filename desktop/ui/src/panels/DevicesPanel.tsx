import { useCallback, useEffect, useState } from "react";
import { api, type RevokePlan, type RosterView } from "../api";
import { relative, useNow, when } from "../format";
import { Icon } from "../components/Icon";
import { Modal } from "../components/Modal";

// The revocation dialog of SPEC §3.3 step 2.
//
// Confirming requires a plan, and a plan only exists once the service has
// fetched the roster and this component has rendered the devices it names. The
// button below is therefore not merely disabled until the roster loads: there
// is nothing to confirm with until it does. It is a real modal now rather than
// a panel that appeared below the table — the previous one could be scrolled
// off screen while it was the only thing waiting for an answer.
export function DevicesPanel({ inGroup, onChanged }: { inGroup: boolean; onChanged: () => void }) {
  const [roster, setRoster] = useState<RosterView | null>(null);
  const [plan, setPlan] = useState<RevokePlan | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");

  useNow();

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

  if (!inGroup) {
    return (
      <>
        <h1>Devices</h1>
        <div className="empty">This device is not in a group yet.</div>
      </>
    );
  }

  return (
    <>
      <h1>Devices</h1>
      <p className="lede">
        Everything that holds this group&apos;s key. Removing one generates a new key and gives it
        to the rest, so the removed device cannot read anything written afterwards.
      </p>

      <div className="group-head">
        <h2>{roster ? `In the group · epoch ${roster.epoch}` : "In the group"}</h2>
        <button className="action small" onClick={() => void load()}>
          <Icon name="refresh" size={15} />
          Refresh
        </button>
      </div>

      {error ? (
        <div className="notice error">
          <Icon name="alert" />
          <span>{error}</span>
        </div>
      ) : null}

      {!roster ? (
        <div className="row">
          <span className="spinner" />
          <span className="muted">Loading the group&apos;s devices…</span>
        </div>
      ) : (
        <div className="list">
          {roster.devices.map((d) => (
            <div className="item" key={d.id}>
              <div className="body">
                <div className="title">
                  {d.name}
                  {d.this ? <span className="muted small"> · this device</span> : null}
                </div>
                <div className="muted small">
                  added {when(d.created_at)} · last seen {relative(d.last_seen)}
                </div>
              </div>
              <div className="actions">
                {d.this ? null : (
                  <button className="action small danger" onClick={() => void prepare(d.id)}>
                    <Icon name="trash" size={15} />
                    Revoke
                  </button>
                )}
              </div>
            </div>
          ))}
        </div>
      )}

      <Modal
        open={plan !== null}
        title={plan ? `Revoke ${plan.target.name}?` : "Revoke"}
        onClose={() => (busy ? undefined : setPlan(null))}
        footer={
          <>
            <button className="action" disabled={busy} onClick={() => setPlan(null)}>
              Cancel
            </button>
            <button className="action solid-danger" disabled={busy} onClick={() => void confirm()}>
              {busy ? <span className="spinner" /> : <Icon name="trash" size={15} />}
              {busy ? "Revoking…" : `Revoke ${plan?.target.name ?? ""}`}
            </button>
          </>
        }
      >
        {plan ? (
          <>
            <p>
              A new group key will be generated and given to the devices below.{" "}
              <strong>{plan.target.name}</strong> loses access immediately and cannot rejoin without
              pairing again.
            </p>
            <h3 style={{ margin: "1rem 0 0.4rem" }}>Devices that keep access</h3>
            <div className="list">
              {plan.remaining.map((d) => (
                <div className="item" key={d.id}>
                  <div className="body">
                    <div className="title">
                      {d.name}
                      {d.this ? <span className="muted small"> · this device</span> : null}
                    </div>
                  </div>
                </div>
              ))}
            </div>
            <p className="muted small" style={{ marginTop: "0.9rem" }}>
              Your local clipboard is not touched. Entries written before the change stay readable
              only to whoever already had them, and expire within 24 hours.
            </p>
          </>
        ) : null}
      </Modal>
    </>
  );
}
