/**
 * A relay that speaks enough of the wire protocol to drive the client through
 * SPEC §3 and §6, in process.
 *
 * It is deliberately not a second implementation of the server: it holds no
 * Redis semantics, no TTLs and no blob backend, and it never tries to decide
 * whether something the client sent is cryptographically sound. What it does
 * is answer frames in the order and shape the contract defines, so the tests
 * can assert the *client's* rules — epoch handling, the id it binds, the
 * pairing handshake, the revocation gate.
 *
 * The real relay is exercised by `scripts/interop`, which runs the actual
 * server binary against a real Redis and pairs this client with the Go one.
 */

import { toBase64URL } from '../../bytes';
import { randomBytes } from '../../random';
import {
  CreateGroupRequest,
  CreateGroupResponse,
  Device as WireDevice,
  DeviceListResponse,
  RekeyRequest,
  RekeyResponse,
} from '../../../protocol/gen/tpp/v1/group';
import { DeviceRevoked, EpochChanged, WrappedKeyAvailable } from '../../../protocol/gen/tpp/v1/events';
import {
  EntryFetchRequest,
  EntryFetchResponse,
  EntryHistoryRequest,
  EntryHistoryResponse,
  EntryLatestResponse,
  EntryMeta,
  EntryPutRequest,
  EntryPutResponse,
} from '../../../protocol/gen/tpp/v1/entry';
import { Envelope, MessageType } from '../../../protocol/gen/tpp/v1/envelope';
import { Error as ErrorFrame, ErrorCode } from '../../../protocol/gen/tpp/v1/error';
import {
  PairingComplete,
  PairingJoinRequest,
  PairingJoinNotice,
  PairingOfferAcceptRequest,
  PairingOfferRequest,
  PairingOfferResponse,
  PairingStartResponse,
  PairingWrappedKeyUpload,
} from '../../../protocol/gen/tpp/v1/pairing';
import { Socket, SocketFactory } from '../connection';

interface StoredEntry {
  meta: EntryMeta;
  ciphertext: Uint8Array;
}

interface StoredOffer {
  name: string;
  publicKey: Uint8Array;
  /** socket is the offering device's own, which is the only way to reach it: it has no identity yet. */
  socket: RelaySocket;
  consumed?: boolean;
}

interface StoredDevice {
  device: WireDevice;
  /** The wrapped key waiting for a device that was offline during a rekey (SPEC §3.3). */
  pending?: { epoch: string; wrapped: Uint8Array };
}

/** FakeRelay is one relay holding one group. */
export class FakeRelay {
  readonly creationToken = 'creation-token';
  groupId = '';
  epoch = 1n;

  private devices = new Map<string, StoredDevice>();
  private sockets = new Map<string, RelaySocket>();
  private entries: StoredEntry[] = [];
  private pairings = new Map<string, string>(); // pairing token -> inviter device id
  private offers = new Map<string, StoredOffer>(); // offer code -> what the joiner registered
  private nextId = 1;

  /** creationURL is what an operator hands a user (SPEC §3.1). */
  get creationURL(): string {
    return `https://relay.test/${this.creationToken}`;
  }

  /** factory dials this relay. Pass it to the client as its SocketFactory. */
  readonly factory: SocketFactory = (url: string): Socket => {
    const deviceId = new URL(url).searchParams.get('device_id') ?? '';
    const socket = new RelaySocket(this, deviceId);
    if (deviceId !== '') {
      if (!this.devices.has(deviceId)) {
        // A device the relay no longer knows is refused, exactly as a revoked
        // one is: the socket never opens (SPEC §3.3 step 5).
        socket.refuse('unknown device');
        return socket.port;
      }
      this.sockets.set(deviceId, socket);
    }
    socket.open();
    return socket.port;
  };

  /** entryCount is what a test asserts when it needs to know nothing was written. */
  get entryCount(): number {
    return this.entries.length;
  }

  /** deviceIds lists the group's members. */
  get deviceIds(): string[] {
    return [...this.devices.keys()];
  }

