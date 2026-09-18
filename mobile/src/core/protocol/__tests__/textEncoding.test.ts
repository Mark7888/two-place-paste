/**
 * What this client does on a runtime without the Text Encoding API.
 *
 * This is the one test in the suite that has to lie about the runtime it is
 * on. Node has `TextEncoder` and `TextDecoder`; Hermes has the first and not
 * the second, and `@bufbuild/protobuf` builds both the first time any message
 * is encoded or decoded. That difference shipped a build in which neither way
 * into a group worked — creating one reported "undefined cannot be used as a
 * constructor", and scanning a desktop's code reported that it was not a code
 * — while every test here passed.
 *
 * So these tests take the globals away for the duration and run the two flows
 * the user actually hit. Anything that asserts against a real `TextDecoder` is
 * asserting about Node, not about the app.
 */

/** TEXT_ENCODING is where @bufbuild/protobuf memoizes its encoder and decoder. */
const TEXT_ENCODING = Symbol.for('@bufbuild/protobuf/text-encoding');

type Globals = Record<string | symbol, unknown>;

/**
 * onHermes runs `body` with the globals a phone has: `TextEncoder` present,
 * `TextDecoder` gone, and nothing memoized. The modules are loaded inside,
 * after the teardown, so that importing them is what has to survive it.
 */
function onHermes(body: () => void): void {
  const globals = globalThis as unknown as Globals;
  const decoder = globals.TextDecoder;
  const memo = globals[TEXT_ENCODING];
  delete globals.TextDecoder;
  delete globals[TEXT_ENCODING];
  jest.resetModules();
  try {
    body();
  } finally {
    globals.TextDecoder = decoder;
    globals[TEXT_ENCODING] = memo;
    jest.resetModules();
  }
}

describe('a runtime without TextDecoder', () => {
  it('is what broke: the codec builds one before it reads a byte', () => {
    onHermes(() => {
      // eslint-disable-next-line @typescript-eslint/no-require-imports
      const { BinaryWriter } = require('@bufbuild/protobuf/wire') as {
        BinaryWriter: new () => unknown;
      };
      expect(() => new BinaryWriter()).toThrow(TypeError);
    });
  });

  it('encodes and decodes a pairing invitation anyway', () => {
    onHermes(() => {
      // eslint-disable-next-line @typescript-eslint/no-require-imports
      const pairing = require('../pairing') as typeof import('../pairing');
      const code = pairing.encodePairingPayload({
        serverUrl: 'https://tpp.example.com',
        pairingToken: 'a-token-with-π-and-🔑',
        inviterEphemeralPublicKey: new Uint8Array([1, 2, 3]),
      });
      const back = pairing.decodePairingPayload(code);

      expect(back.serverUrl).toBe('https://tpp.example.com');
      expect(back.pairingToken).toBe('a-token-with-π-and-🔑');
      expect(Array.from(back.inviterEphemeralPublicKey)).toEqual([1, 2, 3]);
      // The failure the user saw: a throwing decode reads as "not a code".
      expect(pairing.classifyCode(code)).toBe('pairing-code');
    });
  });

  it('encodes the request that creates a group', () => {
    onHermes(() => {
      // eslint-disable-next-line @typescript-eslint/no-require-imports
      require('../client');
      // eslint-disable-next-line @typescript-eslint/no-require-imports
      const { CreateGroupRequest } = require('../../../protocol/gen/tpp/v1/group') as
        typeof import('../../../protocol/gen/tpp/v1/group');

      const bytes = CreateGroupRequest.encode({
        token: 'creation-token',
        devicePublicKey: new Uint8Array(32),
        wrappedGroupKey: new Uint8Array(81),
        deviceName: 'Pixel 8',
      }).finish();

      expect(CreateGroupRequest.decode(bytes).deviceName).toBe('Pixel 8');
    });
  });
});
