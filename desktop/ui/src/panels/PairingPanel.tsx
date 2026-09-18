import { useEffect, useRef, useState } from "react";
import QRCode from "qrcode";
import { api, type Invite, type Offer, type OfferPlan, type Status } from "../api";
import { when } from "../format";

// Pairing on the desktop shows a QR and the same string as copyable text
// (SPEC §7.2: no scanner here). Both QR codes are rendered locally by a bundled
// library — a code carrying a pairing token or an offer must not travel to a
// remote QR service to be drawn.
//
// The panel has two halves because pairing runs in two directions, and which
// one is on screen depends only on whether this device is in a group yet.
//
// Paired, it can invite (show a code a joining device consumes) and it can
// accept (read a code an unpaired device is showing). Accepting is the one that
// needs a dialog: it admits a device to the group and hands it the group key,
// so the service will not do it without a plan, and a plan only exists once
// this component has rendered the name and the fingerprint.
//
// Unpaired, it can show an offer of its own — which is how a desktop with no
// camera pairs from a phone that is already in a group — or take a code from a
// device that is.
export function PairingPanel({ status, onChanged }: { status: Status | null; onChanged: () => void }) {
  const [error, setError] = useState("");
  const [notice, setNotice] = useState("");

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
        <>
          <InvitePanel guard={guard} setNotice={setNotice} />
          <AcceptOfferPanel guard={guard} setNotice={setNotice} onChanged={onChanged} />
        </>
      ) : (
        <>
          <ShowOfferPanel guard={guard} setNotice={setNotice} onChanged={onChanged} />
          <JoinPanel guard={guard} setNotice={setNotice} onChanged={onChanged} />
          <CreateGroupPanel guard={guard} setNotice={setNotice} onChanged={onChanged} />
        </>
      )}
    </>
  );
}

type Guard = (fn: () => Promise<void>) => Promise<void>;
type SetNotice = (message: string) => void;

// A QR code rendered on this machine, with the same string underneath it.
// Whichever form the other device can read, it is reading one code.
function Code({ value, expiresAt }: { value: string; expiresAt: string }) {
  const canvas = useRef<HTMLCanvasElement | null>(null);

  useEffect(() => {
    if (canvas.current) {
      void QRCode.toCanvas(canvas.current, value, { width: 220, margin: 1 });
    }
  }, [value]);

  return (
    <div style={{ marginTop: "1rem" }}>
      <canvas ref={canvas} />
      <p className="mono" style={{ userSelect: "all" }}>
        {value}
      </p>
      <p className="muted">Expires {when(expiresAt)}.</p>
    </div>
  );
}

// Inviting: this device holds the group key and mints a token for one that does
// not (SPEC §3.2).
function InvitePanel({ guard, setNotice }: { guard: Guard; setNotice: SetNotice }) {
  const [invite, setInvite] = useState<Invite | null>(null);

  return (
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
            setNotice("");
          })
        }
      >
        {invite ? "New invitation" : "Show pairing code"}
      </button>
      {invite ? <Code value={invite.payload} expiresAt={invite.expires_at} /> : null}
    </div>
  );
}

