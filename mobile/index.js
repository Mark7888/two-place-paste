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
import SyncOverlay from './src/app/SyncOverlay';
import { init } from './src/app/sessionStore';
import { name as appName, syncComponentName } from './app.json';

/*
 * Two components, one session.
 *
 * `App` is the app; `SyncOverlay` is the small panel a quick-settings tap puts
 * over whatever the user is doing. They are separate Android activities and
 * therefore separate React surfaces, but they share this JavaScript context —
 * so the session is opened here, once, rather than by whichever of them
 * happens to mount. That is what lets a tile tap be answered without the whole
 * app coming to the front, and it is why the tile handler is no longer an
 * effect inside a screen.
 */
init();

AppRegistry.registerComponent(appName, () => App);
AppRegistry.registerComponent(syncComponentName, () => SyncOverlay);
