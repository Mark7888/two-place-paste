import { Caption } from "./Caption";
import type { PreviewProps } from "./index";

/** TextPreview shows a text entry or a text file as text — an HTML file is
 *  its source, never a page. */
export function TextPreview({ preview, action }: PreviewProps) {
  return (
    <>
      <pre className="preview mono">{preview.text}</pre>
      <Caption preview={preview} note={preview.truncated ? "Shown in part · " : undefined} action={action} />
    </>
  );
}
