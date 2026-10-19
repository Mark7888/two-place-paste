/**
 * How a history entry is drawn once it is opened.
 *
 * The service decides how an entry is rendered, from its preview table
 * (desktop/internal/preview), and says so in `render`. This module maps each
 * value to the component that draws it. Adding a format the service already
 * renders one of these ways needs nothing here; adding a new way of rendering
 * is a new `Render` value and one component in the table below, which the
 * type checker will not let you forget.
 */

import type { ComponentType } from "react";
import type { EntryPreview, EntryView, Render } from "../api";
import type { IconName } from "../components/Icon";
import { ImagePreview } from "./ImagePreview";
import { MediaPreview } from "./MediaPreview";
import { TextPreview } from "./TextPreview";

const renderers: Record<Render, ComponentType<{ preview: EntryPreview }>> = {
  text: TextPreview,
  image: ImagePreview,
  audio: MediaPreview,
  video: MediaPreview,
};

/** PreviewBody draws an opened entry with the component for its renderer. */
export function PreviewBody({ preview }: { preview: EntryPreview }) {
  const Body = renderers[preview.render];
  return <Body preview={preview} />;
}

/** entryIcon picks a row's icon: what the entry previews as when it does,
 *  else what it is on the clipboard. */
export function entryIcon(e: Pick<EntryView, "kind" | "render">): IconName {
  if (e.render === "image" || e.render === "audio" || e.render === "video") return e.render;
  return e.kind ?? "file";
}
