import { useEffect, useId, useRef } from "react";
import { useCopyFeedback } from "../hooks/useCopyFeedback";
import { qrPath } from "../lib/paths";
import { CopyButton } from "./CopyButton";

export interface ShownToken {
  value: string;
  name: string;
  url: string;
}

/** TokenDialog shows one creation code: its QR, its link and a copy button.
 *
 *  There is a single dialog for the whole screen rather than one per row: the
 *  QR is a request for a live credential, so the image is only in the page
 *  while the dialog is open. A closed dialog holds no token image, and a
 *  screen listing twenty unused tokens fetches none of them. */
export function TokenDialog({ token, onClose }: { token: ShownToken | null; onClose: () => void }) {
  const ref = useRef<HTMLDialogElement>(null);
  const titleId = useId();
  const feedback = useCopyFeedback();
  const { reset } = feedback;

  useEffect(() => {
    const dialog = ref.current;
    if (!dialog || typeof dialog.showModal !== "function") return;
    if (token && !dialog.open) dialog.showModal();
    if (!token && dialog.open) dialog.close();
  }, [token]);

  // `close` fires for Escape and for the method="dialog" buttons alike, so
  // this is the one place the screen's state follows the element's.
  useEffect(() => {
    const dialog = ref.current;
    if (!dialog) return;
    const onCloseEvent = () => {
      reset();
      onClose();
    };
    dialog.addEventListener("close", onCloseEvent);
    return () => dialog.removeEventListener("close", onCloseEvent);
  }, [onClose, reset]);

  return (
    <dialog
      ref={ref}
      aria-labelledby={titleId}
      onClick={(event) => {
        // Clicking the backdrop — the dialog element itself rather than the
        // panel inside it — dismisses. Escape already does.
        if (event.target === event.currentTarget) event.currentTarget.close();
      }}
    >
      <div className="dialog-head">
        <h2 id={titleId}>{token?.name || "Creation code"}</h2>
        <form method="dialog">
          <button type="submit" className="icon-button" aria-label="Close">
            &times;
          </button>
        </form>
      </div>
      <div className="dialog-body">
        {token && <img className="qr" width={200} height={200} alt="QR code for the creation URL" src={qrPath(token.value)} />}
        <p className="scan-hint">Scan this from the device, or send it the link.</p>
        <div className="copy-row">
          <code className="value mono">{token?.url}</code>
          <CopyButton text={token?.url ?? ""} label="Copy" feedback={feedback} />
        </div>
        <p className="copied" role="status" aria-live="polite">
          {feedback.message ?? ""}
        </p>
      </div>
      <div className="dialog-foot">
        <form method="dialog">
          <button type="submit" className="secondary">
            Done
          </button>
        </form>
      </div>
    </dialog>
  );
}
