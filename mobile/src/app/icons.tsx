/**
 * The icon set, drawn here rather than installed.
 *
 * `react-native-svg` is already a dependency — the pairing screen renders QR
 * codes with it — so an icon is one stroked path and no new package. Emoji are
 * deliberately not used for the tab bar or anywhere else: they are a different
 * picture on every Android version, they carry colour this theme cannot
 * control, and at tab-bar size they are unreadable.
 *
 * Every icon is decoration beside a label, so none of them is announced; the
 * label next to it is what a screen reader reads.
 */

import React from 'react';
import Svg, { Path } from 'react-native-svg';

export type IconName =
  | 'sync'
  | 'history'
  | 'devices'
  | 'link'
  | 'settings'
  | 'upload'
  | 'download'
  | 'check'
  | 'alert'
  | 'close'
  | 'open'
  | 'store'
  | 'plus'
  | 'tag'
  | 'edit'
  | 'trash'
  | 'search'
  | 'text'
  | 'image'
  | 'file';

const paths: Record<IconName, string> = {
  sync: 'M3 12a9 9 0 0 1 15.5-6.2M21 12a9 9 0 0 1-15.5 6.2M18 3v3h-3M6 21v-3h3',
  history: 'M12 7v5l3 2M3.1 13a9 9 0 1 0 2.2-6.3M3 4v4h4',
  devices: 'M4 5h10v9H4zM17 9h3v10h-6v-5M2 18h14',
  link: 'M10 13a4 4 0 0 0 5.7.3l3-3A4 4 0 0 0 13 4.7l-1.4 1.4M14 11a4 4 0 0 0-5.7-.3l-3 3A4 4 0 0 0 11 19.3l1.4-1.4',
  settings:
    'M12 15.5a3.5 3.5 0 1 0 0-7 3.5 3.5 0 0 0 0 7zM19.6 14.5a1.6 1.6 0 0 0 .3 1.8l.1.1a2 2 0 1 1-2.8 2.8l-.1-.1a1.6 1.6 0 0 0-2.7 1.1v.3a2 2 0 1 1-4 0v-.2a1.6 1.6 0 0 0-2.8-1.1l-.1.1a2 2 0 1 1-2.8-2.8l.1-.1a1.6 1.6 0 0 0-1.1-2.7H4a2 2 0 1 1 0-4h.2a1.6 1.6 0 0 0 1.1-2.8l-.1-.1a2 2 0 1 1 2.8-2.8l.1.1a1.6 1.6 0 0 0 2.7-1.1V3a2 2 0 1 1 4 0v.2a1.6 1.6 0 0 0 2.8 1.1l.1-.1a2 2 0 1 1 2.8 2.8l-.1.1a1.6 1.6 0 0 0 1.1 2.7h.3a2 2 0 1 1 0 4h-.2a1.6 1.6 0 0 0-1.1 1z',
  upload: 'M12 19V5M6 11l6-6 6 6',
  download: 'M12 5v14M18 13l-6 6-6-6',
  check: 'M4 12.5 9 18 20 6',
  alert: 'M12 8v5M12 17h.01M10.3 3.9 2.4 17.5A2 2 0 0 0 4.1 20.5h15.8a2 2 0 0 0 1.7-3L13.7 3.9a2 2 0 0 0-3.4 0z',
  close: 'M6 6l12 12M18 6 6 18',
  open: 'M14 4h6v6M20 4l-9 9M18 14v5a1 1 0 0 1-1 1H5a1 1 0 0 1-1-1V7a1 1 0 0 1 1-1h5',
  store: 'M3 4h18v4H3zM5 8v12h14V8M10 12h4',
  plus: 'M12 5v14M5 12h14',
  tag: 'M3 12V3h9l9 9-9 9zM7.5 7.5h.01',
  edit: 'M4 20h4L19 9l-4-4L4 16zM14 6l4 4',
  trash: 'M4 7h16M10 11v6M14 11v6M6 7l1 13h10l1-13M9 7V4h6v3',
  search: 'M11 18a7 7 0 1 0 0-14 7 7 0 0 0 0 14zM20 20l-4-4',
  text: 'M5 6h14M5 11h14M5 16h9',
  image: 'M4 5h16v14H4zM4 16l4.5-4.5 4 4L16 12l4 4M9.5 9.5a1 1 0 1 1-2 0 1 1 0 0 1 2 0z',
  file: 'M14 3H7a2 2 0 0 0-2 2v14a2 2 0 0 0 2 2h10a2 2 0 0 0 2-2V8zM14 3v5h5',
};

export function Icon({
  name,
  size = 22,
  color,
}: {
  name: IconName;
  size?: number;
  color: string;
}): React.JSX.Element {
  return (
    <Svg width={size} height={size} viewBox="0 0 24 24" fill="none">
      <Path
        d={paths[name]}
        stroke={color}
        strokeWidth={1.8}
        strokeLinecap="round"
        strokeLinejoin="round"
      />
    </Svg>
  );
}
