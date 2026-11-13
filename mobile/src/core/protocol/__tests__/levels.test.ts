/**
 * Protocol levels and relay capabilities (SPEC §5.3): the relay refusing an
 * app too old for it, a relay too old to answer Hello, and a pairing code
 * from a newer app. The Go client's protocol tests cover the same ground.
 */

import { BinaryWriter } from '@bufbuild/protobuf/wire';

import { toBase64URL } from '../../bytes';
import { Client } from '../client';
import { TppError, describe as describeError, isClientOutdated } from '../errors';
import { refusalForAge } from '../levels';
import { decodePairingCode } from '../pairing';
import { FakeRelay, MemoryStore } from '../../demo';

const opened: Client[] = [];

afterEach(() => {
  while (opened.length > 0) {
    opened.pop()?.disconnect();
  }
});

async function open(relay: FakeRelay, handlers: Parameters<typeof Client.open>[0]['handlers'] = {}): Promise<Client> {
  const client = await Client.open({
    store: new MemoryStore(),
    deviceName: 'phone',
    socketFactory: relay.factory,
    // A healthy relay: a refusal it explains must not be guessed to be a revocation.
    healthCheck: async () => true,
    deviceStatus: relay.deviceStatus,
    handlers,
  });
  opened.push(client);
  return client;
}

async function kindOf(work: Promise<unknown>): Promise<string> {
  try {
    await work;
  } catch (err) {
    return err instanceof TppError ? err.kind : String(err);
  }
  return 'resolved';
}

describe('a refusal for age (SPEC §5.3)', () => {
  test.each([
    { name: 'the relay’s refusal', status: 426, body: '{"error":"client_outdated","protocol_min":2,"protocol_max":3}', want: true },
    { name: 'the 426 every plain GET gets', status: 426, body: 'WebSocket protocol violation', want: false },
    { name: 'another JSON error', status: 426, body: '{"error":"something_else"}', want: false },
    { name: 'the body on another status', status: 401, body: '{"error":"client_outdated"}', want: false },
  ])('is told by its body: $name', ({ status, body, want }) => {
    expect(refusalForAge(status, body) !== null).toBe(want);
  });

  test('is not a revocation, is reported once, and ends when the app is let in', async () => {
    const relay = new FakeRelay();
    let outdated = 0;
    let probablyRevoked = 0;
    const phone = await open(relay, {
      onClientOutdated: () => outdated++,
      onProbablyRevoked: () => probablyRevoked++,
    });
    await phone.createGroup(relay.creationURL);
    phone.disconnect();

    relay.protocolMin = 2;
    expect(await kindOf(phone.connect())).toBe('client-outdated');
    expect(await kindOf(phone.connect())).toBe('client-outdated');
    expect(outdated).toBe(1);
    expect(probablyRevoked).toBe(0);
    expect(phone.isOutdated).toBe(true);
    expect(phone.inGroup).toBe(true);

    // The app is updated: here, the relay lets this level in again.
    relay.protocolMin = 1;
    await phone.connect();
    expect(phone.isOutdated).toBe(false);
  });

  test('reaches the pairing and group-creation sockets too', async () => {
    const relay = new FakeRelay({ protocolMin: 2 });
    const phone = await open(relay);
    expect(await kindOf(phone.createGroup(relay.creationURL))).toBe('client-outdated');
  });

  test('reads as "update the app"', () => {
    const err = new TppError('client-outdated', 'the relay needs a newer version of this app');
    expect(isClientOutdated(err)).toBe(true);
    expect(describeError(err)).toMatch(/newer version of this app/);
  });
});

describe('Hello (SPEC §5.3)', () => {
  test('a relay states its levels', async () => {
    const relay = new FakeRelay();
    const phone = await open(relay);
    await phone.createGroup(relay.creationURL);
    expect(await phone.relay()).toEqual({ protocolMin: 1, protocolMax: 1, capabilities: [] });
  });

  test('a relay that predates it is level 1 with nothing extra', async () => {
    const relay = new FakeRelay({ hello: false });
    const phone = await open(relay);
    await phone.createGroup(relay.creationURL);
    expect(await phone.relay()).toEqual({ protocolMin: 1, protocolMax: 1, capabilities: [] });
  });
});

function kindThrown(work: () => unknown): string {
  try {
    work();
  } catch (err) {
    return err instanceof TppError ? err.kind : String(err);
  }
  return 'returned';
}

describe('a pairing code from a newer app', () => {
  function code(write: (w: BinaryWriter) => void): string {
    const writer = new BinaryWriter();
    write(writer);
    return toBase64URL(writer.finish());
  }

  test('says to update this app instead of calling the code invalid', () => {
    // A PairingCode member this build does not know, carrying some message.
    const newer = code((w) => w.uint32((9 << 3) | 2).bytes(new Uint8Array([10, 1, 120])));
    expect(kindThrown(() => decodePairingCode(newer))).toBe('code-from-newer-app');
    expect(describeError(new TppError('code-from-newer-app', 'x'))).toMatch(/newer version of TwoPlacePaste/);
  });

  test('is not guessed from stray bytes', () => {
    const stray = code((w) => w.uint32(9 << 3).uint32(1));
    expect(kindThrown(() => decodePairingCode(stray))).toBe('invalid');
  });
});