  handle(socket: RelaySocket, env: Envelope): void {
    const reply = (type: MessageType, payload: Uint8Array) =>
      socket.push({ id: env.id, type, payload });
    const fail = (code: ErrorCode, message: string) =>
      reply(MessageType.MESSAGE_TYPE_ERROR, ErrorFrame.encode({ code, message }).finish());

    switch (env.type) {
      case MessageType.MESSAGE_TYPE_CREATE_GROUP_REQUEST: {
        const request = CreateGroupRequest.decode(env.payload);
        if (request.token !== this.creationToken || this.groupId !== '') {
          fail(ErrorCode.ERROR_CODE_TOKEN_CONSUMED, 'one token, one group');
          return;
        }
        this.groupId = 'group-1';
        const deviceId = this.addDevice(request.deviceName, request.devicePublicKey);
        reply(
          MessageType.MESSAGE_TYPE_CREATE_GROUP_RESPONSE,
          CreateGroupResponse.encode({
            groupId: this.groupId,
            deviceId,
            epoch: this.epoch.toString(),
          }).finish(),
        );
        return;
      }

      case MessageType.MESSAGE_TYPE_PAIRING_START_REQUEST: {
        const token = `pair-${this.nextId++}`;
        this.pairings.set(token, socket.deviceId);
        reply(
          MessageType.MESSAGE_TYPE_PAIRING_START_RESPONSE,
          PairingStartResponse.encode({
            pairingToken: token,
            expiresAtUnixMs: (Date.now() + 300_000).toString(),
          }).finish(),
        );
        return;
      }

      case MessageType.MESSAGE_TYPE_PAIRING_JOIN_REQUEST: {
        const request = PairingJoinRequest.decode(env.payload);
        const inviterId = this.pairings.get(request.pairingToken);
        if (inviterId === undefined) {
          fail(ErrorCode.ERROR_CODE_TOKEN_EXPIRED, 'no such pairing');
          return;
        }
        const deviceId = this.addDevice(request.deviceName, request.devicePublicKey);
        socket.joinerFor = request.pairingToken;
        this.sockets.get(inviterId)?.push({
          id: 'notice',
          type: MessageType.MESSAGE_TYPE_PAIRING_JOIN_NOTICE,
          payload: PairingJoinNotice.encode({
            pairingToken: request.pairingToken,
            deviceId,
            deviceName: request.deviceName,
            devicePublicKey: request.devicePublicKey,
          }).finish(),
        });
        this.joiners.set(request.pairingToken, socket);
        return;
      }

      case MessageType.MESSAGE_TYPE_PAIRING_WRAPPED_KEY_UPLOAD: {
        const request = PairingWrappedKeyUpload.decode(env.payload);
        const complete = PairingComplete.encode({
          groupId: this.groupId,
          deviceId: request.deviceId,
          epoch: this.epoch.toString(),
          wrappedGroupKey: request.wrappedGroupKey,
        }).finish();
        this.joiners.get(request.pairingToken)?.push({
          id: 'complete',
          type: MessageType.MESSAGE_TYPE_PAIRING_COMPLETE,
          payload: complete,
        });
        this.joiners.delete(request.pairingToken);
        this.pairings.delete(request.pairingToken);
        reply(MessageType.MESSAGE_TYPE_PAIRING_COMPLETE, complete);
        return;
      }

      case MessageType.MESSAGE_TYPE_PAIRING_OFFER_REQUEST: {
        const request = PairingOfferRequest.decode(env.payload);
        if (request.devicePublicKey.length === 0) {
          fail(ErrorCode.ERROR_CODE_INVALID_ARGUMENT, 'device_public_key is required');
          return;
        }
        const code = `offer-${this.nextId++}`;
        // The public key is stored at mint time precisely so the accept can be
        // checked against it, which is the real relay's whole defence here.
        this.offers.set(code, {
          name: request.deviceName,
          publicKey: request.devicePublicKey,
          socket,
        });
        reply(
          MessageType.MESSAGE_TYPE_PAIRING_OFFER_RESPONSE,
          PairingOfferResponse.encode({
            offerCode: code,
            expiresAtUnixMs: (Date.now() + 300_000).toString(),
          }).finish(),
        );
        return;
      }

      case MessageType.MESSAGE_TYPE_PAIRING_OFFER_ACCEPT_REQUEST: {
        const request = PairingOfferAcceptRequest.decode(env.payload);
        const offer = this.offers.get(request.offerCode);
        if (offer === undefined) {
          fail(ErrorCode.ERROR_CODE_TOKEN_EXPIRED, 'no such offer');
          return;
        }
        if (offer.consumed === true) {
          fail(ErrorCode.ERROR_CODE_TOKEN_CONSUMED, 'this offer has already admitted a device');
          return;
        }
        // Byte for byte: a member that wrapped to a key the offer was not made
        // with cannot complete the pairing.
        if (toBase64URL(request.devicePublicKey) !== toBase64URL(offer.publicKey)) {
          fail(ErrorCode.ERROR_CODE_INVALID_ARGUMENT, 'that is not the key this offer was made with');
          return;
        }
        offer.consumed = true;

        const deviceId = this.addDevice(offer.name, offer.publicKey);
        const complete = PairingComplete.encode({
          groupId: this.groupId,
          deviceId,
          epoch: this.epoch.toString(),
          wrappedGroupKey: request.wrappedGroupKey,
        }).finish();
        offer.socket.push({
          id: 'complete',
          type: MessageType.MESSAGE_TYPE_PAIRING_COMPLETE,
          payload: complete,
        });
        reply(MessageType.MESSAGE_TYPE_PAIRING_COMPLETE, complete);
        return;
      }

      case MessageType.MESSAGE_TYPE_DEVICE_LIST_REQUEST: {
        reply(
          MessageType.MESSAGE_TYPE_DEVICE_LIST_RESPONSE,
          DeviceListResponse.encode({
            devices: [...this.devices.values()].map((d) => d.device),
            epoch: this.epoch.toString(),
          }).finish(),
        );
        return;
      }

      case MessageType.MESSAGE_TYPE_REKEY_REQUEST: {
        const request = RekeyRequest.decode(env.payload);
        if (request.expectedEpoch !== this.epoch.toString()) {
          fail(ErrorCode.ERROR_CODE_EPOCH_CONFLICT, 'another device re-keyed first');
          return;
        }
        this.devices.delete(request.revokedDeviceId);
        this.sockets.get(request.revokedDeviceId)?.drop('revoked');
        this.sockets.delete(request.revokedDeviceId);
        this.epoch += 1n;

        // Applied all at once, as the real relay does in one transaction.
        for (const key of request.wrappedKeys) {
          const stored = this.devices.get(key.deviceId);
          if (!stored) {
            continue;
          }
          stored.pending = { epoch: this.epoch.toString(), wrapped: key.wrappedGroupKey };
          const live = this.sockets.get(key.deviceId);
          if (live && key.deviceId !== socket.deviceId) {
            this.deliverPending(key.deviceId);
            live.push({
              id: 'epoch',
              type: MessageType.MESSAGE_TYPE_EPOCH_CHANGED,
              payload: EpochChanged.encode({
                epoch: this.epoch.toString(),
                revokedDeviceId: request.revokedDeviceId,
              }).finish(),
            });
          }
        }
        for (const [id, live] of this.sockets) {
          if (id !== socket.deviceId) {
            live.push({
              id: 'revoked',
              type: MessageType.MESSAGE_TYPE_DEVICE_REVOKED,
              payload: DeviceRevoked.encode({
                deviceId: request.revokedDeviceId,
                epoch: this.epoch.toString(),
              }).finish(),
            });
          }
        }
        reply(
          MessageType.MESSAGE_TYPE_REKEY_RESPONSE,
          RekeyResponse.encode({ epoch: this.epoch.toString() }).finish(),
        );
        return;
      }

      case MessageType.MESSAGE_TYPE_ENTRY_PUT_REQUEST: {
        const request = EntryPutRequest.decode(env.payload);
        if (this.entries.some((e) => e.meta.entryId === request.entryId)) {
          // An id already in use is refused rather than overwritten.
          fail(ErrorCode.ERROR_CODE_INVALID_ARGUMENT, 'entry id already in use');
          return;
        }
        const meta: EntryMeta = {
          entryId: this.rewriteEntryId ?? request.entryId,
          epoch: request.epoch,
          size: request.size,
          createdAtUnixMs: Date.now().toString(),
          expiresAtUnixMs: (Date.now() + 86_400_000).toString(),
          inline: true,
        };
        this.entries.push({ meta, ciphertext: request.ciphertext ?? new Uint8Array(0) });
        reply(
          MessageType.MESSAGE_TYPE_ENTRY_PUT_RESPONSE,
          EntryPutResponse.encode({ meta }).finish(),
        );
        return;
      }

      case MessageType.MESSAGE_TYPE_ENTRY_LATEST_REQUEST: {
        const latest = this.entries[this.entries.length - 1];
        reply(
          MessageType.MESSAGE_TYPE_ENTRY_LATEST_RESPONSE,
          EntryLatestResponse.encode(
            latest
              ? { meta: latest.meta, ciphertext: latest.ciphertext }
              : { ciphertext: new Uint8Array(0) },
          ).finish(),
        );
        return;
      }

      case MessageType.MESSAGE_TYPE_ENTRY_HISTORY_REQUEST: {
        EntryHistoryRequest.decode(env.payload);
        this.historyRequests++;
        reply(
          MessageType.MESSAGE_TYPE_ENTRY_HISTORY_RESPONSE,
          EntryHistoryResponse.encode({
            entries: [...this.entries].reverse().map((e) => e.meta),
            nextBeforeUnixMs: '0',
          }).finish(),
        );
        return;
      }

      case MessageType.MESSAGE_TYPE_ENTRY_FETCH_REQUEST: {
        const request = EntryFetchRequest.decode(env.payload);
        const found = this.entries.find((e) => e.meta.entryId === request.entryId);
        if (!found) {
          fail(ErrorCode.ERROR_CODE_NOT_FOUND, 'no such entry');
          return;
        }
        reply(
          MessageType.MESSAGE_TYPE_ENTRY_FETCH_RESPONSE,
          EntryFetchResponse.encode({ meta: found.meta, ciphertext: found.ciphertext }).finish(),
        );
        return;
      }

      default:
        fail(ErrorCode.ERROR_CODE_UNSUPPORTED_MESSAGE, 'unsupported message');
    }
  }

