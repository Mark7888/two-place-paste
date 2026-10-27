/**
 * The dialog a page shows once the service has stopped accepting its token:
 * the app quit, or restarted after an update. Nothing on the page can work
 * again, so it cannot be closed; the way on is the tray.
 *
 * Not the shared Modal: it has no title bar or close button to offer, only a
 * short message, and it is tinted red because it is the end of this page.
 */

import { useEffect, useRef } from "react";
import { Icon } from "./Icon";

export function SessionLostDialog({ open }: { open: boolean }) {
  const ref = useRef<HTMLDialogElement | null>(null);

  useEffect(() => {
    const dialog = ref.current;
    if (open && dialog && !dialog.open) dialog.showModal();
  }, [open]);

  return (
    <dialog
      ref={ref}
      className="session-lost"
      aria-labelledby="session-lost-title"
      // Escape fires cancel; refusing it is what keeps the dialog up.
      onCancel={(event) => event.preventDefault()}
    >
      <Icon name="alert" size={20} />
      <div>
        <h2 id="session-lost-title">TwoPlacePaste was closed or restarted</h2>
        <p>Open it again from the tray icon to continue.</p>
      </div>
    </dialog>
  );
}
