// Flat config. The rules that matter here are the ones that catch what a
// typecheck does not: a floating promise in a screen, an `any` that slipped in,
// a console call that would put an error — and one day a key — into logcat.
import js from '@eslint/js';
import tseslint from 'typescript-eslint';

export default tseslint.config(
  { ignores: ['node_modules/**', 'android/**', 'src/protocol/gen/**', 'coverage/**'] },
  js.configs.recommended,
  ...tseslint.configs.recommendedTypeChecked,
  {
    languageOptions: {
      parserOptions: { projectService: true, tsconfigRootDir: import.meta.dirname },
      globals: {
        console: 'readonly',
        fetch: 'readonly',
        setTimeout: 'readonly',
        clearTimeout: 'readonly',
        queueMicrotask: 'readonly',
        WebSocket: 'readonly',
        MessageEvent: 'readonly',
        CloseEvent: 'readonly',
        Crypto: 'readonly',
        __dirname: 'readonly',
        require: 'readonly',
        module: 'writable',
      },
    },
    rules: {
      // The log is part of the threat model (docs/conventions.md §2): this app
      // has no logger of its own, and console output on Android is logcat,
      // which every app on the device with the right tooling can read.
      'no-console': 'error',
      '@typescript-eslint/no-explicit-any': 'error',
      '@typescript-eslint/no-floating-promises': 'error',
      '@typescript-eslint/no-non-null-assertion': 'error',
      // An `async` method that happens to have nothing to await is how a
      // synchronous implementation satisfies an asynchronous port — the
      // in-memory store, the iOS clipboard. That is not a defect.
      '@typescript-eslint/require-await': 'off',
    },
  },
  {
    // Tests may assert on a vector field the fixture guarantees is present.
    files: ['**/__tests__/**', '**/*.test.ts'],
    rules: { '@typescript-eslint/no-non-null-assertion': 'off' },
  },
  {
    // Config files and the interop script are plain JavaScript: no type
    // information to lint with, and the script is a command-line tool whose
    // output is the point.
    files: ['**/*.js', '**/*.mjs'],
    ...tseslint.configs.disableTypeChecked,
    rules: {
      ...tseslint.configs.disableTypeChecked.rules,
      'no-console': 'off',
      '@typescript-eslint/no-require-imports': 'off',
    },
  },
);