  /** rewriteEntryId makes the relay file the next entry under another id, to prove the client refuses it. */
  rewriteEntryId: string | null = null;

  /** historyRequests counts explicit history listings: nothing must list history on its own. */
  historyRequests = 0;

  private joiners = new Map<string, RelaySocket>();

  /** detach forgets a socket the client closed, so a wrapped key waits for the reconnect instead of being pushed into a dead socket. */
  detach(socket: RelaySocket): void {
    if (this.sockets.get(socket.deviceId) === socket) {
      this.sockets.delete(socket.deviceId);
    }
  }

  /** deliverPending hands a device the wrapped key that was waiting for it (SPEC §3.3). */
  deliverPending(deviceId: string): void {
    const stored = this.devices.get(deviceId);
    const socket = this.sockets.get(deviceId);
    if (!stored?.pending || !socket) {
      return;
    }
    socket.push({
      id: 'wrapped',
      type: MessageType.MESSAGE_TYPE_WRAPPED_KEY_AVAILABLE,
      payload: WrappedKeyAvailable.encode({
        epoch: stored.pending.epoch,
        wrappedGroupKey: stored.pending.wrapped,
      }).finish(),
    });
    stored.pending = undefined;
  }

  private addDevice(name: string, publicKey: Uint8Array): string {
    const deviceId = `device-${this.nextId++}`;
    this.devices.set(deviceId, {
      device: {
        deviceId,
        name,
        publicKey,
        createdAtUnixMs: Date.now().toString(),
        lastSeenUnixMs: '0',
      },
    });
    return deviceId;
  }
}

