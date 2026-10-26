/**
 * How a history entry or a store item is drawn once it is opened.
 *
 * The service decides how an entry is rendered, from its preview table
 * (desktop/internal/preview), and says so in `render`. This module maps each
 * value to the component that draws it. Adding a format the service already
 * renders one of these ways needs nothing here; adding a new way of rendering
 * is a new `Render` value and one component in the table below, which the
 * type checker will not let you forget.
 */

import type { ComponentType } from "react";
import { api, type EntryPreview, type EntryView, type Render } from "../api";
import type { IconName } from "../components/Icon";
import { ImagePreview } from "./ImagePreview";
import { MediaPreview } from "./MediaPreview";
import { TextPreview } from "./TextPreview";

/** PreviewProps is what every renderer gets: the preview, and where audio
 *  and video fetch the bytes they play — a history entry's or a store
 *  item's. */
export interface PreviewProps {
  preview: EntryPreview;
  loadMedia: (id: string) => Promise<Blob>;
}

const renderers: Record<Render, ComponentType<PreviewProps>> = {
  text: TextPreview,
  image: ImagePreview,
  audio: MediaPreview,
  video: MediaPreview,
};

/** PreviewBody draws an opened entry or store item with the component for
 *  its renderer. */
export function PreviewBody({
  preview,
  loadMedia = api.entryContent,
}: {
  preview: EntryPreview;
  loadMedia?: (id: string) => Promise<Blob>;
}) {
  const Body = renderers[preview.render];
  return <Body preview={preview} loadMedia={loadMedia} />;
}

/** entryIcon picks a row's icon: what the entry previews as when it does,
 *  else what it is on the clipboard. */
export function entryIcon(e: Pick<EntryView, "kind" | "render">): IconName {
  if (e.render === "image" || e.render === "audio" || e.render === "video") return e.render;
  return e.kind ?? "file";
}
