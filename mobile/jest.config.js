/**
 * Jest runs the core: the crypto profile, the protocol client and the state
 * machine around them — everything under `src/core`, which is deliberately
 * free of React Native imports so that it runs in Node exactly as it runs on
 * Hermes.
 *
 * `src/app` is in scope for the same reason, not as an exception to it: the
 * pure modules there — how an entry is classified for display, how its bytes
 * become a data URI — import nothing from React Native either, and the
 * classification has to agree with the desktop's or the two clients describe
 * the same entry differently.
 *
 * The screens themselves are still not unit tested. What would be asserted
 * there is that a button calls a core method, and the core methods are covered
 * directly; a renderer test would restate the wiring rather than check it.
 */
module.exports = {
  testEnvironment: 'node',
  roots: ['<rootDir>/src/core', '<rootDir>/src/app'],
  // Only *.test.ts is a test; the fake relay next to them is a helper.
  testMatch: ['**/*.test.ts'],
  // @noble/* ship ESM only (spec/crypto.md §2.2 pins them), so they are
  // transformed rather than ignored like the rest of node_modules.
  transformIgnorePatterns: ['node_modules/(?!@noble/)'],
  transform: {
    '\\.[jt]sx?$': ['babel-jest', { configFile: require.resolve('./babel.config.js') }],
  },
};
