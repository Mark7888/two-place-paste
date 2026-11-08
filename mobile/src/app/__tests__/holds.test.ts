import type { Progress } from '../../core';
import { DOWNLOAD_DONE, KeepAlive, type KeepAliveDeps } from '../holds';

const download = (done: number, total: number): Progress => ({
  entryId: 'e',
  direction: 'download',
  done,
  total,
  filename: '',
  contentType: '',
  waiting: false,
});

/** setup is a KeepAlive over fakes that record what it did. */
function setup(opts: { foreground?: boolean } = {}) {
  const log: string[] = [];
  let listener: ((p: Progress) => void) | null = null;
  let clock = 0;
  const state = { foreground: opts.foreground ?? true };
  const deps: KeepAliveDeps = {
    start: async () => {
      log.push('start');
    },
    progress: (title, done, total) => log.push(`${title} ${done}/${total}`),
    result: (_id, ok, message) => log.push(`result ${ok} ${message}`),
    foreground: () => state.foreground,
    onProgress: (l) => {
      listener = l;
      return () => {
        listener = null;
      };
    },
    describeError: (err) => (err as Error).message,
    now: () => clock,
  };
  const emit = (p: Progress) => listener?.(p);
  const tick = (ms: number) => {
    clock += ms;
  };
  return { keep: new KeepAlive(deps), log, emit, tick, state };
}

describe('KeepAlive', () => {
  it('never starts the runner for work with no large download in it', async () => {
    const { keep, log } = setup();
    expect(await keep.during(async () => 'small')).toBe('small');
    expect(log).toEqual([]);
    expect(keep.held).toBe(false);
  });

  it('holds the runner from a download’s first progress until the work is done', async () => {
    const { keep, log, emit, tick } = setup();
    let finish: () => void = () => {};
    const work = keep.during(
      () =>
        new Promise<void>((resolve) => {
          finish = resolve;
        }),
    );
    emit(download(0, 100));
    tick(100);
    emit(download(10, 100)); // too soon after the last: not shown
    tick(500);
    emit(download(60, 100));
    emit(download(100, 100));
    expect(keep.held).toBe(true);
    let released = false;
    void keep.whenReleased().then(() => {
      released = true;
    });
    finish();
    await work;
    await Promise.resolve();
    expect(released).toBe(true);
    expect(keep.held).toBe(false);
    expect(log).toEqual(['start', 'Downloading… 0/100', 'Downloading… 60/100', 'Decrypting… 0/0']);
  });

  it('says how it ended when the user is elsewhere, without the file’s name', async () => {
    const { keep, log, emit, state } = setup();
    await keep.during(async () => {
      emit(download(0, 10));
      state.foreground = false;
    });
    expect(log).toContain(`result true ${DOWNLOAD_DONE}`);

    log.length = 0;
    state.foreground = true;
    await expect(
      keep.during(async () => {
        emit(download(0, 10));
        state.foreground = false;
        throw new Error('the relay went away');
      }),
    ).rejects.toThrow('the relay went away');
    expect(log).toContain('result false the relay went away');
  });

  it('does not start the runner from the background, which Android refuses', async () => {
    const { keep, log, emit } = setup({ foreground: false });
    await keep.during(async () => {
      emit(download(0, 10));
    });
    expect(log).toEqual([]);
  });

  it('carries on without the runner when it cannot be started', async () => {
    const { keep, emit } = setup();
    const failing = new KeepAlive({
      ...(keep as unknown as { deps: KeepAliveDeps }).deps,
      start: () => Promise.reject(new Error('not allowed')),
    });
    await expect(
      failing.during(async () => {
        emit(download(0, 10));
        return 'done';
      }),
    ).resolves.toBe('done');
  });
});
