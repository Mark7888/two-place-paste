import type { EntryPreview } from "../api";
import { bytes } from "../format";

/** Caption is the line under every preview: what the format is, and its size. */
export function Caption({ preview, note }: { preview: EntryPreview; note?: string }) {
  return (
    <p className="muted small">
      {note}
      {preview.format || preview.content_type} · {bytes(preview.bytes)}
    </p>
  );
}
