import { useCallback, useEffect, useState } from "react";
import { api, type GroupView, type GroupsView, type ServiceEvent } from "../api";
import { Icon } from "../components/Icon";
import { Modal } from "../components/Modal";

/** Events after which the list may have changed. */
const groupEvents = new Set(["groups", "connected", "group_name", "revoked", "group_deleted"]);

/** label is what a group is called on screen: its name, or its relay when the admin gave it none. */
function label(g: GroupView): string {
  return g.name || g.server_url || "Unnamed group";
}

/**
 * Groups: the groups this device keeps, one in use and the others set aside
 * (/spec/crypto.md §10.1). Only shown while "Keep several groups" is on.
 *
 * Switching is the app closing and starting again with the other group, so
 * nothing here needs to know what the other panels show: the service resets,
 * pushes an event, and every panel reloads.
 */
export function GroupsPanel({
  lastEvent,
  onChanged,
  onAdded,
}: {
  lastEvent: ServiceEvent | null;
  onChanged: () => void;
  /** onAdded opens Pairing once the group in use is set aside. */
  onAdded: () => void;
}) {
  const [groups, setGroups] = useState<GroupsView | null>(null);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState("");
  const [adding, setAdding] = useState(false);
  const [switching, setSwitching] = useState<GroupView | null>(null);
  const [removing, setRemoving] = useState<GroupView | null>(null);

  const load = useCallback(async () => {
    try {
      setGroups(await api.groups());
      setError("");
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    }
  }, []);

  useEffect(() => {
    void load();
  }, [load]);

  useEffect(() => {
    if (lastEvent && groupEvents.has(lastEvent.kind)) void load();
  }, [lastEvent, load]);

  const run = async (what: string, op: () => Promise<GroupsView>): Promise<boolean> => {
    setError("");
    setBusy(what);
    try {
      setGroups(await op());
      onChanged();
      return true;
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
      return false;
    } finally {
      setBusy("");
    }
  };

  const active = groups?.active ?? null;
  const dead = active?.dead === true;

  const switchTo = async (g: GroupView) => {
    if (await run(g.ref, () => api.switchGroup(g.ref))) setSwitching(null);
  };

  // A dead group is about to be thrown away, so that is asked first; a
  // working one is only set aside, and switching back costs nothing.
  const askSwitch = (g: GroupView) => {
    if (dead) setSwitching(g);
    else void switchTo(g);
  };

  const add = async () => {
    if (await run("add", () => api.addGroup())) {
      setAdding(false);
      onAdded();
    }
  };

  const remove = async () => {
    if (!removing) return;
    const ref = removing.ref;
    if (await run(ref, () => api.removeGroup(ref))) setRemoving(null);
  };

  return (
    <>
      <h1>Groups</h1>
      <p className="lede">
        The groups this device keeps. One is in use at a time; the others are set aside with their
        keys, and switching to one picks up where it left off.
      </p>

      {error ? (
        <div className="notice error">
          <Icon name="alert" />
          <span>{error}</span>
        </div>
      ) : null}

      {!groups ? (
        error ? null : (
          <div className="row">
            <span className="spinner" />
            <span className="muted">Loading…</span>
          </div>
        )
      ) : (
        <>
          <section className="group">
            <h2>In use</h2>
            {active ? (
              <div className="list">
                <div className="item">
                  <div className="body">
                    <div className="title">{label(active)}</div>
                    <div className="muted small mono">{active.server_url}</div>
                  </div>
                  <div className="actions">
                    <span className={`pill ${dead ? "off" : "on"}`}>
                      <span className="dot" />
                      {dead ? "can’t be used" : "in use"}
                    </span>
                  </div>
                </div>
              </div>
            ) : (
              <div className="empty">
                This device is not using a group. Switch to one below, or create or join one from
                Pairing.
              </div>
            )}
            {dead ? (
              <div className="notice warn" style={{ marginTop: "0.75rem" }}>
                <Icon name="alert" />
                <span>
                  This device was removed from this group, or the group was deleted. Switching or
                  adding a group discards it instead of keeping it.
                </span>
              </div>
            ) : null}
          </section>

          <section className="group">
            <div className="group-head">
              <h2>Set aside</h2>
              <button
                type="button"
                className="action small"
                disabled={busy !== ""}
                onClick={() => setAdding(true)}
              >
                <Icon name="plus" size={15} />
                Add a group…
              </button>
            </div>
            {groups.parked.length === 0 ? (
              <div className="empty">
                No other groups. Add one to keep this group and set up another next to it.
              </div>
            ) : (
              <div className="list">
                {groups.parked.map((g) => (
                  <div className="item" key={g.ref}>
                    <div className="body">
                      <div className="title">{label(g)}</div>
                      <div className="muted small mono">{g.server_url}</div>
                    </div>
                    <div className="actions">
                      <button
                        type="button"
                        className="action small"
                        disabled={busy !== ""}
                        onClick={() => askSwitch(g)}
                      >
                        {busy === g.ref ? <span className="spinner" /> : <Icon name="sync" size={15} />}
                        {busy === g.ref ? "Switching…" : "Switch"}
                      </button>
                      <button
                        type="button"
                        className="action small danger"
                        disabled={busy !== ""}
                        onClick={() => setRemoving(g)}
                      >
                        <Icon name="trash" size={15} />
                        Remove…
                      </button>
                    </div>
                  </div>
                ))}
              </div>
            )}
          </section>
        </>
      )}

      <Modal
        open={adding}
        title="Add a group?"
        onClose={() => (busy ? undefined : setAdding(false))}
        footer={
          <>
            <button className="action" disabled={busy !== ""} onClick={() => setAdding(false)}>
              Cancel
            </button>
            <button className="action primary" disabled={busy !== ""} onClick={() => void add()}>
              {busy === "add" ? <span className="spinner" /> : <Icon name="plus" size={15} />}
              {busy === "add" ? "Setting it aside…" : "Continue to Pairing"}
            </button>
          </>
        }
      >
        {active && !dead ? (
          <p>
            <strong>{label(active)}</strong> is set aside with its keys, and you can switch back to it
            here at any time. Nothing syncs with it meanwhile.
          </p>
        ) : active ? (
          <p>
            <strong>{label(active)}</strong> can&rsquo;t be used any more, so its keys are deleted
            from this device.
          </p>
        ) : null}
        <p>
          This device then joins the new group as a new device: create a group with a token from your
          server admin, or pair it with a device that is in the group, from Pairing.
        </p>
      </Modal>

      <Modal
        open={switching !== null}
        title={switching ? `Switch to ${label(switching)}?` : "Switch"}
        onClose={() => (busy ? undefined : setSwitching(null))}
        footer={
          <>
            <button className="action" disabled={busy !== ""} onClick={() => setSwitching(null)}>
              Cancel
            </button>
            <button
              className="action solid-danger"
              disabled={busy !== ""}
              onClick={() => (switching ? void switchTo(switching) : undefined)}
            >
              {busy ? <span className="spinner" /> : <Icon name="sync" size={15} />}
              {busy ? "Switching…" : "Switch and discard"}
            </button>
          </>
        }
      >
        {active ? (
          <p>
            <strong>{label(active)}</strong> can&rsquo;t be used any more, so its keys are deleted
            from this device instead of being set aside.
          </p>
        ) : null}
      </Modal>

      <Modal
        open={removing !== null}
        title={removing ? `Remove ${label(removing)}?` : "Remove"}
        onClose={() => (busy ? undefined : setRemoving(null))}
        footer={
          <>
            <button className="action" disabled={busy !== ""} onClick={() => setRemoving(null)}>
              Cancel
            </button>
            <button className="action solid-danger" disabled={busy !== ""} onClick={() => void remove()}>
              {busy ? <span className="spinner" /> : <Icon name="trash" size={15} />}
              {busy ? "Removing…" : "Delete this group’s keys"}
            </button>
          </>
        }
      >
        <p>
          The keys this device holds for the group are deleted from this machine. To use the group
          again, pair this device with one that is still in it.
        </p>
        <p className="muted">
          Nothing is removed from the relay. The group still lists this device until another device
          revokes it, which re-keys the group.
        </p>
      </Modal>
    </>
  );
}
