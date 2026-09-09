import { useEffect, useRef, useState } from "react";
import QRCode from "qrcode";
import { api, type Invite, type Status } from "../api";
import { when } from "../format";

// Pairing on the desktop shows a QR and the same string as copyable text
// (SPEC §7.2: no scanner here). The QR is rendered locally by a bundled
// library — the payload carries a pairing token and must not travel to a
// remote QR service to be drawn.
export function PairingPanel({ status, onChanged }: { status: Status | null; onChanged: () => void }) {
  const [invite, setInvite] = useState<Invite | null>(null);
  const [payload, setPayload] = useState("");
  const [creationUrl, setCreationUrl] = useState("");
  const [error, setError] = useState("");
  const [notice, setNotice] = useState("");
  const canvas = useRef<HTMLCanvasElement | null>(null);

  useEffect(() => {
    if (invite && canvas.current) {
      void QRCode.toCanvas(canvas.current, invite.payload, { width: 220, margin: 1 });
    }
  }, [invite]);

  const guard = async (fn: () => Promise<void>) => {
    setError("");
    setNotice("");
    try {
      await fn();
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    }
  };

  const inGroup = status?.in_group ?? false;

  return (
    <>
      {error ? <div className="notice error">{error}</div> : null}
      {notice ? <div className="notice info">{notice}</div> : null}

      {inGroup ? (
        <div className="panel">
          <h2>Add a device</h2>
          <p className="muted">
            Show this to the joining device. The token is short-lived and pairs exactly one device.
          </p>
          <button
            className="action primary"
            onClick={() =>
              void guard(async () => {
                setInvite(await api.startPairing());
              })
            }
          >
            {invite ? "New invitation" : "Show pairing code"}
          </button>
          {invite ? (
            <div style={{ marginTop: "1rem" }}>
              <canvas ref={canvas} />
              <p className="mono" style={{ userSelect: "all" }}>
                {invite.payload}
              </p>
              <p className="muted">Expires {when(invite.expires_at)}.</p>
            </div>
          ) : null}
        </div>
      ) : (
        <>
          <div className="panel">
            <h2>Join a group</h2>
            <p className="muted">Paste the payload shown by a device that is already in the group.</p>
            <textarea
              className="mono"
              value={payload}
              onChange={(e) => setPayload(e.target.value)}
              placeholder="tpp pairing payload"
            />
            <div className="row" style={{ marginTop: "0.5rem" }}>
              <button
                className="action primary"
                disabled={!payload.trim()}
                onClick={() =>
                  void guard(async () => {
                    await api.joinPairing(payload.trim());
                    setPayload("");
                    setNotice("Paired. The group key arrives from the inviting device.");
                    onChanged();
                  })
                }
              >
                Join
              </button>
            </div>
          </div>

          <div className="panel">
            <h2>Create a group</h2>
            <p className="muted">
              Paste the creation link from the relay&apos;s admin page. It creates one group and is
              then spent.
            </p>
            <input
              type="text"
              className="mono"
              value={creationUrl}
              onChange={(e) => setCreationUrl(e.target.value)}
              placeholder="https://relay.example/AbCdEf"
            />
            <div className="row" style={{ marginTop: "0.5rem" }}>
              <button
                className="action"
                disabled={!creationUrl.trim()}
                onClick={() =>
                  void guard(async () => {
                    await api.createGroup(creationUrl.trim());
                    setCreationUrl("");
                    setNotice("Group created. This device holds the first group key.");
                    onChanged();
                  })
                }
              >
                Create
              </button>
            </div>
          </div>
        </>
      )}
    </>
  );
}
