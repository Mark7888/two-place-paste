/**
 * When this device's clipboard was last set (SPEC §6).
 *
 * Android records it: `ClipDescription.getTimestamp()` has been there since
 * API 26 and is what makes a direction decision possible on this platform at
 * all — neither macOS nor Windows exposes an equivalent, which is why the
 * desktop client has two directional buttons and no automatic direction.
 *
 * A device that answers 0, or a build with no native module, gets `null`: the
 * app then asks the user rather than guessing.
 */

import { TppClipboard } from './native';

export async function clipboardTimestamp(): Promise<number | null> {
  if (TppClipboard === null) {
    return null;
  }
  const ms = await TppClipboard.primaryClipTimestamp();
  return ms > 0 ? ms : null;
}
