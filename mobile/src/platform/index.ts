/**
 * The platform layer.
 *
 * Everything the app does that is not protocol or crypto goes through here,
 * and every implementation is a per-OS file Metro resolves by extension. The
 * app imports `clipboard`, not `Clipboard`; `secureStore`, not `Keychain`.
 * That is the rule that keeps iOS an addition rather than a restructuring
 * (SPEC §7.3, docs/conventions.md §11).
 */

export { clipboard } from './clipboard';
export { secureStore } from './secureStore';
export { deviceInfo } from './deviceInfo';
export { tile } from './tile';
export { clipboardTimestamp } from './clipboardTimestamp';
export * from './types';
