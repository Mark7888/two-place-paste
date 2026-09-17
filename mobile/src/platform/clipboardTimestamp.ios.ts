/**
 * iOS records no clipboard timestamp, so the direction is never inferred there
 * (SPEC §6, §7.3): the two explicit directional buttons are the interface.
 */

export async function clipboardTimestamp(): Promise<number | null> {
  return null;
}
