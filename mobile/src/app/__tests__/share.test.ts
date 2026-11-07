import { hashHex, ProtocolError, sha256Sliced, STORE_LIMITS, utf8, type Limits, type StoreLimits } from '../../core';
import { ErrorCode } from '../../protocol/gen/tpp/v1/error';
import type { ClipboardItem, Shared, StageResult } from '../../platform/types';
import {
  describePayload,
  describeShared,
  payloadFor,
  payloadHash,
  prepareSend,
  prepareStore,
  STORE_OFF,
  suggestedName,
  TEXT_CONTENT_TYPE,
  UNSUPPORTED,
  type Payload,
  type ShareClient,
} from '../share';

const MB = 1 << 20;

function fileShare(over: Partial<Extract<Shared, { kind: 'file' }>> = {}): Shared {
  return { id: 7, kind: 'file', name: 'report.pdf', contentType: 'application/pdf', size: 3, previewUri: '', ...over };
}

function textShare(text: string, subject = ''): Shared {
  return { id: 8, kind: 'text', text, subject };
}

/**
 * stager is a fake `share.stage` for a file of `size` bytes: it stages it
 * when it fits, and records its calls and the copies discarded.
 */
function stager(size: number) {
  const calls: { id: number; maxBytes: number }[] = [];
  const discarded: string[] = [];
  const stage = async (id: number, maxBytes: number): Promise<StageResult> => {
    calls.push({ id, maxBytes });
    if (size > maxBytes) {
      return { tooLarge: true, name: 'x', size };
    }
    return { tooLarge: false, token: 'tok', name: 'x', contentType: 'application/pdf', size, sha256: 'ab12' };
  };
  return { stage, discard: (t: string) => discarded.push(t), calls, discarded };
}

/** fakeClient answers with the limits it is given and records what is put. */
function fakeClient(opts: { maxEntryBytes?: number; store?: Partial<StoreLimits> | Error } = {}) {
  const put: ClipboardItem[] = [];
  const client: ShareClient = {
    connect: async () => {},
    hold: <T>(run: () => Promise<T>) => run(),
    limits: async (): Promise<Limits> => ({
      maxEntryBytes: opts.maxEntryBytes ?? 32 * MB,
      singleFrameMaxBytes: 10 * MB,
      chunkBytes: MB,
      inlineMaxBytes: 0,
      headerV2: true,
    }),
    putEntry: async (item) => {
      put.push({ contentType: item.contentType, filename: item.filename ?? '', body: item.body });
      return {} as Awaited<ReturnType<ShareClient['putEntry']>>;
    },
    storeLimits: async (): Promise<StoreLimits> => {
      if (opts.store instanceof Error) {
        throw opts.store;
      }
      return {
        enabled: true,
        maxBytes: 0,
        usedBytes: 0,
        availableBytes: 5 * MB,
        maxItems: 0,
        usedItems: 0,
        maxItemBytes: 5 * MB,
        ...opts.store,
      };
    },
  };
  return { client, put };
}

describe('payloadFor', () => {
  it('carries shared text whole, as a text entry like copied text', async () => {
    const f = stager(0);
    const got = await payloadFor(textShare('héllo'), 100, 'limit', f.stage, f.discard);
    expect(got).toEqual({ kind: 'text', item: { contentType: TEXT_CONTENT_TYPE, filename: '', body: utf8('héllo') } });
    // Text is in the share itself: nothing is staged.
    expect(f.calls).toHaveLength(0);
  });

  it('stages a file within the limit, keeping the name and type the panel showed', async () => {
    const f = stager(3);
    const got = await payloadFor(fileShare(), 100, 'limit', f.stage, f.discard);
    expect(got).toEqual({
      kind: 'file',
      file: { token: 'tok', name: 'x', contentType: 'application/pdf', size: 3, sha256: 'ab12' },
      contentType: 'application/pdf',
      filename: 'report.pdf',
    });
    expect(f.calls).toEqual([{ id: 7, maxBytes: 100 }]);
  });

  const refusals: { name: string; shared: Shared; size: number; max: number; want: string; stages: number }[] = [
    {
      name: 'a file the sender says is too large, before copying it',
      shared: fileShare({ size: 2 * MB }),
      size: 2 * MB,
      max: MB,
      want: 'This file is 2.0 MB; limit 1.0 MB.',
      stages: 0,
    },
    {
      name: 'a file of unknown size that turns out too large',
      shared: fileShare({ size: -1 }),
      size: 2 * MB,
      max: MB,
      want: 'This file is 2.0 MB; limit 1.0 MB.',
      stages: 1,
    },
    { name: 'text over the limit', shared: textShare('abcdef'), size: 0, max: 3, want: 'This text is 6 B; limit 3 B.', stages: 0 },
    { name: 'something this app will not read', shared: { id: 9, kind: 'unsupported' }, size: 0, max: MB, want: UNSUPPORTED, stages: 0 },
  ];
  it.each(refusals)('refuses $name', async ({ shared, size, max, want, stages }) => {
    const f = stager(size);
    await expect(payloadFor(shared, max, 'limit', f.stage, f.discard)).rejects.toThrow(want);
    expect(f.calls).toHaveLength(stages);
  });

  it('refuses an empty file and deletes its copy', async () => {
    const f = stager(0);
    await expect(payloadFor(fileShare({ size: -1 }), MB, 'limit', f.stage, f.discard)).rejects.toThrow('That file is empty.');
    expect(f.discarded).toEqual(['tok']);
  });

  it('never names the file in a refusal', async () => {
    const f = stager(2 * MB);
    await expect(
      payloadFor(fileShare({ name: 'secret-plans.pdf', size: 2 * MB }), MB, 'limit', f.stage, f.discard),
    ).rejects.toThrow(/^(?!.*secret-plans).*$/);
  });
});

