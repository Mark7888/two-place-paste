import { utf8, type StoreDetails } from '../../core';
import type { ClipboardItem, StagedFile } from '../../platform/types';
import { JobQueue, runJob, type Job, type JobDeps } from '../jobs';
import { SENT, STORED, TEXT_CONTENT_TYPE, type ShareClient } from '../share';

const file: StagedFile = { token: 'tok', name: 'x', contentType: 'application/pdf', size: 3, sha256: '' };
const details: StoreDetails = { name: 'Report', description: '', tags: ['work'] };

/** deps are fake job dependencies that record what the job did, in order. */
function deps(opts: { client?: boolean; readFails?: boolean; putFails?: boolean } = {}) {
  const log: string[] = [];
  const put: ClipboardItem[] = [];
  const stored: { item: ClipboardItem; details: StoreDetails }[] = [];
  const client = {
    hold: async <T>(run: () => Promise<T>) => {
      log.push('hold');
      try {
        return await run();
      } finally {
        log.push('release');
      }
    },
    putEntry: async (item: ClipboardItem) => {
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
    saveToStore: async (item, det) => {
      log.push('store');
      stored.push({ item, details: det });
    },
    progress: (title, done, total) => log.push(`${title} ${done}/${total}`),
  };
  return { d, log, put, stored };
}

describe('runJob', () => {
  it('reads a staged file, deletes the copy, then sends it', async () => {
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
      'discard tok',
      'Encrypting… 0/0',
      'put',
      'release',
    ]);
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

describe('JobQueue', () => {
  const job = (id: number): Job => ({
    id,
    target: 'send',
    payload: { kind: 'text', item: { contentType: TEXT_CONTENT_TYPE, filename: '', body: utf8(String(id)) } },
  });

  it('runs jobs one at a time, in order, and reports each outcome', async () => {
    const order: string[] = [];
    let active = 0;
    const reports: [boolean, string][] = [];
    const q = new JobQueue(
      async (j) => {
        active++;
        expect(active).toBe(1);
        order.push(`start ${j.id}`);
        await Promise.resolve();
        order.push(`end ${j.id}`);
        active--;
        if (j.id === 2) {
          throw new Error('two failed');
        }
        return `done ${j.id}`;
      },
      (_job, ok, message) => reports.push([ok, message]),
      (err) => (err as Error).message,
    );
    q.add(job(1));
    q.add(job(2));
    q.add(job(3));
    await q.drain();
    expect(order).toEqual(['start 1', 'end 1', 'start 2', 'end 2', 'start 3', 'end 3']);
    expect(reports).toEqual([
      [true, 'done 1'],
      [false, 'two failed'],
      [true, 'done 3'],
    ]);
    expect(q.running).toBe(false);
  });

  it('picks up a job added while it runs, and joins a drain in progress', async () => {
    const ran: number[] = [];
    const q: JobQueue = new JobQueue(
      async (j) => {
        ran.push(j.id);
        if (j.id === 1) {
          q.add(job(2));
        }
        return '';
      },
      () => {},
      String,
    );
    q.add(job(1));
    const first = q.drain();
    const second = q.drain();
    expect(second).toBe(first);
    await first;
    expect(ran).toEqual([1, 2]);
  });

  it('starts a new drain for a job added after the last one finished', async () => {
    const ran: number[] = [];
    const q = new JobQueue(
      async (j) => {
        ran.push(j.id);
        return '';
      },
      () => {},
      String,
    );
    await q.drain();
    expect(q.running).toBe(false);
    q.add(job(4));
    await q.drain();
    expect(ran).toEqual([4]);
  });
});

describe('JobQueue.position', () => {
  const job = (id: number): Job => ({
    id,
    target: 'send',
    payload: { kind: 'text', item: { contentType: TEXT_CONTENT_TYPE, filename: '', body: utf8(String(id)) } },
  });

  it('counts which upload of the run is going, including ones added meanwhile', async () => {
    const seen: string[] = [];
    const q: JobQueue = new JobQueue(
      async (j) => {
        if (j.id === 1) {
          q.add(job(3));
        }
        const { index, total } = q.position;
        seen.push(`${index} of ${total}`);
        return '';
      },
      () => {},
      String,
    );
    q.add(job(1));
    q.add(job(2));
    await q.drain();
    expect(seen).toEqual(['1 of 3', '2 of 3', '3 of 3']);
    // A new run counts from one again.
    q.add(job(4));
    await q.drain();
    expect(seen[3]).toBe('1 of 1');
  });
});
