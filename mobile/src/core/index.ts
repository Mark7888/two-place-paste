/**
 * The core of the TwoPlacePaste client: the crypto profile and the protocol
 * client.
 *
 * Nothing under `src/core` imports React or React Native. That is what lets it
 * run unchanged in three places — the app on Hermes, the quick-settings tile's
 * sync, and Node, where the vector tests and the interop script exercise it —
 * and it is why the platform surface lives behind `src/platform` (SPEC §7.3).
 */

export * from './bytes';
export * from './random';
export * from './crypto';
export * from './protocol';
