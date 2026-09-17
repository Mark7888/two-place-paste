// Metro uses this preset to bundle the app; babel-jest uses it to run the core
// tests in Node. One config, so a file that compiles for the device compiles
// for the test run.
module.exports = {
  presets: ['module:@react-native/babel-preset'],
};
