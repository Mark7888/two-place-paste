/**
 * The icon set, drawn here rather than installed.
 *
 * Every icon is one 24×24 stroked path, which is a few hundred bytes against a
 * dependency that would have to be kept current for the dozen glyphs this
 * application uses. Emoji are deliberately not used: they render as a
 * different picture on every platform, they carry colour the theme cannot
 * control, and at small sizes they are unreadable.
 *
 * Icons here are decoration next to a label, so they are `aria-hidden` and the
 * label is what a screen reader announces. The one exception is an icon-only
 * button, which carries its own `aria-label`.
 */

import type { SVGProps } from "react";

export type IconName =
  | "sync"
  | "history"
  | "devices"
  | "link"
  | "settings"
  | "upload"
  | "download"
  | "copy"
  | "check"
  | "alert"
  | "info"
  | "close"
  | "chevron"
  | "refresh"
  | "text"
  | "image"
  | "file"
  | "trash";

// Paths are stroked, never filled, so one set reads correctly on both themes.
const paths: Record<IconName, string> = {
  sync: "M3 12a9 9 0 0 1 15.5-6.2M21 12a9 9 0 0 1-15.5 6.2M18 3v3h-3M6 21v-3h3",
  history: "M12 7v5l3 2M3.1 13a9 9 0 1 0 2.2-6.3M3 4v4h4",
  devices: "M4 5h10v9H4zM17 9h3v10h-6v-5M2 18h14",
  link: "M10 13a4 4 0 0 0 5.7.3l3-3A4 4 0 0 0 13 4.7l-1.4 1.4M14 11a4 4 0 0 0-5.7-.3l-3 3A4 4 0 0 0 11 19.3l1.4-1.4",
  settings:
    "M12 15.5a3.5 3.5 0 1 0 0-7 3.5 3.5 0 0 0 0 7zM19.4 15a1.7 1.7 0 0 0 .3 1.8l.1.1a2 2 0 1 1-2.8 2.8l-.1-.1a1.7 1.7 0 0 0-2.9 1.2v.2a2 2 0 1 1-4 0v-.1a1.7 1.7 0 0 0-3-1.2l-.1.1a2 2 0 1 1-2.8-2.8l.1-.1a1.7 1.7 0 0 0-1.2-2.9H3a2 2 0 1 1 0-4h.1A1.7 1.7 0 0 0 4.3 6l-.1-.1a2 2 0 1 1 2.8-2.8l.1.1a1.7 1.7 0 0 0 2.9-1.2V2a2 2 0 1 1 4 0v.1a1.7 1.7 0 0 0 2.9 1.2l.1-.1a2 2 0 1 1 2.8 2.8l-.1.1a1.7 1.7 0 0 0 1.2 2.9h.2a2 2 0 1 1 0 4h-.1a1.7 1.7 0 0 0-1.6 1z",
  upload: "M12 19V5M6 11l6-6 6 6",
  download: "M12 5v14M18 13l-6 6-6-6",
  copy: "M9 9h10v10H9zM5 15H4V4h11v1",
  check: "M4 12.5 9 18 20 6",
  alert: "M12 8v5M12 17h.01M10.3 3.9 2.4 17.5A2 2 0 0 0 4.1 20.5h15.8a2 2 0 0 0 1.7-3L13.7 3.9a2 2 0 0 0-3.4 0z",
  info: "M12 16v-5M12 8h.01M12 21a9 9 0 1 0 0-18 9 9 0 0 0 0 18z",
  close: "M6 6l12 12M18 6 6 18",
  chevron: "M9 6l6 6-6 6",
  refresh: "M20 11A8 8 0 0 0 6.3 5.7L3 9M4 13a8 8 0 0 0 13.7 5.3L21 15M21 5v4h-4M3 19v-4h4",
  text: "M5 6h14M5 11h14M5 16h9",
  image: "M4 5h16v14H4zM4 16l4.5-4.5 4 4L16 12l4 4M9.5 9.5a1 1 0 1 1-2 0 1 1 0 0 1 2 0z",
  file: "M14 3H7a2 2 0 0 0-2 2v14a2 2 0 0 0 2 2h10a2 2 0 0 0 2-2V8zM14 3v5h5",
  trash: "M4 7h16M10 11v6M14 11v6M6 7l1 13h10l1-13M9 7V4h6v3",
};

export function Icon({
  name,
  size = 18,
  ...rest
}: { name: IconName; size?: number } & Omit<SVGProps<SVGSVGElement>, "name">) {
  return (
    <svg
      width={size}
      height={size}
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth={1.75}
      strokeLinecap="round"
      strokeLinejoin="round"
      aria-hidden="true"
      focusable="false"
      {...rest}
    >
      <path d={paths[name]} />
    </svg>
  );
}
