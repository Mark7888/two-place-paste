/**
 * One WebSocket connection to the relay, with request correlation on top of it
 * (SPEC §5.1).
 *
 * Every frame is a serialized tpp.v1.Envelope carried as a binary message and
 * nothing else. A response echoes its request's id; a server-pushed event
 * carries an id that correlates with nothing and is routed to the event
 * handler instead.
 */

// Installs the UTF-8 the protobuf codec reaches for. It must be imported
// above the generated messages below: on Hermes they cannot encode or
// decode a single field without it.
import './textEncoding';

import { toBase64URL } from '../bytes';
import { randomBytes } from '../random';
import { Envelope, MessageType } from '../../protocol/gen/tpp/v1/envelope';
import { Error as ErrorFrame } from '../../protocol/gen/tpp/v1/error';
import { ProtocolError, TppError } from './errors';

/**
 * Socket is the port to the platform WebSocket. React Native and Node both
 * expose a global `WebSocket`; this interface exists so a test can drive a
 * relay in process, and so a platform without the global can supply its own.
 */
export interface Socket {
  send(frame: Uint8Array): void;
  close(): void;
  onOpen(handler: () => void): void;
  onMessage(handler: (frame: Uint8Array) => void): void;
  onClose(handler: (reason: string) => void): void;
  onError(handler: (reason: string) => void): void;
}

/** SocketFactory opens a socket to a URL. */
export type SocketFactory = (url: string) => Socket;

/** Codec is the encode/decode pair ts-proto generates for one message. */
export interface Codec<T> {
  encode(message: T): { finish(): Uint8Array };
  decode(input: Uint8Array): T;
}

/** REQUEST_TIMEOUT_MS bounds one request. A phone that has lost its network must not hang a screen forever. */
export const REQUEST_TIMEOUT_MS = 20_000;

interface Pending {
  resolve(env: Envelope): void;
  reject(err: Error): void;
}

/**
 * Connection is one live socket. It is opened by `connect`, which resolves
 * once the socket is open, and is dead for good once it closes: the client
 * dials a new one rather than reviving this.
 */
export class Connection {
  private readonly socket: Socket;
  private readonly pending = new Map<string, Pending>();
  private readonly waiters = new Map<MessageType, Pending[]>();
  private onEvent: (env: Envelope) => void = () => {};
  private onClosed: (reason: string) => void = () => {};
  private closedReason: string | null = null;

  private constructor(socket: Socket) {
    this.socket = socket;
  }

  /** connect opens a socket and resolves when it is ready to carry frames. */
  static connect(factory: SocketFactory, url: string, timeoutMs = REQUEST_TIMEOUT_MS): Promise<Connection> {
    return new Promise<Connection>((resolve, reject) => {
      let socket: Socket;
      try {
        socket = factory(url);
      } catch (cause) {
        reject(new TppError('disconnected', 'the relay could not be dialled', { cause }));
        return;
      }
      const conn = new Connection(socket);
      let settled = false;
      const timer = setTimeout(() => {
        if (!settled) {
          settled = true;
          socket.close();
          reject(new TppError('disconnected', 'the relay did not answer in time'));
        }
      }, timeoutMs);

      socket.onOpen(() => {
        if (settled) {
          return;
        }
        settled = true;
        clearTimeout(timer);
        resolve(conn);
      });
      socket.onMessage((frame) => conn.receive(frame));
      socket.onError((reason) => {
        if (!settled) {
          settled = true;
          clearTimeout(timer);
          reject(new TppError('disconnected', `the relay could not be reached: ${reason}`));
          return;
        }
        conn.fail(reason);
      });
      socket.onClose((reason) => {
        if (!settled) {
          settled = true;
          clearTimeout(timer);
          reject(new TppError('disconnected', `the relay closed the connection: ${reason}`));
          return;
        }
        conn.fail(reason);
      });
    });
  }

  /** events registers the handler for frames the relay pushes rather than answers. */
  events(handler: (env: Envelope) => void): void {
    this.onEvent = handler;
  }

  /** closed registers the handler that fires once, when this connection ends. */
  closed(handler: (reason: string) => void): void {
    this.onClosed = handler;
    if (this.closedReason !== null) {
      handler(this.closedReason);
    }
  }

  /** isClosed reports whether this connection has ended. */
  get isClosed(): boolean {
    return this.closedReason !== null;
  }

  /** close shuts the socket down and rejects everything waiting on it. */
  close(reason = 'closed by this client'): void {
    this.socket.close();
    this.fail(reason);
  }

  /** call sends a request and resolves with the decoded response, or rejects with a ProtocolError. */
  async call<Req, Resp>(
    type: MessageType,
    request: { codec: Codec<Req>; message: Req },
    response: Codec<Resp> | null,
    timeoutMs = REQUEST_TIMEOUT_MS,
  ): Promise<Resp> {
    const id = newEnvelopeID();
    const env = await this.exchange(id, type, request.codec.encode(request.message).finish(), timeoutMs);
    return decodeResponse(env, response);
  }