/** RelaySocket is the relay's end of one connection. */
class RelaySocket {
  joinerFor: string | null = null;

  private openHandler: () => void = () => {};
  private messageHandler: (frame: Uint8Array) => void = () => {};
  private closeHandler: (reason: string) => void = () => {};
  private errorHandler: (reason: string) => void = () => {};
  private closed = false;

  constructor(
    private readonly relay: FakeRelay,
    readonly deviceId: string,
  ) {}

  /** port is the client's end: the Socket the connection drives. */
  readonly port: Socket = {
    send: (frame: Uint8Array) => {
      if (this.closed) {
        return;
      }
      // Asynchronous, like a real socket: a client that only works when the
      // reply is synchronous is a client that does not work.
      queueMicrotask(() => this.relay.handle(this, Envelope.decode(frame)));
    },
    close: () => {
      this.closed = true;
      this.relay.detach(this);
    },
    onOpen: (handler) => {
      this.openHandler = handler;
    },
    onMessage: (handler) => {
      this.messageHandler = handler;
    },
    onClose: (handler) => {
      this.closeHandler = handler;
    },
    onError: (handler) => {
      this.errorHandler = handler;
    },
  };

  open(): void {
    queueMicrotask(() => {
      if (!this.closed) {
        this.openHandler();
        this.relay.deliverPending(this.deviceId);
      }
    });
  }

  refuse(reason: string): void {
    queueMicrotask(() => this.errorHandler(reason));
  }

  push(env: { id: string; type: MessageType; payload: Uint8Array }): void {
    if (this.closed) {
      return;
    }
    queueMicrotask(() => this.messageHandler(Envelope.encode(env).finish()));
  }

  drop(reason: string): void {
    this.closed = true;
    queueMicrotask(() => this.closeHandler(reason));
  }
}

/** MemoryStore is a SecureStore for tests: the same interface the Keystore-backed one implements. */
export class MemoryStore {
  private readonly values = new Map<string, string>();

  async load(name: string): Promise<string | null> {
    return this.values.get(name) ?? null;
  }

  async save(name: string, value: string): Promise<void> {
    this.values.set(name, value);
  }

  async clear(name: string): Promise<void> {
    this.values.delete(name);
  }

  /** raw is what a test asserts against when it needs to know what was persisted. */
  peek(name: string): string | null {
    return this.values.get(name) ?? null;
  }
}

/** randomName keeps two clients in one test apart without a fixture. */
export const randomName = (prefix: string): string => `${prefix}-${toBase64URL(randomBytes(3))}`;
