/**
 * The scaffolding the interop test runs on: a real Redis, the real relay
 * binary, and the Go client core as a subprocess.
 *
 * Nothing here is a stand-in. The point of this test is that two independent
 * implementations of spec/crypto.md and the wire contract agree, and a fake on
 * either side would only prove that this client agrees with itself. The unit
 * tests under `src/core` use an in-process relay for the client's own rules;
 * this is where the actual server runs.
 */

import { type ChildProcess, type ChildProcessWithoutNullStreams, spawn, spawnSync } from 'node:child_process';
import { createInterface, type Interface } from 'node:readline';
import { createServer } from 'node:net';
import { mkdtempSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';

/** REPO is the repository root, from `mobile/src/interop`. */
export const REPO = join(__dirname, '..', '..', '..');

/** missingTool names the first tool this test needs and cannot find, or null. */
export function missingTool(): string | null {
  for (const tool of ['go', 'redis-server']) {
    if (spawnSync(tool, ['--version'], { stdio: 'ignore' }).error !== undefined) {
      return tool;
    }
  }
  return null;
}

/** freePort reserves a loopback port and releases it. */
export async function freePort(): Promise<number> {
  return new Promise((resolve, reject) => {
    const server = createServer();
    server.on('error', reject);
    server.listen(0, '127.0.0.1', () => {
      const address = server.address();
      const port = typeof address === 'object' && address !== null ? address.port : 0;
      server.close(() => resolve(port));
    });
  });
}

/** waitFor polls until a condition holds, and fails loudly if it never does. */
export async function waitFor(what: string, condition: () => Promise<boolean>): Promise<void> {
  const deadline = Date.now() + 60_000;
  while (Date.now() < deadline) {
    if (await condition().catch(() => false)) {
      return;
    }
    await new Promise((resolve) => setTimeout(resolve, 100));
  }
  throw new Error(`timed out waiting for ${what}`);
}

/** Relay is a running server, with the Redis behind it and the admin flow that mints a creation token. */
export class Relay {
  private constructor(
    readonly baseUrl: string,
    private readonly adminPassword: string,
    private readonly processes: ChildProcess[],
    private readonly directory: string,
  ) {}

  /** start brings up Redis and the relay, built from this repository. */
  static async start(): Promise<Relay> {
    const directory = mkdtempSync(join(tmpdir(), 'tpp-interop-'));
    const redisPort = await freePort();
    const redis = spawn(
      'redis-server',
      [
        '--bind', '127.0.0.1',
        '--port', String(redisPort),
        '--save', '',
        '--appendonly', 'no',
        // The deployment's own policy: volatile-lru, because allkeys-lru would
        // evict group records (SPEC §4.2).
        '--maxmemory-policy', 'volatile-lru',
        '--dir', directory,
      ],
      { stdio: ['ignore', 'pipe', 'pipe'] },
    );

    const binary = join(directory, 'tpp');
    const build = spawnSync('go', ['build', '-o', binary, './cmd/tpp'], {
      cwd: join(REPO, 'server'),
      encoding: 'utf8',
    });
    if (build.status !== 0) {
      throw new Error(`building the relay failed: ${build.stderr}`);
    }

    const port = await freePort();
    const baseUrl = `http://127.0.0.1:${port}`;
    const adminPassword = 'interop-test-password';
    const server = spawn(binary, [], {
      cwd: directory,
      stdio: ['ignore', 'pipe', 'pipe'],
      env: {
        ...process.env,
        TPP_HTTP_ADDR: `127.0.0.1:${port}`,
        TPP_PUBLIC_BASE_URL: baseUrl,
        ADMIN_PASSWORD: adminPassword,
        TPP_REDIS_ADDR: `127.0.0.1:${redisPort}`,
        TPP_BLOB_ROOT: join(directory, 'blobs'),
        TPP_BLOB_SWEEP_INTERVAL: '10m',
        TPP_LOG_LEVEL: 'warn',
      },
    });

    const relay = new Relay(baseUrl, adminPassword, [server, redis], directory);
    await waitFor('the relay to answer /healthz', async () => {
      const response = await fetch(`${baseUrl}/healthz`);
      return response.ok;
    });
    return relay;
  }

  /**
   * creationURL signs in to the admin UI and mints a creation token, which is
   * the string an operator hands a user (SPEC §3.1, §4.4).
   */
  async creationURL(name: string): Promise<string> {
    // Node's fetch keeps no cookie jar, so the session cookie is carried by
    // hand. It is one cookie and one flow.
    const login = await fetch(`${this.baseUrl}/admin/login`, {
      method: 'POST',
      headers: { 'content-type': 'application/x-www-form-urlencoded' },
      body: new URLSearchParams({ password: this.adminPassword }),
      redirect: 'manual',
    });
    const cookie = (login.headers.getSetCookie()[0] ?? '').split(';')[0];
    if (cookie === '') {
      throw new Error(`admin login did not set a session cookie (status ${login.status})`);
    }

    const page = await (await fetch(`${this.baseUrl}/admin/`, { headers: { cookie } })).text();
    const csrf = between(page, 'name="csrf" value="', '"');
    await fetch(`${this.baseUrl}/admin/tokens`, {
      method: 'POST',
      headers: { cookie, 'content-type': 'application/x-www-form-urlencoded' },
      body: new URLSearchParams({ name, csrf }),
      redirect: 'manual',
    });

    // The token screen renders each unused token's QR image at a URL carrying
    // the token value, which is the only place it appears in the markup.
    const withToken = await (await fetch(`${this.baseUrl}/admin/`, { headers: { cookie } })).text();
    return `${this.baseUrl}/${between(withToken, '/admin/tokens/', '/qr.png')}`;
  }

  /** stop tears everything down. */
  stop(): void {
    for (const child of this.processes) {
      child.kill('SIGKILL');
    }
    rmSync(this.directory, { recursive: true, force: true });
  }
}

/** GoPeer is the Go client core, driven over the line protocol `scripts/gopeer` speaks. */
export class GoPeer {
  private readonly lines: Interface;
  private readonly queue: string[] = [];
  private waiting: ((line: string) => void) | null = null;

  private constructor(
    private readonly child: ChildProcessWithoutNullStreams,
    name: string,
  ) {
    this.name = name;
    this.lines = createInterface({ input: child.stdout });
    this.lines.on('line', (line) => {
      if (this.waiting !== null) {
        const resolve = this.waiting;
        this.waiting = null;
        resolve(line);
        return;
      }
      this.queue.push(line);
    });
  }

  readonly name: string;

  /** start builds the peer and launches it. */
  static start(name: string): GoPeer {
    const directory = join(REPO, 'mobile', 'scripts', 'gopeer');
    const build = spawnSync('go', ['build', '-o', join(directory, 'gopeer'), '.'], {
      cwd: directory,
      // The peer is deliberately outside /go.work (see its package comment).
      env: { ...process.env, GOWORK: 'off' },
      encoding: 'utf8',
    });
    if (build.status !== 0) {
      throw new Error(`building the Go peer failed: ${build.stderr}`);
    }
    const child = spawn(join(directory, 'gopeer'), [], {
      stdio: ['pipe', 'pipe', 'pipe'],
      env: { ...process.env, TPP_DEVICE_NAME: name },
    });
    return new GoPeer(child, name);
  }

  /** send runs one command and returns its reply, failing the test if the peer reported an error. */
  async send(command: string): Promise<Record<string, unknown>> {
    this.child.stdin.write(`${command}\n`);
    const line = await this.nextLine();
    const reply = JSON.parse(line) as Record<string, unknown>;
    if (reply.ok !== true) {
      throw new Error(`the Go peer refused "${command}": ${String(reply.error)}`);
    }
    return reply;
  }

  /** stop closes the peer down. */
  stop(): void {
    this.child.stdin.end();
    this.child.kill('SIGKILL');
    this.lines.close();
  }

  private nextLine(): Promise<string> {
    const queued = this.queue.shift();
    if (queued !== undefined) {
      return Promise.resolve(queued);
    }
    return new Promise<string>((resolve, reject) => {
      const timer = setTimeout(() => reject(new Error('the Go peer did not answer')), 60_000);
      this.waiting = (line) => {
        clearTimeout(timer);
        resolve(line);
      };
    });
  }
}

function between(haystack: string, prefix: string, suffix: string): string {
  const start = haystack.indexOf(prefix);
  if (start < 0) {
    throw new Error(`no ${prefix} in the admin page`);
  }
  const rest = haystack.slice(start + prefix.length);
  const end = rest.indexOf(suffix);
  if (end < 0) {
    throw new Error(`${prefix} is not terminated by ${suffix}`);
  }
  return rest.slice(0, end);
}
