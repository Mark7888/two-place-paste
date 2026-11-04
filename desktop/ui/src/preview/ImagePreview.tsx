import { Caption } from "./Caption";
import type { PreviewProps } from "./index";

/** ImagePreview draws an image the service inlined as a data URL. It is
 *  always an <img>: that is what keeps an SVG's scripts from running. */
export function ImagePreview({ preview, action }: PreviewProps) {
  if (preview.image_too_large || !preview.image_data_url) {
    return <Caption preview={preview} note="Too large to show here. Copy it to see it · " action={action} />;
  }
  return (
    <>
      <img className="preview-image" src={preview.image_data_url} alt="The entry's image" />
      <Caption preview={preview} action={action} />
    </>
  );
}
