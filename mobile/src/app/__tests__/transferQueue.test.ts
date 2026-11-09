import { KEEP_FINISHED, TransferQueue, type TransferInfo } from '../transferQueue';

const up = (name: string): TransferInfo => ({ direction: 'upload', target: 'group', name, size: 10 });
const down = (name: string): TransferInfo => ({ direction: 'download', target: 'store', name, size: 20 });

/** deferred is a promise resolved from outside: a transfer the test finishes when it says. */
function deferred<T>() {
  let resolve: (v: T) => void = () => {};
  let reject: (e: unknown) => void = () => {};
  const promise = new Promise<T>((res, rej) => {
    resolve = res;
    reject = rej;
  });
  return { promise, resolve, reject };
}

const settle = () => new Promise((resolve) => setImmediate(resolve));

function queue() {
  let changes = 0;
  const q = new TransferQueue(
    () => {
      changes++;
    },
    (err) => (err as Error).message,
  );
  return { q, changes: () => changes };
}

describe('TransferQueue', () => {
  it('runs one transfer at a time, uploads and downloads alike, in the order asked', async () => {
    const { q } = queue();
    const log: string[] = [];
    const a = deferred<string>();
    const b = deferred<string>();
    const first = q.run(up('a.pdf'), async () => {
      log.push('start a');
      return a.promise;
    }, (r) => r);
    const second = q.run(down('b.bin'), async () => {
      log.push('start b');
      return b.promise;
    }, (r) => r);
    await settle();
    expect(log).toEqual(['start a']);
    expect(q.transfers.map((t) => [t.name, t.state])).toEqual([
      ['a.pdf', 'running'],
      ['b.bin', 'waiting'],
    ]);
    expect(q.position).toEqual({ index: 1, total: 2 });

    a.resolve('Sent.');
    expect(await first).toBe('Sent.');
    await settle();
    expect(log).toEqual(['start a', 'start b']);
    expect(q.position).toEqual({ index: 2, total: 2 });

    b.resolve('Got it.');
    await second;
    await settle();
    expect(q.busy).toBe(false);
    // Finished, newest first, with how each ended.
    expect(q.transfers.map((t) => [t.name, t.state, t.message])).toEqual([
      ['b.bin', 'done', 'Got it.'],
      ['a.pdf', 'done', 'Sent.'],
    ]);
  });

  it('goes on to the next when one fails, and says why it failed', async () => {
    const { q } = queue();
    const failed = q.run(up('x'), async () => {
      throw new Error('the relay went away');
    }, () => '');
    const next = q.run(up('y'), async () => 'ok', (r) => r);
    await expect(failed).rejects.toThrow('the relay went away');
    expect(await next).toBe('ok');
    expect(q.transfers.find((t) => t.name === 'x')).toMatchObject({ state: 'failed', message: 'the relay went away' });
  });

  it('shows the running transfer’s step and progress, and only the running one’s', async () => {
    const { q } = queue();
    const a = deferred<string>();
    let late: ((phase: string, done: number, total: number) => void) | null = null;
    void q.run(up('a'), async (control) => {
      control.progress('Encrypting…', 0, 0);
      control.progress('Uploading…', 3, 10);
      late = control.progress;
      return a.promise;
    }, (r) => r);
    await settle();
    expect(q.current).toMatchObject({ phase: 'Uploading…', done: 3, total: 10 });
    a.resolve('done');
    await settle();
    late!('Uploading…', 9, 10);
    expect(q.transfers[0]).toMatchObject({ state: 'done', phase: '' });
  });

  it('counts a transfer added while others run into the same run', async () => {
    const { q } = queue();
    const a = deferred<string>();
    void q.run(up('a'), () => a.promise, (r) => r);
    await settle();
    expect(q.position).toEqual({ index: 1, total: 1 });
    void q.run(down('b'), async () => 'b', (r) => r);
    expect(q.position).toEqual({ index: 1, total: 2 });
    a.resolve('a');
    await q.whenIdle();
    // A new run counts from one again.
    const c = deferred<string>();
    void q.run(up('c'), () => c.promise, (r) => r);
    await settle();
    expect(q.position).toEqual({ index: 1, total: 1 });
    c.resolve('c');
    await q.whenIdle();
  });

  it('keeps the last few finished until cleared', async () => {
    const { q } = queue();
    for (let i = 0; i < KEEP_FINISHED + 2; i++) {
      await q.run(up(`f${i}`), async () => 'ok', (r) => r);
    }
    expect(q.transfers).toHaveLength(KEEP_FINISHED);
    expect(q.transfers[0].name).toBe(`f${KEEP_FINISHED + 1}`);
    q.clearFinished();
    expect(q.transfers).toHaveLength(0);
  });

  it('gives a fresh list on every change, so a view sees it', async () => {
    const { q, changes } = queue();
    const before = q.transfers;
    const a = deferred<string>();
    void q.run(up('a'), () => a.promise, (r) => r);
    await settle();
    expect(q.transfers).not.toBe(before);
    expect(changes()).toBeGreaterThan(0);
    a.resolve('a');
    await q.whenIdle();
  });

  it('is idle at once when empty, and once the last transfer ends otherwise', async () => {
    const { q } = queue();
    await q.whenIdle();
    const a = deferred<string>();
    void q.run(up('a'), () => a.promise, (r) => r);
    let idle = false;
    void q.whenIdle().then(() => {
      idle = true;
    });
    await settle();
    expect(idle).toBe(false);
    a.resolve('a');
    await settle();
    expect(idle).toBe(true);
  });
});
