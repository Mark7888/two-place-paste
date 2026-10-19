import { useEffect, useState } from "react";
import { api, type EntryPreview } from "../api";
import { Icon } from "../components/Icon";
import { Caption } from "./Caption";

/**
 * MediaPreview plays an audio or video entry.
 *
 * The bytes are fetched with the token, like every other call, and played
 * from a blob URL the page makes itself; the URL is released when the preview
 * closes, and with it the decrypted copy. Nothing plays until the user presses
 * play, and only the metadata is read before that.
 *
 * Which codecs play is the browser's business, not the service's: an iPhone's
 * HEVC .mov plays in Safari and often nowhere else. When the browser gives up,
 * the preview says so and points at Copy.
 */
export function MediaPreview({ preview }: { preview: EntryPreview }) {
  const [src, setSrc] = useState<string | null>(null);
  const [error, setError] = useState("");
  const [unplayable, setUnplayable] = useState(false);

  useEffect(() => {
    let url: string | null = null;
    let cancelled = false;
    api
      .entryContent(preview.id)
      .then((blob) => {
        if (cancelled) return;
        url = URL.createObjectURL(blob);
        setSrc(url);
      })
      .catch((err: unknown) => {
        if (!cancelled) setError(err instanceof Error ? err.message : String(err));
      });
    return () => {
      cancelled = true;
      if (url) URL.revokeObjectURL(url);
    };
  }, [preview.id]);

  if (error) {
    return (
      <div className="notice error">
        <Icon name="alert" />
        <span>{error}</span>
      </div>
    );
  }
  if (unplayable) {
    return (
      <Caption preview={preview} note="This browser cannot play this file. Copy it to open it elsewhere · " />
    );
  }
  if (!src) {
    return (
      <div className="row">
        <span className="spinner" />
        <span className="muted small">Decrypting…</span>
      </div>
    );
  }

  const onError = () => setUnplayable(true);
  return (
    <>
      {preview.render === "video" ? (
        <video className="preview-media" src={src} controls preload="metadata" playsInline onError={onError} />
      ) : (
        <audio className="preview-media" src={src} controls preload="metadata" onError={onError} />
      )}
      <Caption preview={preview} />
    </>
  );
}
