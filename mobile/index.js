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
import { name as appName } from './app.json';

AppRegistry.registerComponent(appName, () => App);
