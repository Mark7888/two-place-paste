/**
 * The interop test's own configuration.
 *
 * It is separate from `jest.config.js` because this suite needs Go and Redis on
 * the machine and takes minutes, while `npm test` must stay a fast check that
 * runs anywhere — including the Node CI job, which has neither.
 */
const base = require('./jest.config');

module.exports = {
  ...base,
  roots: ['<rootDir>/src/interop'],
  // The relay, the Go peer and the phone are one shared world.
  maxWorkers: 1,
};
