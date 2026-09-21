import { useEffect, useRef, useState } from "react";
import QRCode from "qrcode";
import { api, type Invite, type Offer, type OfferPlan, type Status } from "../api";
import { when } from "../format";
import { Icon } from "../components/Icon";
import { Modal } from "../components/Modal";

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
      <h1>Pairing</h1>
      <p className="lede">
        {inGroup
          ? "Add another device to this group, in whichever direction has the screen you can read."
          : "This device is not in a group yet. Join one, or create one with a link from the relay’s admin page."}
      </p>

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

/** copy puts a string on the clipboard, falling back to a hidden selection
 *  where the async API is unavailable. The desktop UI is served over plain
 *  http on 127.0.0.1, which is a secure context — but a packaged build served
 *  any other way would not be, and a copy button that silently does nothing is
 *  worse than none. */
async function copyToClipboard(text: string): Promise<void> {
  if (navigator.clipboard && window.isSecureContext) {
    await navigator.clipboard.writeText(text);
    return;
  }
  const area = document.createElement("textarea");
  area.value = text;
  area.style.position = "fixed";
  area.style.top = "-1000px";
  document.body.appendChild(area);
  area.select();
  const ok = document.execCommand("copy");
  document.body.removeChild(area);
  if (!ok) throw new Error("this browser would not copy");
}

function CopyButton({ value }: { value: string }) {
  const [state, setState] = useState<"idle" | "done" | "failed">("idle");

  useEffect(() => {
    if (state === "idle") return;
    const id = window.setTimeout(() => setState("idle"), 1800);
    return () => window.clearTimeout(id);
  }, [state]);

  return (
    <button
      className="action"
      onClick={() => void copyToClipboard(value).then(() => setState("done"), () => setState("failed"))}
    >
      <Icon name={state === "done" ? "check" : "copy"} size={15} />
      {state === "done" ? "Copied" : state === "failed" ? "Select it" : "Copy"}
    </button>
  );
}

// A QR code rendered on this machine, with the same string underneath it.
// Whichever form the other device can read, it is reading one code.
//
// What it carries is the code wrapped in a link to this group's own relay
// (SPEC §6a), not the bare code: a general-purpose scanner shows a base64url
// string as text and offers nothing to open. The code is in the link's
// fragment, so the relay never receives it.
function Code({ value, expiresAt }: { value: string; expiresAt: string }) {
  const canvas = useRef<HTMLCanvasElement | null>(null);

  useEffect(() => {
    if (canvas.current) {
      void QRCode.toCanvas(canvas.current, value, { width: 220, margin: 1 });
    }
  }, [value]);

  return (
    <div className="stack" style={{ marginTop: "0.9rem", gap: "0.6rem" }}>
      <div className="qr-holder">
        <canvas ref={canvas} />
        <span className="muted small">Expires {when(expiresAt)}.</span>
      </div>
      <div className="copy-row">
        <code className="value mono">{value}</code>
        <CopyButton value={value} />
      </div>
    </div>
  );
}

// Inviting: this device holds the group key and mints a token for one that does
// not (SPEC §3.2).
function InvitePanel({ guard, setNotice }: { guard: Guard; setNotice: SetNotice }) {
  const [invite, setInvite] = useState<Invite | null>(null);

  return (
    <section className="group">
      <h2>Add a device</h2>
      <div className="card">
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
          <Icon name="link" size={16} />
          {invite ? "New invitation" : "Show pairing code"}
        </button>
        {invite ? <Code value={invite.link} expiresAt={invite.expires_at} /> : null}
      </div>
    </section>
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
    <section className="group">
      <h2>Accept a code from a device with no group</h2>
      <div className="card stack" style={{ gap: "0.7rem" }}>
        <p className="muted">
          For a device that cannot read this screen. It shows a code; paste it below.
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
        <div className="row">
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
      </div>

      <Modal
        open={plan !== null}
        title={plan ? `Add ${plan.device_name} to this group?` : "Add a device"}
        onClose={() => (busy ? undefined : setPlan(null))}
        footer={
          <>
            <button className="action" disabled={busy} onClick={() => setPlan(null)}>
              Cancel
            </button>
            <button
              className="action primary"
              disabled={busy}
              onClick={() =>
                void guard(async () => {
                  if (!plan) return;
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
              {busy ? <span className="spinner" /> : null}
              Add {plan?.device_name ?? ""}
            </button>
          </>
        }
      >
        {plan ? (
          <>
            <p>
              Adding it gives it the group key and everything this group copies from now on.
            </p>
            <p className="muted">Check that this fingerprint is the one that device is showing:</p>
            <div className="copy-row">
              <code className="value mono">{plan.fingerprint}</code>
            </div>
          </>
        ) : null}
      </Modal>
    </section>
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
    <section className="group">
      <h2>Show a code instead</h2>
      <div className="card stack" style={{ gap: "0.7rem" }}>
        <p className="muted">
          If the other device is the one that can scan, this one can show the code. Type the
          relay&apos;s address — this device does not know it yet — and the code will carry it.
        </p>
        <div className="inline-form">
          <label className="field">
            <span className="label">Relay address</span>
            <input
              type="text"
              className="mono"
              value={serverUrl}
              onChange={(e) => setServerUrl(e.target.value)}
              placeholder="https://tpp.example.com"
            />
          </label>
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
        {offer ? <Code value={offer.link} expiresAt={offer.expires_at} /> : null}
        {offer ? (
          <p className="muted small">
            The other device will show this device&apos;s name and a fingerprint before it adds
            anything. Check that the fingerprint it shows is the one here — this window updates once
            the pairing goes through.
          </p>
        ) : null}
      </div>
    </section>
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
    <section className="group">
      <h2>Join a group</h2>
      <div className="card stack" style={{ gap: "0.7rem" }}>
        <p className="muted">Paste the payload shown by a device that is already in the group.</p>
        <textarea
          className="mono"
          value={payload}
          onChange={(e) => setPayload(e.target.value)}
          placeholder="tpp pairing payload"
        />
        <div className="row">
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
    </section>
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
    <section className="group">
      <h2>Create a group</h2>
      <div className="card stack" style={{ gap: "0.7rem" }}>
        <p className="muted">
          Paste the creation link from the relay&apos;s admin page. It creates one group and is then
          spent.
        </p>
        <div className="inline-form">
          <label className="field">
            <span className="label">Creation link</span>
            <input
              type="text"
              className="mono"
              value={creationUrl}
              onChange={(e) => setCreationUrl(e.target.value)}
              placeholder="https://relay.example/AbCdEf"
            />
          </label>
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
    </section>
  );
}
