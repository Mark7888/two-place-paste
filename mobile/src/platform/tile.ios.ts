/**
 * There is no quick-settings tile on iOS (SPEC §7.3). The port exists so that
 * the app's startup path is one path on both platforms; on iOS it reports that
 * there is no tile and does nothing.
 */

import type { TilePort } from './types';

export const tile: TilePort = {
  available: false,
  requests: () => () => {},
  report: () => {},
  pending: async () => false,
  // No tile means no overlay to close.
  closeOverlay: () => {},
};
