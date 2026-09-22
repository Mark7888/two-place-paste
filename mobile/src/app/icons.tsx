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
  | 'close';

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
