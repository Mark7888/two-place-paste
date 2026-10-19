import type { EntryPreview } from "../api";
import { Caption } from "./Caption";

/** TextPreview shows a text entry or a text file as text — an HTML file is
 *  its source, never a page. */
export function TextPreview({ preview }: { preview: EntryPreview }) {
  return (
    <>
      <pre className="preview mono">{preview.text}</pre>
      <Caption preview={preview} note={preview.truncated ? "Shown in part · " : undefined} />
    </>
  );
}