  /**
   * expect registers interest in the next pushed frame of a type, before the
   * request that triggers it is sent. Registering first is what makes the wait
   * race-free: the notice can arrive while the request is still in flight.
   */
  expect(type: MessageType, timeoutMs: number): Promise<Envelope> {
    return new Promise<Envelope>((resolve, reject) => {
      if (this.closedReason !== null) {
        reject(new TppError('disconnected', `connection lost: ${this.closedReason}`));
        return;
      }
      const waiter: Pending = { resolve, reject };
      const queue = this.waiters.get(type) ?? [];
      queue.push(waiter);
      this.waiters.set(type, queue);

      const timer = setTimeout(() => {
        this.dropWaiter(type, waiter);
        reject(new TppError('disconnected', `the relay sent no ${MessageType[type]} in time`));
      }, timeoutMs);
      const clear = () => clearTimeout(timer);
      waiter.resolve = (value) => {
        clear();
        resolve(value);
      };
      waiter.reject = (err) => {
        clear();
        reject(err);
      };
    });
  }

  /** send writes a frame that expects no correlated response. */
  send<T>(type: MessageType, codec: Codec<T>, message: T): void {
    this.write(newEnvelopeID(), type, codec.encode(message).finish());
  }

  private exchange(
    id: string,
    type: MessageType,
    payload: Uint8Array,
    timeoutMs: number,
  ): Promise<Envelope> {
    return new Promise<Envelope>((resolve, reject) => {
      if (this.closedReason !== null) {
        reject(new TppError('disconnected', `connection lost: ${this.closedReason}`));
        return;
      }
      const timer = setTimeout(() => {
        this.pending.delete(id);
        reject(new TppError('disconnected', `the relay did not answer ${MessageType[type]} in time`));
      }, timeoutMs);
      this.pending.set(id, {
        resolve: (env) => {
          clearTimeout(timer);
          resolve(env);
        },
        reject: (err) => {
          clearTimeout(timer);
          reject(err);
        },
      });
      try {
        this.write(id, type, payload);
      } catch (cause) {
        clearTimeout(timer);
        this.pending.delete(id);
        reject(new TppError('disconnected', `sending ${MessageType[type]} failed`, { cause }));
      }
    });
  }

  private write(id: string, type: MessageType, payload: Uint8Array): void {
    this.socket.send(Envelope.encode({ id, type, payload }).finish());
  }

  private receive(frame: Uint8Array): void {
    let env: Envelope;
    try {
      env = Envelope.decode(frame);
    } catch {
      // A frame this client cannot parse is dropped rather than treated as
      // fatal: the socket is still healthy and the next frame may be fine.
      return;
    }

    const pending = this.pending.get(env.id);
    if (pending) {
      this.pending.delete(env.id);
      pending.resolve(env);
      return;
    }
    const queue = this.waiters.get(env.type);
    if (queue && queue.length > 0) {
      const waiter = queue.shift() as Pending;
      if (queue.length === 0) {
        this.waiters.delete(env.type);
      }
      waiter.resolve(env);
      return;
    }
    this.onEvent(env);
  }

  private dropWaiter(type: MessageType, waiter: Pending): void {
    const queue = this.waiters.get(type);
    if (!queue) {
      return;
    }
    const index = queue.indexOf(waiter);
    if (index >= 0) {
      queue.splice(index, 1);
    }
    if (queue.length === 0) {
      this.waiters.delete(type);
    }
  }

  private fail(reason: string): void {
    if (this.closedReason !== null) {
      return;
    }
    this.closedReason = reason;
    const err = new TppError('disconnected', `connection lost: ${reason}`);
    for (const pending of this.pending.values()) {
      pending.reject(err);
    }
    this.pending.clear();
    for (const queue of this.waiters.values()) {
      for (const waiter of queue) {
        waiter.reject(err);
      }
    }
    this.waiters.clear();
    this.onClosed(reason);
  }
}

/** decodeResponse turns a response envelope into a message, or into a ProtocolError. */
export function decodeResponse<T>(env: Envelope, codec: Codec<T> | null): T {
  if (env.type === MessageType.MESSAGE_TYPE_ERROR) {
    const frame = ErrorFrame.decode(env.payload);
    throw new ProtocolError(frame.code, frame.message);
  }
  if (codec === null) {
    return undefined as T;
  }
  return codec.decode(env.payload);
}

/** newEnvelopeID returns a correlation id: 96 bits, which is plenty to keep concurrent requests on one socket apart. */
export function newEnvelopeID(): string {
  return toBase64URL(randomBytes(12));
}

/**
 * webSocketFactory dials with the platform's global WebSocket, which both
 * React Native and Node provide. Binary frames only: the read limit the relay
 * enforces is on ciphertext, and a text frame is a protocol error either way.
 */
export const webSocketFactory: SocketFactory = (url: string): Socket => {
  const ws = new WebSocket(url);
  ws.binaryType = 'arraybuffer';
  return {
    send: (frame) => ws.send(frame),
    close: () => ws.close(),
    onOpen: (handler) => {
      ws.onopen = () => handler();
    },
    onMessage: (handler) => {
      ws.onmessage = (event: MessageEvent) => {
        const data = event.data as ArrayBuffer | string;
        if (typeof data === 'string') {
          // The relay never sends text. Dropping it is what keeps a proxy's
          // injected message from being parsed as a frame.
          return;
        }
        handler(new Uint8Array(data));
      };
    },
    onClose: (handler) => {
      ws.onclose = (event: CloseEvent) => handler(event.reason || `code ${event.code}`);
    },
    onError: (handler) => {
      ws.onerror = () => handler('socket error');
    },
  };
};
