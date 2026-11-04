import type { ReactNode } from "react";
import type { EntryPreview } from "../api";
import { bytes } from "../format";

/** Caption is the line under every preview: what the format is, and its size.
 *  An action, such as importing an exported filter set, sits at its right. */
export function Caption({ preview, note, action }: { preview: EntryPreview; note?: string; action?: ReactNode }) {
  const text = (
    <p className="muted small">
      {note}
      {preview.format || preview.content_type} · {bytes(preview.bytes)}
    </p>
  );
  if (!action) return text;
  return (
    <div className="spread preview-caption">
      {text}
      {action}
    </div>
  );
}