describe('prepareSend', () => {
  it('stages within the relay’s entry limit and uploads nothing itself', async () => {
    const { client, put } = fakeClient({ maxEntryBytes: MB });
    const f = stager(3);
    await prepareSend(client, fileShare(), f.stage, f.discard);
    expect(f.calls).toEqual([{ id: 7, maxBytes: MB }]);
    expect(put).toHaveLength(0);
  });

  it('says what the relay takes when the file is over it', async () => {
    const { client } = fakeClient({ maxEntryBytes: MB });
    const f = stager(2 * MB);
    await expect(prepareSend(client, fileShare({ size: -1 }), f.stage, f.discard)).rejects.toThrow(
      'the relay takes at most 1.0 MB',
    );
  });
});

describe('prepareStore', () => {
  it('stages within the room the store has left', async () => {
    const { client } = fakeClient({ store: { availableBytes: 4 } });
    const f = stager(3);
    await prepareStore(client, fileShare(), f.stage, f.discard);
    expect(f.calls).toEqual([{ id: 7, maxBytes: 4 }]);
  });

  it('says how much room there is when the file does not fit', async () => {
    const { client } = fakeClient({ store: { availableBytes: 2 } });
    const f = stager(3);
    await expect(prepareStore(client, fileShare({ size: 3 }), f.stage, f.discard)).rejects.toThrow(
      'This file is 3 B; the store has room for 2 B.',
    );
  });

  const off: { name: string; store: Partial<StoreLimits> | Error }[] = [
    { name: 'reports it disabled', store: { enabled: false } },
    { name: 'refuses with STORE_DISABLED', store: new ProtocolError(ErrorCode.ERROR_CODE_STORE_DISABLED, 'store disabled') },
  ];
  it.each(off)('says the store is off when the relay $name', async ({ store }) => {
    const { client } = fakeClient({ store });
    const f = stager(3);
    await expect(prepareStore(client, fileShare(), f.stage, f.discard)).rejects.toThrow(STORE_OFF);
    expect(f.calls).toHaveLength(0);
  });
});

describe('payloadHash', () => {
  it('takes a staged file’s hash from the copy, and hashes text itself', async () => {
    const file: Payload = {
      kind: 'file',
      file: { token: 't', name: 'x', contentType: 'a/b', size: 1, sha256: 'ab12' },
      contentType: 'a/b',
      filename: 'x',
    };
    expect(await payloadHash(file)).toBe('ab12');
    const body = utf8('hello');
    const text: Payload = { kind: 'text', item: { contentType: TEXT_CONTENT_TYPE, filename: '', body } };
    expect(await payloadHash(text)).toBe(hashHex(await sha256Sliced(body)));
  });
});

describe('describePayload', () => {
  it('names a file with its bare type and size, and text by its size', () => {
    expect(
      describePayload({
        kind: 'file',
        file: { token: 't', name: 'x', contentType: 'a/b', size: 2048, sha256: '' },
        contentType: 'text/csv; charset=utf-8',
        filename: 'a.csv',
      }),
    ).toBe('a.csv (text/csv, 2.0 KB)');
    expect(describePayload({ kind: 'text', item: { contentType: TEXT_CONTENT_TYPE, filename: '', body: utf8('abc') } })).toBe(
      'text, 3 B',
    );
  });
});

describe('describeShared', () => {
  const cases: { name: string; shared: Shared; title: string; detail: string }[] = [
    {
      name: 'a file by name, bare type and size',
      shared: fileShare({ contentType: 'text/csv; charset=utf-8', name: 'a.csv', size: 2048 }),
      title: 'a.csv',
      detail: 'text/csv · 2.0 KB',
    },
    {
      name: 'a file of unknown size without one',
      shared: fileShare({ size: -1 }),
      title: 'report.pdf',
      detail: 'application/pdf',
    },
    {
      name: 'text by its first non-empty line',
      shared: textShare('\n  first line \nsecond'),
      title: 'first line',
      detail: 'Text · 21 B',
    },
  ];
  it.each(cases)('describes $name', ({ shared, title, detail }) => {
    expect(describeShared(shared)).toEqual({ title, detail });
  });
});

describe('suggestedName', () => {
  const cases: { name: string; shared: Shared; want: string }[] = [
    { name: 'a file by its name', shared: fileShare(), want: 'report.pdf' },
    { name: 'text by its subject', shared: textShare('https://example.com', ' A page '), want: 'A page' },
    { name: 'text without a subject by its first line', shared: textShare('\nhello\nworld'), want: 'hello' },
    { name: 'nothing for an unsupported share', shared: { id: 1, kind: 'unsupported' }, want: '' },
  ];
  it.each(cases)('suggests $name', ({ shared, want }) => {
    expect(suggestedName(shared)).toBe(want);
  });

  it('cuts a long name to the store’s limit, by whole characters', () => {
    const got = suggestedName(fileShare({ name: 'é'.repeat(STORE_LIMITS.name) }));
    expect(utf8(got).length).toBeLessThanOrEqual(STORE_LIMITS.name);
    expect(got).toBe('é'.repeat(STORE_LIMITS.name / 2));
  });
});
