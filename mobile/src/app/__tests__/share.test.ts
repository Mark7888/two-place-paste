import { ProtocolError, STORE_LIMITS, utf8, type Limits, type StoreLimits } from '../../core';
import { ErrorCode } from '../../protocol/gen/tpp/v1/error';
import type { ClipboardItem, PickedFile, Shared } from '../../platform/types';
import {
  describeShared,
  itemFor,
  sendShared,
  SENT,
  STORE_OFF,
  storeItemFor,
  suggestedName,
  TEXT_CONTENT_TYPE,
  UNSUPPORTED,
  type ReadShared,
  type ShareClient,
} from '../share';

const MB = 1 << 20;

function fileShare(over: Partial<Extract<Shared, { kind: 'file' }>> = {}): Shared {
  return { id: 7, kind: 'file', name: 'report.pdf', contentType: 'application/pdf', size: 3, previewUri: '', ...over };
}

function textShare(text: string, subject = ''): Shared {
  return { id: 8, kind: 'text', text, subject };
}

/** reader is a fake `share.read` that hands out `body`, honouring the limit, and counts its calls. */
function reader(body: Uint8Array): ReadShared & { calls: { id: number; maxBytes: number }[] } {
  const calls: { id: number; maxBytes: number }[] = [];
  const read = async (id: number, maxBytes: number): Promise<PickedFile> => {
    calls.push({ id, maxBytes });
    if (body.length > maxBytes) {
      return { tooLarge: true, name: 'x', size: body.length };
    }
    return { tooLarge: false, name: 'x', contentType: 'application/pdf', body };
  };
  return Object.assign(read, { calls });
}

/** fakeClient records what a share puts and answers with the limits it is given. */
function fakeClient(opts: { maxEntryBytes?: number; store?: Partial<StoreLimits> | Error } = {}) {
  const put: ClipboardItem[] = [];
  const client: ShareClient = {
    connect: async () => {},
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

describe('itemFor', () => {
  it('turns shared text into a text entry, the same as copied text', async () => {
    const read = reader(new Uint8Array(0));
    const got = await itemFor(textShare('héllo'), 100, 'limit', read);
    expect(got.contentType).toBe(TEXT_CONTENT_TYPE);
    expect(got.filename).toBe('');
    expect(got.body).toEqual(utf8('héllo'));
    // Text is carried in the share itself: nothing is read.
    expect(read.calls).toHaveLength(0);
  });

  it('keeps a file’s name and type as the panel showed them', async () => {
    const read = reader(utf8('pdf'));
    const got = await itemFor(fileShare(), 100, 'limit', read);
    expect(got).toEqual({ contentType: 'application/pdf', filename: 'report.pdf', body: utf8('pdf') });
    expect(read.calls).toEqual([{ id: 7, maxBytes: 100 }]);
  });

  const refusals: { name: string; shared: Shared; body: Uint8Array; max: number; want: string; reads: number }[] = [
    {
      name: 'a file the sender says is too large, before reading it',
      shared: fileShare({ size: 2 * MB }),
      body: new Uint8Array(0),
      max: MB,
      want: 'This file is 2.0 MB; limit 1.0 MB.',
      reads: 0,
    },
    {
      name: 'a file of unknown size that turns out too large',
      shared: fileShare({ size: -1 }),
      body: new Uint8Array(2 * MB),
      max: MB,
      want: 'This file is 2.0 MB; limit 1.0 MB.',
      reads: 1,
    },
    {
      name: 'an empty file',
      shared: fileShare({ size: -1 }),
      body: new Uint8Array(0),
      max: MB,
      want: 'That file is empty.',
      reads: 1,
    },
    {
      name: 'text over the limit',
      shared: textShare('abcdef'),
      body: new Uint8Array(0),
      max: 3,
      want: 'This text is 6 B; limit 3 B.',
      reads: 0,
    },
    {
      name: 'something this app will not read',
      shared: { id: 9, kind: 'unsupported' },
      body: new Uint8Array(0),
      max: MB,
      want: UNSUPPORTED,
      reads: 0,
    },
  ];
  it.each(refusals)('refuses $name', async ({ shared, body, max, want, reads }) => {
    const read = reader(body);
    await expect(itemFor(shared, max, 'limit', read)).rejects.toThrow(want);
    expect(read.calls).toHaveLength(reads);
  });

  it('never names the file in a refusal', async () => {
    const read = reader(new Uint8Array(0));
    await expect(itemFor(fileShare({ name: 'secret-plans.pdf', size: 2 * MB }), MB, 'limit', read)).rejects.toThrow(
      /^(?!.*secret-plans).*$/,
    );
  });
});

describe('sendShared', () => {
  it('puts the share up as the group’s latest entry', async () => {
    const { client, put } = fakeClient();
    const got = await sendShared(client, fileShare(), reader(utf8('pdf')));
    expect(got).toBe(SENT);
    expect(put).toEqual([{ contentType: 'application/pdf', filename: 'report.pdf', body: utf8('pdf') }]);
  });

  it('reads within the relay’s entry limit and sends nothing over it', async () => {
    const { client, put } = fakeClient({ maxEntryBytes: MB });
    const read = reader(new Uint8Array(2 * MB));
    await expect(sendShared(client, fileShare({ size: -1 }), read)).rejects.toThrow('the relay takes at most 1.0 MB');
    expect(read.calls).toEqual([{ id: 7, maxBytes: MB }]);
    expect(put).toHaveLength(0);
  });
});

describe('storeItemFor', () => {
  it('reads within the room the store has left, and saves nothing itself', async () => {
    const { client, put } = fakeClient({ store: { availableBytes: 4 } });
    const read = reader(utf8('pdf'));
    const got = await storeItemFor(client, fileShare(), read);
    expect(got.body).toEqual(utf8('pdf'));
    expect(read.calls).toEqual([{ id: 7, maxBytes: 4 }]);
    expect(put).toHaveLength(0);
  });

  it('says how much room there is when the file does not fit', async () => {
    const { client } = fakeClient({ store: { availableBytes: 2 } });
    await expect(storeItemFor(client, fileShare({ size: 3 }), reader(utf8('pdf')))).rejects.toThrow(
      'This file is 3 B; the store has room for 2 B.',
    );
  });

  const off: { name: string; store: Partial<StoreLimits> | Error }[] = [
    { name: 'reports it disabled', store: { enabled: false } },
    {
      name: 'refuses with STORE_DISABLED',
      store: new ProtocolError(ErrorCode.ERROR_CODE_STORE_DISABLED, 'store disabled'),
    },
  ];
  it.each(off)('says the store is off when the relay $name', async ({ store }) => {
    const { client } = fakeClient({ store });
    await expect(storeItemFor(client, fileShare(), reader(utf8('pdf')))).rejects.toThrow(STORE_OFF);
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
