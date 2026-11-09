/**
 * The one queue every large transfer goes through, upload or download.
 *
 * A large upload (a shared file, a file added to the store) and a large
 * download (a received file saved, opened or shared, a store item, the latest
 * entry) each take a whole file through memory, the crypto and the relay's
 * bandwidth. Two at once only made both slower and the app heavier, and the
 * user had no way to see what was moving. So they run here, one at a time, in
 * the order they were asked for; the next starts when the last has finished,
 * whichever way either goes.
 *
 * Each transfer says what it is — which way, to or from where (the group's
 * history or its store), its name and size — and where it is: waiting, its
 * phase and progress while it runs, and how it ended. That is what the app's
 * Transfers panel and the notification show.
 *
 * This module imports nothing from React Native, so it is tested directly.
 */

/** Direction is which way a transfer goes. */
export type Direction = 'upload' | 'download';

/** Target is where it goes to or comes from: the group's latest entries, or its store. */
export type Target = 'group' | 'store';

/** TransferInfo is what a transfer is, as the user would name it. */
export interface TransferInfo {
  direction: Direction;
  target: Target;
  /** name is a filename, an item's name, or what the content is ("Text"). */
  name: string;
  /** size is in bytes, or 0 when not known yet. */
  size: number;
}

/** TransferState is where a transfer is. */
export type TransferState = 'waiting' | 'running' | 'done' | 'failed';

/** Transfer is one transfer as the panel shows it. */
export interface Transfer extends TransferInfo {
  id: number;
  state: TransferState;
  /** phase is what it is doing while it runs ("Encrypting…"), "" otherwise. */
  phase: string;
  /** done and total are its progress in the current phase; total 0 has nothing to count. */
  done: number;
  total: number;
  /** message is how it ended, once it has. */
  message: string;
}

/** TransferControl is what a running transfer reports through. */
export interface TransferControl {
  /** progress says what the transfer is doing and how far along; total 0 has nothing to count. */
  progress: (phase: string, done: number, total: number) => void;
}

/** KEEP_FINISHED is how many finished transfers stay in the panel until cleared. */
export const KEEP_FINISHED = 5;

interface Task {
  transfer: Transfer;
  start: () => void;
}

export class TransferQueue {
  private readonly tasks: Task[] = [];
  private finished: Transfer[] = [];
  private running: Task | null = null;
  private nextId = 1;
  /** done counts the transfers finished since the queue was last empty, for "2 of 3". */
  private doneInRun = 0;
  private idleWaiters: (() => void)[] = [];
  private view: readonly Transfer[] = [];

  constructor(
    private readonly onChange: () => void,
    private readonly describeError: (err: unknown) => string,
  ) {}

  /**
   * run queues `work` as a transfer and resolves with what it resolves with,
   * once it has had its turn and finished. `outcome` names a success for the
   * panel; a failure is named from its error.
   */
  run<T>(info: TransferInfo, work: (control: TransferControl) => Promise<T>, outcome: (result: T) => string): Promise<T> {
    return new Promise<T>((resolve, reject) => {
      const transfer: Transfer = { ...info, id: this.nextId++, state: 'waiting', phase: '', done: 0, total: 0, message: '' };
      const task: Task = {
        transfer,
        start: () => {
          const control: TransferControl = {
            progress: (phase, done, total) => {
              if (this.running === task) {
                this.update(transfer, { phase, done, total });
              }
            },
          };
          let settled: Promise<T>;
          try {
            settled = work(control);
          } catch (err) {
            settled = Promise.reject(asError(err));
          }
          settled.then(
            (result) => {
              this.finish(task, 'done', outcome(result));
              resolve(result);
            },
            (err: unknown) => {
              this.finish(task, 'failed', this.describeError(err));
              reject(asError(err));
            },
          );
        },
      };
      this.tasks.push(task);
      this.changed();
      this.next();
    });
  }

  /** transfers is every transfer to show: running, then waiting, then the last few finished, newest first. */
  get transfers(): readonly Transfer[] {
    return this.view;
  }

  /** current is the running transfer, or null. */
  get current(): Transfer | null {
    return this.running?.transfer ?? null;
  }

  /** busy reports whether a transfer is running or waiting. */
  get busy(): boolean {
    return this.running !== null || this.tasks.length > 0;
  }

  /**
   * position is where the running transfer stands in this run of the queue —
   * the second of three — counting those finished since it was last empty.
   */
  get position(): { index: number; total: number } {
    const running = this.running === null ? 0 : 1;
    return { index: this.doneInRun + running, total: this.doneInRun + running + this.tasks.length };
  }

  /** whenIdle resolves once nothing is running or waiting. */
  whenIdle(): Promise<void> {
    if (!this.busy) {
      return Promise.resolve();
    }
    return new Promise((resolve) => this.idleWaiters.push(resolve));
  }

  /** clearFinished drops the finished transfers from the panel. */
  clearFinished(): void {
    if (this.finished.length > 0) {
      this.finished = [];
      this.changed();
    }
  }

  private next(): void {
    if (this.running !== null) {
      return;
    }
    const task = this.tasks.shift();
    if (task === undefined) {
      this.doneInRun = 0;
      const waiters = this.idleWaiters;
      this.idleWaiters = [];
      for (const wake of waiters) {
        wake();
      }
      return;
    }
    this.running = task;
    this.update(task.transfer, { state: 'running' });
    task.start();
  }

  private finish(task: Task, state: 'done' | 'failed', message: string): void {
    if (this.running !== task) {
      return;
    }
    this.running = null;
    this.doneInRun++;
    const ended = { ...task.transfer, state, message, phase: '' };
    task.transfer = ended;
    this.finished = [ended, ...this.finished].slice(0, KEEP_FINISHED);
    this.changed();
    this.next();
  }

  private update(transfer: Transfer, patch: Partial<Transfer>): void {
    Object.assign(transfer, patch);
    this.changed();
  }

  private changed(): void {
    // A fresh array and fresh objects, so a view that compares by identity
    // sees every change.
    const active = [...(this.running === null ? [] : [this.running]), ...this.tasks].map((t) => ({ ...t.transfer }));
    this.view = [...active, ...this.finished];
    this.onChange();
  }
}

/** asError keeps an Error as it is — a TppError keeps its kind — and wraps anything else. */
function asError(err: unknown): Error {
  return err instanceof Error ? err : new Error(String(err));
}
