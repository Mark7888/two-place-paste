/**
 * When a build was made, from its stamp.
 *
 * Every CI build gets a stamp: whole seconds since 2025-01-01T00:00:00Z
 * (scripts/version.sh). On Android it is the APK's version code; the desktop
 * reads the same number from its update manifest and shows it as "Built", so
 * the two apps describe a build the same way.
 */

/** STAMP_EPOCH is the instant stamps count from, in UTC milliseconds. */
const STAMP_EPOCH = Date.UTC(2025, 0, 1);

/** builtAt returns when a build with this stamp was made, or null for a local build (stamp 0). */
export function builtAt(stamp: number): Date | null {
  return Number.isSafeInteger(stamp) && stamp > 0 ? new Date(STAMP_EPOCH + stamp * 1000) : null;
}
