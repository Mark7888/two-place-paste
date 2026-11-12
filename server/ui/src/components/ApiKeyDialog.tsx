import { useEffect, useId, useRef } from "react";
import { useCopyFeedback } from "../hooks/useCopyFeedback";
import { CopyButton } from "./CopyButton";
import { Notice } from "./Notice";

export interface ShownApiKey {
  name: string;
  key: string;
}

/** ApiKeyDialog shows a key that was just created, the one time it can be
 *  seen: the relay keeps only its hash. Closing the dialog drops the key from
 *  the page as well, so nothing on screen holds it afterwards. */
export function ApiKeyDialog({ apiKey, onClose }: { apiKey: ShownApiKey | null; onClose: () => void }) {
  const ref = useRef<HTMLDialogElement>(null);
  const titleId = useId();
  const feedback = useCopyFeedback();
  const { reset } = feedback;

  useEffect(() => {
    const dialog = ref.current;
    if (!dialog || typeof dialog.showModal !== "function") return;
    if (apiKey && !dialog.open) dialog.showModal();
    if (!apiKey && dialog.open) dialog.close();
  }, [apiKey]);

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

  // No closing on a backdrop click here, unlike the token dialog: losing
  // this key to a stray click means making another.
  return (
    <dialog ref={ref} aria-labelledby={titleId}>
      <div className="dialog-head">
        <h2 id={titleId}>{apiKey ? `API key “${apiKey.name}”` : "API key"}</h2>
        <form method="dialog">
          <button type="submit" className="icon-button" aria-label="Close">
            &times;
          </button>
        </form>
      </div>
      <div className="dialog-body start">
        <Notice kind="warn">Copy the key now. It is shown only this once; the relay keeps only a hash of it.</Notice>
        <div className="copy-row">
          <code className="value mono">{apiKey?.key}</code>
          <CopyButton text={apiKey?.key ?? ""} label="Copy" feedback={feedback} />
        </div>
        <p className="copied" role="status" aria-live="polite">
          {feedback.message ?? ""}
        </p>
        <p className="hint">Send it in the Authorization header of each request:</p>
        <pre className="example">
          <code>curl -H "Authorization: Bearer $TPP_API_KEY" {window.location.origin}/api/v1/dashboard</code>
        </pre>
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