// Accepting an offer: the other device is showing the code, and this one admits
// it (docs/plans/joiner-emitted-pairing.md).
//
// The confirmation is not a formality here. An invitation a joiner reads wrongly
// costs it a failed pairing; a code this device reads wrongly costs the group its
// key. So the button that admits anything is not rendered at all until the
// service has returned a plan, and the plan carries what the user has to check.
function AcceptOfferPanel({
  guard,
  setNotice,
  onChanged,
}: {
  guard: Guard;
  setNotice: SetNotice;
  onChanged: () => void;
}) {
  const [code, setCode] = useState("");
  const [plan, setPlan] = useState<OfferPlan | null>(null);
  const [busy, setBusy] = useState(false);

  return (
    <div className="panel">
      <h2>Accept a code from a device with no group</h2>
      <p className="muted">
        For a device that cannot read this screen — a phone whose camera you would rather not use,
        or one you are holding while the desktop is here. It shows a code; paste it below.
      </p>
      <textarea
        className="mono"
        value={code}
        onChange={(e) => {
          setCode(e.target.value);
          setPlan(null);
        }}
        placeholder="tpp pairing code"
      />
      <div className="row" style={{ marginTop: "0.5rem" }}>
        <button
          className="action"
          disabled={!code.trim() || busy}
          onClick={() =>
            void guard(async () => {
              setPlan(await api.prepareAcceptOffer(code.trim()));
            })
          }
        >
          Read the code
        </button>
      </div>

      {plan ? (
        <div className="notice info" style={{ marginTop: "1rem" }}>
          <p>
            <strong>{plan.device_name}</strong> is asking to join this group. Adding it gives it the
            group key and everything this group copies from now on.
          </p>
          <p>
            Check that this fingerprint is the one that device is showing:
            <br />
            <span className="mono" style={{ userSelect: "all" }}>
              {plan.fingerprint}
            </span>
          </p>
          <div className="row" style={{ marginTop: "0.5rem" }}>
            <button
              className="action primary"
              disabled={busy}
              onClick={() =>
                void guard(async () => {
                  setBusy(true);
                  try {
                    const device = await api.confirmAcceptOffer(plan.id);
                    setPlan(null);
                    setCode("");
                    setNotice(`${device.name} was added to the group.`);
                    onChanged();
                  } finally {
                    setBusy(false);
                  }
                })
              }
            >
              Add {plan.device_name}
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

// Showing an offer: this device has no group key, so it cannot mint a pairing
// token — the relay holds an offer for it instead, and a member accepts it.
//
// The relay URL is the one thing the user has to supply in this direction: a
// device with no group has no relay URL either, and the code has to say where
// the offer is held.
function ShowOfferPanel({
  guard,
  setNotice,
  onChanged,
}: {
  guard: Guard;
  setNotice: SetNotice;
  onChanged: () => void;
}) {
  const [serverUrl, setServerUrl] = useState("");
  const [offer, setOffer] = useState<Offer | null>(null);

  return (
    <div className="panel">
      <h2>Show a code instead</h2>
      <p className="muted">
        If the other device is the one that can scan, this one can show the code. Type the relay&apos;s
        address — this device does not know it yet — and the code will carry it.
      </p>
      <input
        type="text"
        className="mono"
        value={serverUrl}
        onChange={(e) => setServerUrl(e.target.value)}
        placeholder="https://tpp.example.com"
      />
      <div className="row" style={{ marginTop: "0.5rem" }}>
        <button
          className="action primary"
          disabled={!serverUrl.trim()}
          onClick={() =>
            void guard(async () => {
              setOffer(await api.startOffer(serverUrl.trim()));
              setNotice("Scan this from a device that is already in the group, or send it the text.");
            })
          }
        >
          {offer ? "New code" : "Show a code"}
        </button>
        {offer ? (
          <button
            className="action"
            onClick={() =>
              void guard(async () => {
                await api.cancelOffer();
                setOffer(null);
                setNotice("The code was withdrawn.");
                onChanged();
              })
            }
          >
            Withdraw
          </button>
        ) : null}
      </div>
      {offer ? <Code value={offer.code} expiresAt={offer.expires_at} /> : null}
      {offer ? (
        <p className="muted">
          The other device will show this device&apos;s name and a fingerprint before it adds
          anything. Check that the fingerprint it shows is the one here — this window updates once
          the pairing goes through.
        </p>
      ) : null}
    </div>
  );
}

// Joining with a code a member is showing (SPEC §3.2, the original direction).
function JoinPanel({
  guard,
  setNotice,
  onChanged,
}: {
  guard: Guard;
  setNotice: SetNotice;
  onChanged: () => void;
}) {
  const [payload, setPayload] = useState("");

  return (
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
  );
}

function CreateGroupPanel({
  guard,
  setNotice,
  onChanged,
}: {
  guard: Guard;
  setNotice: SetNotice;
  onChanged: () => void;
}) {
  const [creationUrl, setCreationUrl] = useState("");

  return (
    <div className="panel">
      <h2>Create a group</h2>
      <p className="muted">
        Paste the creation link from the relay&apos;s admin page. It creates one group and is then
        spent.
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
  );
}
