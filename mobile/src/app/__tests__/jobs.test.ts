import { utf8, type ExternalBody, type StoreDetails } from '../../core';
import type { StagedFile } from '../../platform/types';
import { knownHash, runJob, type JobDeps, type UploadItem } from '../jobs';
import { SENT, STORED, TEXT_CONTENT_TYPE, type ShareClient } from '../share';

const file: StagedFile = { token: 'tok', name: 'x', contentType: 'application/pdf', size: 3, sha256: '' };
const details: StoreDetails = { name: 'Report', description: '', tags: ['work'] };

/** deps are fake job dependencies that record what the job did, in order. */
function deps(
  opts: { client?: boolean; readFails?: boolean; putFails?: boolean; external?: ExternalBody | null } = {},
) {
  const log: string[] = [];
  const put: UploadItem[] = [];
  const stored: { item: UploadItem; details: StoreDetails }[] = [];
  const client = {
    hold: async <T>(run: () => Promise<T>) => {
      log.push('hold');
      try {
        return await run();
      } finally {
        log.push('release');
      }
    },
    putEntry: async (item: UploadItem) => {
      log.push('put');
      if (opts.putFails === true) {
        throw new Error('relay gone');
      }
      put.push(item);
      return {};
    },
  } as unknown as ShareClient;
  const d: JobDeps = {
    client: () => (opts.client === false ? null : client),
    readStaged: async (f, onProgress) => {
      log.push(`read ${f.token}`);
      if (opts.readFails === true) {
        throw new Error('gone');
      }
      onProgress(f.size, f.size);
      return utf8('pdf');
    },
    discard: (token) => log.push(`discard ${token}`),
    external:
      opts.external === undefined
        ? undefined
        : async (f) => {
            log.push(`external ${f.token}`);
            return opts.external ?? null;
          },
    saveToStore: async (item, det) => {
      log.push('store');
      stored.push({ item, details: det });
    },
    progress: (title, done, total) => log.push(`${title} ${done}/${total}`),
  };
  return { d, log, put, stored };
}

describe('runJob', () => {
  it('reads a staged file, sends it, then deletes the copy', async () => {
    const { d, log, put } = deps();
    const got = await runJob({ id: 1, target: 'send', payload: { kind: 'file', file, contentType: 'application/pdf', filename: 'r.pdf' } }, d);
    expect(got).toBe(SENT);
    expect(put).toEqual([{ contentType: 'application/pdf', filename: 'r.pdf', body: utf8('pdf') }]);
    // The whole job holds the socket, so leaving the app cannot cut it off.
    expect(log).toEqual([
      'hold',
      'Preparing… 0/3',
      'read tok',
      'Preparing… 3/3',
      'Encrypting… 0/0',
      'put',
      'discard tok',
      'release',
    ]);
  });

  it('hands a staged file over sealed natively when it can, without reading it', async () => {
    const ext: ExternalBody = { size: 3, sha256: new Uint8Array(32), seal: async () => new Uint8Array(0) };
    const { d, log, put } = deps({ external: ext });
    await runJob({ id: 1, target: 'send', payload: { kind: 'file', file, contentType: 'application/pdf', filename: 'r.pdf' } }, d);
    expect(put).toEqual([{ contentType: 'application/pdf', filename: 'r.pdf', external: ext }]);
    // The copy is read by the native seal during the upload, so it goes after.
    expect(log).toEqual(['hold', 'external tok', 'Encrypting… 0/0', 'put', 'discard tok', 'release']);
  });

  it('reads and seals in JavaScript when the native seal cannot be used', async () => {
    const { d, log, put } = deps({ external: null });
    await runJob({ id: 1, target: 'send', payload: { kind: 'file', file, contentType: 'application/pdf', filename: 'r.pdf' } }, d);
    expect(log).toContain('read tok');
    expect(put).toEqual([{ contentType: 'application/pdf', filename: 'r.pdf', body: utf8('pdf'), sha256: undefined }]);
  });

  it('passes the hash the copy was made with, so it is not worked out again', async () => {
    const { d, put } = deps();
    const hashed = { ...file, sha256: 'ab'.repeat(32) };
    await runJob({ id: 1, target: 'send', payload: { kind: 'file', file: hashed, contentType: 'application/pdf', filename: 'r.pdf' } }, d);
    expect((put[0] as { sha256?: Uint8Array }).sha256).toEqual(new Uint8Array(32).fill(0xab));
  });

  it('saves to the store with the details the user gave', async () => {
    const { d, stored } = deps();
    const item = { contentType: TEXT_CONTENT_TYPE, filename: '', body: utf8('hi') };
    const got = await runJob({ id: 2, target: 'store', details, payload: { kind: 'text', item } }, d);
    expect(got).toBe(STORED);
    expect(stored).toEqual([{ item, details }]);
  });

  const failures: { name: string; opts: Parameters<typeof deps>[0]; want: string }[] = [
    { name: 'the copy cannot be read', opts: { readFails: true }, want: 'gone' },
    { name: 'there is no client', opts: { client: false }, want: 'not paired' },
    { name: 'the upload fails', opts: { putFails: true }, want: 'relay gone' },
  ];
  it.each(failures)('fails, and still deletes the copy, when $name', async ({ opts, want }) => {
    const { d, log } = deps(opts);
    await expect(
      runJob({ id: 3, target: 'send', payload: { kind: 'file', file, contentType: 'application/pdf', filename: 'r.pdf' } }, d),
    ).rejects.toThrow(want);
    expect(log).toContain('discard tok');
  });
});

describe('knownHash', () => {
  const cases: { name: string; hex: string; want: Uint8Array | undefined }[] = [
    { name: 'a SHA-256 in lowercase hex', hex: '01'.repeat(32), want: new Uint8Array(32).fill(1) },
    { name: 'nothing for an empty one', hex: '', want: undefined },
    { name: 'nothing for one of the wrong length', hex: '01'.repeat(16), want: undefined },
    { name: 'nothing for one that is not hex', hex: 'zz'.repeat(32), want: undefined },
  ];
  it.each(cases)('gives $name', ({ hex, want }) => {
    expect(knownHash(hex)).toEqual(want);
  });
});
