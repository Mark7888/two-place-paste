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

describe('TransferQueue, asked twice for the same file', () => {
  const file = (key: string, name: string): TransferInfo => ({ direction: 'download', target: 'group', name, size: 30, key });

  it('joins the second ask to the first, even with another transfer queued between them', async () => {
    const { q } = queue();
    const log: string[] = [];
    const first = deferred<string>();
    const save1 = q.run(file('entry:1', 'one.pdf'), async () => {
      log.push('save 1');
      return first.promise;
    }, (r) => r);
    const open2 = q.run(file('entry:2', 'two.jpg'), async () => {
      log.push('open 2');
      return 'opened 2';
    }, (r) => r);
    const open1 = q.run(file('entry:1', 'one.pdf'), async () => {
      log.push('open 1');
      return 'opened 1';
    }, (r) => r);
    await settle();
    // Two rows, not three: the second ask for one.pdf is part of the first.
    expect(q.transfers.map((t) => [t.name, t.state])).toEqual([
      ['one.pdf', 'running'],
      ['two.jpg', 'waiting'],
    ]);
    expect(q.position).toEqual({ index: 1, total: 2 });

    first.resolve('saved 1');
    expect(await save1).toBe('saved 1');
    expect(await open1).toBe('opened 1');
    expect(await open2).toBe('opened 2');
    // One.pdf is opened once it is saved, before two.jpg's turn.
    expect(log).toEqual(['save 1', 'open 1', 'open 2']);
    expect(q.transfers.map((t) => [t.name, t.state, t.message])).toEqual([
      ['two.jpg', 'done', 'opened 2'],
      ['one.pdf', 'done', 'opened 1'],
    ]);
  });

  it('joins a transfer that is still waiting, too', async () => {
    const { q } = queue();
    const log: string[] = [];
    const block = deferred<string>();
    void q.run(up('upload'), () => block.promise, (r) => r);
    const a = q.run(file('store:9', 'kept'), async () => {
      log.push('a');
      return 'a';
    }, (r) => r);
    const b = q.run(file('store:9', 'kept'), async () => {
      log.push('b');
      return 'b';
    }, (r) => r);
    await settle();
    expect(q.transfers).toHaveLength(2);
    block.resolve('done');
    expect([await a, await b]).toEqual(['a', 'b']);
    expect(log).toEqual(['a', 'b']);
  });

  it('runs the joined work even when the first fails, and ends as the last ended', async () => {
    const { q } = queue();
    const failing = q.run(file('entry:5', 'f'), async () => {
      throw new Error('lost the relay');
    }, () => '');
    const retry = q.run(file('entry:5', 'f'), async () => 'got it', (r) => r);
    await expect(failing).rejects.toThrow('lost the relay');
    expect(await retry).toBe('got it');
    expect(q.transfers[0]).toMatchObject({ state: 'done', message: 'got it' });
  });

  it('does not join a file once its transfer has finished: it is asked for afresh', async () => {
    const { q } = queue();
    await q.run(file('entry:7', 'f'), async () => 'first', (r) => r);
    await q.run(file('entry:7', 'f'), async () => 'second', (r) => r);
    expect(q.transfers.map((t) => t.message)).toEqual(['second', 'first']);
  });

  it('never joins transfers without a key', async () => {
    const { q } = queue();
    const block = deferred<string>();
    void q.run(up('same'), () => block.promise, (r) => r);
    void q.run(up('same'), async () => 'x', (r) => r);
    await settle();
    expect(q.transfers).toHaveLength(2);
    block.resolve('y');
    await q.whenIdle();
  });
});
