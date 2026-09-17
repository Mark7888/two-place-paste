/** What this device calls itself in every other device's revocation dialog (SPEC §3.3 step 2). */

import type { DeviceInfoPort } from './types';

export const deviceInfo: DeviceInfoPort = {
  defaultName(): string {
    return 'iPhone';
  },
};
