/**
 * Jest runs the core: the crypto profile, the protocol client and the state
 * machine around them — everything under `src/core`, which is deliberately
 * free of React Native imports so that it runs in Node exactly as it runs on
 * Hermes.
 *
 * The screens are not unit tested. What would be asserted there is that a
 * button calls a core method, and the core methods are covered directly; a
 * renderer test would restate the wiring rather than check it.
 */
module.exports = {
  testEnvironment: 'node',
  roots: ['<rootDir>/src/core'],
  // Only *.test.ts is a test; the fake relay next to them is a helper.
  testMatch: ['**/*.test.ts'],
  // @noble/* ship ESM only (spec/crypto.md §2.2 pins them), so they are
  // transformed rather than ignored like the rest of node_modules.
  transformIgnorePatterns: ['node_modules/(?!@noble/)'],
  transform: {
    '\\.[jt]sx?$': ['babel-jest', { configFile: require.resolve('./babel.config.js') }],
  },
};
