/**
 * The question a device asking to join puts to this one (SPEC §3.2 step 4).
 *
 * The joining device's public key reached this device through the relay, so
 * a relay that wanted the group key could have put its own in its place. The
 * fingerprint is what catches that: the user compares it with what the joining
 * device shows, and only then does this device hand anything over.
 *
 * It lives at the top of the app rather than in the Pairing panel because the
 * request arrives whenever the other device gets round to it, and the user may
 * have moved on to another tab by then.
 */

import { useState } from "react";
import { api, type JoinPlan } from "../api";
import { Modal } from "./Modal";

export function JoinRequestDialog({
  request,
  onDone,
  onError,
}: {
  request: JoinPlan | null;
  onDone: (message: string) => void;
  onError: (message: string) => void;
}) {
  const [busy, setBusy] = useState(false);

  const answer = async (confirm: boolean) => {
    if (!request) return;
    setBusy(true);
    try {
      if (confirm) {
        const device = await api.confirmJoin(request.id);
        onDone(`${device.name || "The device"} was added to the group.`);
      } else {
        await api.rejectJoin(request.id);
        onDone("The device was turned down. It got nothing from this group.");
      }
    } catch (err) {
      onError(err instanceof Error ? err.message : String(err));
    } finally {
      setBusy(false);
    }
  };

  const name = request?.device_name || "A device";
  return (
    <Modal
      open={request !== null}
      title={`${name} wants to join this group`}
      // Closing without an answer is not an answer: the request stays until
      // the user picks one, or the code it came from expires.
      dismissable={false}
      onClose={() => undefined}
      footer={
        <>
          <button className="action" disabled={busy} onClick={() => void answer(false)}>
            Turn it down
          </button>
          <button className="action primary" disabled={busy} onClick={() => void answer(true)}>
            {busy ? <span className="spinner" /> : null}
            Add {request?.device_name ?? ""}
          </button>
        </>
      }
    >
      {request ? (
        <>
          <p>
            It used the pairing code this device showed. Adding it gives it the group key and
            everything this group copies from now on.
          </p>
          <p className="muted">
            Check that this fingerprint is the one that device is showing. If it is not, turn it
            down: something other than your device answered the code.
          </p>
          <div className="copy-row">
            <code className="value mono">{request.fingerprint}</code>
          </div>
        </>
      ) : null}
    </Modal>
  );
}
