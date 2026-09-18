/**
 * The one modal, over the platform's own <dialog>.
 *
 * <dialog> is used rather than a hand-rolled overlay because it brings the
 * three things a hand-rolled one always forgets: the focus trap, Escape, and
 * the top layer, so a dialog is never behind something with a z-index. This
 * component adds what it does not: opening on a prop rather than on a method
 * call, and closing on a backdrop click.
 */

import { useEffect, useRef, type ReactNode } from "react";
import { Icon } from "./Icon";

export function Modal({
  open,
  title,
  onClose,
  children,
  footer,
  wide,
  dismissable = true,
}: {
  open: boolean;
  title: string;
  onClose: () => void;
  children: ReactNode;
  footer?: ReactNode;
  wide?: boolean;
  /** dismissable is false for a question that has to be answered — the
   *  direction chooser, where cancelling and "either one" are not the same. */
  dismissable?: boolean;
}) {
  const ref = useRef<HTMLDialogElement | null>(null);

  useEffect(() => {
    const dialog = ref.current;
    if (!dialog) return;
    if (open && !dialog.open) dialog.showModal();
    if (!open && dialog.open) dialog.close();
  }, [open]);

  // `close` fires for Escape too, so this is the single place the parent's
  // state is brought back in line with the element's.
  useEffect(() => {
    const dialog = ref.current;
    if (!dialog) return;
    const onCancel = (event: Event) => {
      if (!dismissable) {
        event.preventDefault();
        return;
      }
      onClose();
    };
    const onCloseEvent = () => onClose();
    dialog.addEventListener("cancel", onCancel);
    dialog.addEventListener("close", onCloseEvent);
    return () => {
      dialog.removeEventListener("cancel", onCancel);
      dialog.removeEventListener("close", onCloseEvent);
    };
  }, [onClose, dismissable]);

  return (
    <dialog
      ref={ref}
      className={`modal${wide ? " wide" : ""}`}
      onClick={(event) => {
        // The dialog element is the backdrop; anything inside it is the panel.
        if (dismissable && event.target === ref.current) onClose();
      }}
    >
      <div className="modal-head">
        <h2>{title}</h2>
        {dismissable ? (
          <button type="button" className="icon-button" aria-label="Close" onClick={onClose}>
            <Icon name="close" size={18} />
          </button>
        ) : null}
      </div>
      <div className="modal-body">{children}</div>
      {footer ? <div className="modal-foot">{footer}</div> : null}
    </dialog>
  );
}
