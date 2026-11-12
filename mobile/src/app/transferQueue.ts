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
 * Asking again for a file already in the queue does not queue it twice. A
 * transfer can carry a key — the entry or store item it reads — and work
 * asked for under a key that is waiting or running joins that transfer, to
 * run right after what it was asked for first, even when other transfers were
 * queued in between: "save report.pdf", "open photo.jpg", "open report.pdf"
 * is two transfers, and the second action on report.pdf, run once it has
 * been downloaded, opens the copy the first one kept.
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
  /**
   * key names what the transfer reads — "entry:<id>", "store:<id>" — so a
   * second ask for it joins this one rather than queueing it again.
   */
  key?: string;
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

/** Job is one piece of work asked for: what it does, how its success is named, and who waits on it. */
interface Job {
  work: (control: TransferControl) => Promise<unknown>;
  outcome: (result: unknown) => string;
  resolve: (result: unknown) => void;
  reject: (err: Error) => void;
}

/** Task is one transfer in the queue: the work asked for under it, run in turn. */
interface Task {
  transfer: Transfer;
  key: string | undefined;
  jobs: Job[];
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
   * panel; a failure is named from its error. Work under the key of a
   * transfer that is waiting or running joins it instead (see the top).
   */
  run<T>(info: TransferInfo, work: (control: TransferControl) => Promise<T>, outcome: (result: T) => string): Promise<T> {
    return new Promise<T>((resolve, reject) => {
      const job: Job = {
        work,
        outcome: (result) => outcome(result as T),
        resolve: (result) => resolve(result as T),
        reject,
      };
      const same =
        info.key === undefined
          ? undefined
          : [...(this.running === null ? [] : [this.running]), ...this.tasks].find((t) => t.key === info.key);
      if (same !== undefined) {
        same.jobs.push(job);
        this.changed();
        return;
      }
      const transfer: Transfer = { ...info, id: this.nextId++, state: 'waiting', phase: '', done: 0, total: 0, message: '' };
      this.tasks.push({ transfer, key: info.key, jobs: [job] });
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
    void this.runJobs(task);
  }

  /**
   * runJobs runs a transfer's work in turn, including work that joins it
   * while it runs, and ends it as its last work ended. Nothing is awaited
   * between finding no more work and ending it, so work that joins can never
   * be left behind.
   */
  private async runJobs(task: Task): Promise<void> {
    let state: 'done' | 'failed' = 'done';
    let message = '';
    for (let job = task.jobs.shift(); job !== undefined; job = task.jobs.shift()) {
      const current = job;
      // Progress counts only while this work runs: a late report from work
      // that has finished does not move the row.
      let live = true;
      const control: TransferControl = {
        progress: (phase, done, total) => {
          if (live && this.running === task) {
            this.update(task.transfer, { phase, done, total });
          }
        },
      };
      try {
        const result = await Promise.resolve().then(() => current.work(control));
        state = 'done';
        message = current.outcome(result);
        current.resolve(result);
      } catch (err) {
        state = 'failed';
        message = this.describeError(err);
        current.reject(asError(err));
      }
      live = false;
      if (task.jobs.length > 0) {
        // Joined work starts afresh on the same row.
        this.update(task.transfer, { phase: '', done: 0, total: 0 });
      }
    }
    this.finish(task, state, message);
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
