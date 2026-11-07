/**
 * The app's entry point.
 *
 * `react-native-get-random-values` is imported first and on its own line: it
 * installs `crypto.getRandomValues` over the platform CSPRNG, and every key,
 * nonce and identifier in this client comes from there (spec/crypto.md §2.2).
 * A module that reached the crypto before this ran would find no CSPRNG and
 * refuse to work, which is the intended failure — but it must never happen at
 * all.
 */

import 'react-native-get-random-values';

import { AppRegistry } from 'react-native';

import App from './src/app/App';
import { backgroundTask } from './src/app/backgroundJobs';
import ShareOverlay from './src/app/ShareOverlay';
import SyncOverlay from './src/app/SyncOverlay';
import { init } from './src/app/sessionStore';
import { initShare } from './src/app/shareState';
import { name as appName, shareComponentName, syncComponentName, transferTaskName } from './app.json';

/*
 * Three components, one session.
 *
 * `App` is the app; `SyncOverlay` is the small panel a quick-settings tap puts
 * over whatever the user is doing. They are separate Android activities and
 * therefore separate React surfaces, but they share this JavaScript context —
 * so the session is opened here, once, rather than by whichever of them
 * happens to mount. That is what lets a tile tap be answered without the whole
 * app coming to the front, and it is why the tile handler is no longer an
 * effect inside a screen.
 *
 * `ShareOverlay` is the third: the panel "Share → TwoPlacePaste" opens. The
 * share it asks about is listened for here too, for the same reason — the
 * share may be what started this process.
 *
 * The headless task is what runs the uploads the share panel hands off, after
 * it has closed (`src/app/backgroundJobs.ts`). It runs on this same context
 * too, so it uses the same client.
 */
init();
initShare();

AppRegistry.registerComponent(appName, () => App);
AppRegistry.registerComponent(syncComponentName, () => SyncOverlay);
AppRegistry.registerComponent(shareComponentName, () => ShareOverlay);
AppRegistry.registerHeadlessTask(transferTaskName, () => backgroundTask);
