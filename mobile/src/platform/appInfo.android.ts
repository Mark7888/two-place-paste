/**
 * Which build of the app this is, read from the installed APK.
 *
 * app/build.gradle sets the version name and version code from
 * scripts/version.sh, and the version code is the build's stamp. A local
 * build keeps the Gradle defaults, whose version code of 1 is not a stamp:
 * Android requires a positive code, so it stands in for the desktop's 0.
 */

import type { AppInfoPort, AppVersion } from './types';
import { TppAppInfo } from './native';

/** LOCAL_VERSION_CODE is the version code a build gets when CI does not pass one. */
const LOCAL_VERSION_CODE = 1;

export const appInfo: AppInfoPort = {
  async version(): Promise<AppVersion> {
    const info = await TppAppInfo?.read();
    if (info === undefined) {
      return { version: '', stamp: 0 };
    }
    return { version: info.version, stamp: info.stamp > LOCAL_VERSION_CODE ? info.stamp : 0 };
  },
};
