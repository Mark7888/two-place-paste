const { getDefaultConfig, mergeConfig } = require('@react-native/metro-config');

/**
 * Metro bundles `src/` for the app. The crypto vectors under `/spec` are read
 * by the Jest tests through the filesystem and are deliberately not part of
 * the bundle: nothing the app ships needs them.
 *
 * @type {import('@react-native/metro-config').MetroConfig}
 */
module.exports = mergeConfig(getDefaultConfig(__dirname), {});
