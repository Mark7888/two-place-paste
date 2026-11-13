/**
 * A relay that speaks enough of the wire protocol to drive the client through
 * SPEC §3, §6 and §6b, in process.
 *
 * It has two users. The tests use it to assert the *client's* rules — epoch
 * handling, the id it binds, the pairing handshake, the revocation gate — and
 * the app's demo mode (docs/internal/plans/demo-mode.md) runs it behind the
 * client's socket factory, so a phone with no relay can still reach every
 * screen.
 *
 * It is deliberately not a second implementation of the server: it holds no
 * Redis semantics, no TTLs and no blob backend, and it never tries to decide
 * whether something the client sent is cryptographically sound. What it does
 * is answer frames in the order and shape the contract defines. What it keeps
 * is held in memory and capped, because on a phone that memory is the app's.
 *
 * The real relay is exercised by `scripts/interop`, which runs the actual
 * server binary against a real Redis and pairs this client with the Go one.
 */

import { toBase64URL } from '../bytes';
import {
  CreateGroupRequest,
  CreateGroupResponse,
  Device as WireDevice,
  DeviceListResponse,
  GroupInfoResponse,
  RekeyRequest,
  RekeyResponse,
} from '../../protocol/gen/tpp/v1/group';
import {
  DeviceRevoked,
  EntryAdded,
  EntryEvicted,
  EpochChanged,
  GroupRenamed,
  StoreItemAdded,
  StoreItemDeleted,
  StoreItemUpdated,
  WrappedKeyAvailable,
} from '../../protocol/gen/tpp/v1/events';
import {
  EntryFetchChunkRequest,
  EntryFetchChunkResponse,
  EntryFetchRequest,
  EntryFetchResponse,
  EntryHistoryRequest,
  EntryHistoryResponse,
  EntryLatestResponse,
  EntryMeta,
  EntryLimitsResponse,
  EntryPutRequest,
  EntryPutResponse,
  EntryUploadAbortRequest,
  EntryUploadAbortResponse,
  EntryUploadBeginRequest,
  EntryUploadBeginResponse,
  EntryUploadChunkRequest,
  EntryUploadChunkResponse,
} from '../../protocol/gen/tpp/v1/entry';
import { Envelope, MessageType } from '../../protocol/gen/tpp/v1/envelope';
import { Error as ErrorFrame, ErrorCode } from '../../protocol/gen/tpp/v1/error';
import {
  PairingComplete,
  PairingJoinRequest,
  PairingJoinNotice,
  PairingOfferAcceptRequest,
  PairingOfferRequest,
  PairingOfferResponse,
  PairingStartResponse,
  PairingWrappedKeyUpload,
} from '../../protocol/gen/tpp/v1/pairing';
import {
  StoreDeleteRequest,
  StoreDeleteResponse,
  StoreFetchChunkRequest,
  StoreFetchChunkResponse,
  StoreItemMeta,
  StoreLimitsResponse,
  StoreLinksResponse,
  StoreListResponse,
  StorePutRequest,
  StorePutResponse,
  StoreUpdateMetaRequest,
  StoreUpdateMetaResponse,
  StoreUploadBeginRequest,
  StoreUploadBeginResponse,
} from '../../protocol/gen/tpp/v1/store';
import { SINGLE_FRAME_MAX_BYTES } from '../protocol/client';
import { Socket, SocketFactory } from '../protocol/connection';
import { websocketURL } from '../protocol/urls';

/** DEFAULT_HISTORY_LIMIT is the page size for a history request that names none, as the real relay's default. */
const DEFAULT_HISTORY_LIMIT = 50;

/** CODE_LIFETIME_MS is how long a pairing token or an offer lives (SPEC §3.2). */
const CODE_LIFETIME_MS = 300_000;

/** ENTRY_LIFETIME_MS is the fixed life of an entry (SPEC §4.5). */
const ENTRY_LIFETIME_MS = 86_400_000;

interface StoredEntry {
  meta: EntryMeta;
  ciphertext: Uint8Array;
}

interface StoredOffer {
  name: string;
  publicKey: Uint8Array;
  /** socket is the offering device's own, which is the only way to reach it: it has no identity yet. */
  socket: RelaySocket;
  /** expiresAt is when the offer stops being accepted, by the relay's clock. */
  expiresAt: number;
  consumed?: boolean;
}

interface StoredDevice {
  device: WireDevice;
  /** The wrapped key waiting for a device that was offline during a rekey (SPEC §3.3). */
  pending?: { epoch: string; wrapped: Uint8Array };
}

interface StoredItem {
  meta: StoreItemMeta;
  blob: Uint8Array;
}

/** Upload is a chunked upload in progress, for an entry or a store item. */
interface Upload {
  kind: 'entry' | 'store';
  /** deviceId is the device that began it: a handle is valid for that device only. */
  deviceId: string;
  id: string;
  epoch: string;
  size: number;
  chunkBytes: number;
  buf: Uint8Array;
  received: number;
}

/** StoreConfig is the store a relay offers, or null for a relay that predates it. */
export interface StoreConfig {
  maxBytes: number;
  maxItems: number;
  maxItemBytes: number;
}

/** FakeRelayOptions configures a FakeRelay; every field has the tests' default. */
export interface FakeRelayOptions {
  /** baseUrl is the address the relay answers at. Sockets to any other address are refused. */
  baseUrl?: string;
  /** now is the relay's clock, in milliseconds. */
  now?: () => number;
  /** groupName is what the admin has called the group. */
  groupName?: string;
  /** groupId is the id the group is created with; two relays in one test need two. */
  groupId?: string;
  /** limits are what EntryLimitsRequest is answered with; null answers as a relay that predates it. */
  limits?: EntryLimitsResponse | null;
  /** store is the group's store; null answers every store request as a relay that predates it. */
  store?: StoreConfig | null;
  /** maxEntries caps the history: the oldest entry is evicted to make room, as the real relay does at its storage limit. */
  maxEntries?: number;
  /** maxEntryBytes caps the bytes all entries take together, evicting the oldest the same way. */
  maxEntryBytes?: number;
}

/** FakeRelay is one relay holding one group. */
export class FakeRelay {
  readonly creationToken = 'creation-token';
  readonly baseUrl: string;
  groupId = '';
  epoch = 1n;

  /** onPairingStarted is told when a member mints a pairing token. The demo answers it with a joining device. */
  onPairingStarted: ((inviterId: string, token: string) => void) | null = null;

  /** onRevoked is told when a device is revoked. The demo stops that device's client. */
  onRevoked: ((deviceId: string) => void) | null = null;

  private readonly now: () => number;
  private readonly endpoint: string;
  private readonly storeConfig: StoreConfig | null;
  private readonly maxEntries: number;
  private readonly maxEntryBytes: number;
  private readonly createdGroupId: string;

  private devices = new Map<string, StoredDevice>();
  private sockets = new Map<string, RelaySocket>();
  private entries: StoredEntry[] = [];
  private items: StoredItem[] = []; // oldest first; listed newest first
  private uploads = new Map<string, Upload>();
  private pairings = new Map<string, { inviterId: string; expiresAt: number }>(); // pairing token -> inviter
  private offers = new Map<string, StoredOffer>(); // offer code -> what the joiner registered
  private nextId = 1;

  constructor(options: FakeRelayOptions = {}) {
    this.baseUrl = options.baseUrl ?? 'https://relay.test';
    this.endpoint = websocketURL(this.baseUrl, '');
    this.now = options.now ?? Date.now;
    this.groupName = options.groupName ?? 'Family';
    this.createdGroupId = options.groupId ?? 'group-1';
    this.limits = options.limits ?? null;
    this.storeConfig = options.store ?? null;
    this.maxEntries = options.maxEntries ?? Number.POSITIVE_INFINITY;
    this.maxEntryBytes = options.maxEntryBytes ?? Number.POSITIVE_INFINITY;
  }

  /** creationURL is what an operator hands a user (SPEC §3.1). */
  get creationURL(): string {
    return `${this.baseUrl}/${this.creationToken}`;
  }

  /**
   * factory dials this relay. Pass it to the client as its SocketFactory.
   *
   * A URL for any other host is refused rather than dialled: in the demo this
   * is what keeps a code from a real relay, scanned by mistake, from reaching
   * the network through a client that was built not to.
   */
  readonly factory: SocketFactory = (url: string): Socket => {
    if (this.closed || (url !== this.endpoint && !url.startsWith(`${this.endpoint}?`))) {
      const refused = new RelaySocket(this, '');
      refused.refuse('this relay only answers at its own address');
      return refused.port;
    }
    const deviceId = deviceIdOf(url);
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

  /** itemCount is how many store items the relay holds. */
  get itemCount(): number {
    return this.items.length;
  }

  /** uploadCount is how many chunked uploads are open. */
  get uploadCount(): number {
    return this.uploads.size;
  }

  /** deviceIds lists the group's members. */
  get deviceIds(): string[] {
    return [...this.devices.keys()];
  }

  /** deviceName is a member's name, or "" for a device the relay does not know. */
  deviceName(deviceId: string): string {
    return this.devices.get(deviceId)?.device.name ?? '';
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
        this.groupId = this.createdGroupId;
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
        const expiresAt = this.now() + CODE_LIFETIME_MS;
        this.pairings.set(token, { inviterId: socket.deviceId, expiresAt });
        reply(
          MessageType.MESSAGE_TYPE_PAIRING_START_RESPONSE,
          PairingStartResponse.encode({
            pairingToken: token,
            expiresAtUnixMs: expiresAt.toString(),
          }).finish(),
        );
        this.onPairingStarted?.(socket.deviceId, token);
        return;
      }

      case MessageType.MESSAGE_TYPE_PAIRING_JOIN_REQUEST: {
        const request = PairingJoinRequest.decode(env.payload);
        const pairing = this.pairings.get(request.pairingToken);
        if (pairing === undefined || pairing.expiresAt <= this.now()) {
          fail(ErrorCode.ERROR_CODE_TOKEN_EXPIRED, 'no such pairing');
          return;
        }
        const deviceId = this.addDevice(request.deviceName, request.devicePublicKey);
        socket.joinerFor = request.pairingToken;
        this.sockets.get(pairing.inviterId)?.push({
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
        const expiresAt = this.now() + CODE_LIFETIME_MS;
        // The public key is stored at mint time precisely so the accept can be
        // checked against it, which is the real relay's whole defence here.
        this.offers.set(code, {
          name: request.deviceName,
          publicKey: request.devicePublicKey,
          socket,
          expiresAt,
        });
        reply(
          MessageType.MESSAGE_TYPE_PAIRING_OFFER_RESPONSE,
          PairingOfferResponse.encode({
            offerCode: code,
            expiresAtUnixMs: expiresAt.toString(),
          }).finish(),
        );
        return;
      }

      case MessageType.MESSAGE_TYPE_PAIRING_OFFER_ACCEPT_REQUEST: {
        const request = PairingOfferAcceptRequest.decode(env.payload);
        const offer = this.offers.get(request.offerCode);
        if (offer === undefined || offer.expiresAt <= this.now()) {
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
        this.storeLinks.set(this.epoch, request.storeLink);

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
        this.broadcast(socket, MessageType.MESSAGE_TYPE_DEVICE_REVOKED, DeviceRevoked.encode({
          deviceId: request.revokedDeviceId,
          epoch: this.epoch.toString(),
        }).finish());
        reply(
          MessageType.MESSAGE_TYPE_REKEY_RESPONSE,
          RekeyResponse.encode({ epoch: this.epoch.toString() }).finish(),
        );
        this.onRevoked?.(request.revokedDeviceId);
        return;
      }

      case MessageType.MESSAGE_TYPE_ENTRY_PUT_REQUEST: {
        const request = EntryPutRequest.decode(env.payload);
        if (this.entries.some((e) => e.meta.entryId === request.entryId)) {
          // An id already in use is refused rather than overwritten.
          fail(ErrorCode.ERROR_CODE_INVALID_ARGUMENT, 'entry id already in use');
          return;
        }
        // A write sealed under any epoch but the group's current one is
        // refused, as the real relay does (SPEC §3.3).
        if (request.epoch !== this.epoch.toString()) {
          fail(ErrorCode.ERROR_CODE_EPOCH_CONFLICT, 'the entry is not sealed at the group’s epoch');
          return;
        }
        let ciphertext = request.ciphertext ?? new Uint8Array(0);
        // A relay with limits keeps only small entries inline, as the real one
        // does; the rest are read by id. One without them predates the split.
        let inline = this.limits === null || ciphertext.length <= Number(this.limits.inlineMaxBytes);
        if (request.uploadHandle !== undefined) {
          const upload = this.finishUpload(socket, 'entry', request.uploadHandle, request.entryId, request.epoch, request.size);
          if (typeof upload === 'string') {
            fail(ErrorCode.ERROR_CODE_INVALID_ARGUMENT, upload);
            return;
          }
          ciphertext = upload;
          inline = false;
        }
        const now = this.now();
        const meta: EntryMeta = {
          entryId: this.rewriteEntryId ?? request.entryId,
          epoch: request.epoch,
          size: request.size,
          createdAtUnixMs: now.toString(),
          expiresAtUnixMs: (now + ENTRY_LIFETIME_MS).toString(),
          inline,
          header: request.header,
        };
        this.entries.push({ meta, ciphertext });
        reply(
          MessageType.MESSAGE_TYPE_ENTRY_PUT_RESPONSE,
          EntryPutResponse.encode({ meta }).finish(),
        );
        this.broadcast(socket, MessageType.MESSAGE_TYPE_ENTRY_ADDED, EntryAdded.encode({ meta }).finish());
        this.evictEntries();
        return;
      }

      case MessageType.MESSAGE_TYPE_ENTRY_UPLOAD_BEGIN_REQUEST: {
        const request = EntryUploadBeginRequest.decode(env.payload);
        const size = Number(request.size);
        if (this.limits === null) {
          fail(ErrorCode.ERROR_CODE_UNSUPPORTED_MESSAGE, 'unsupported message');
          return;
        }
        if (!(size > 0) || size > Number(this.limits.maxEntryBytes)) {
          fail(ErrorCode.ERROR_CODE_TOO_LARGE, 'the entry is over the relay’s limit');
          return;
        }
        if (this.entries.some((e) => e.meta.entryId === request.entryId)) {
          fail(ErrorCode.ERROR_CODE_INVALID_ARGUMENT, 'entry id already in use');
          return;
        }
        if (request.epoch !== this.epoch.toString()) {
          fail(ErrorCode.ERROR_CODE_EPOCH_CONFLICT, 'the entry is not sealed at the group’s epoch');
          return;
        }
        const handle = this.beginUpload(socket, 'entry', request.entryId, request.epoch, size);
        reply(
          MessageType.MESSAGE_TYPE_ENTRY_UPLOAD_BEGIN_RESPONSE,
          EntryUploadBeginResponse.encode({
            uploadHandle: handle,
            entryId: request.entryId,
            chunkBytes: this.chunkBytes.toString(),
          }).finish(),
        );
        return;
      }

      case MessageType.MESSAGE_TYPE_ENTRY_UPLOAD_CHUNK_REQUEST: {
        // Chunks of a store upload travel in this same message (store.proto).
        const request = EntryUploadChunkRequest.decode(env.payload);
        const upload = this.uploads.get(request.uploadHandle);
        if (upload === undefined || upload.deviceId !== socket.deviceId) {
          fail(ErrorCode.ERROR_CODE_NOT_FOUND, 'no such upload');
          return;
        }
        const offset = request.index * upload.chunkBytes;
        const last = offset + request.data.length === upload.size;
        if (
          offset !== upload.received ||
          request.data.length === 0 ||
          offset + request.data.length > upload.size ||
          (!last && request.data.length !== upload.chunkBytes)
        ) {
          fail(ErrorCode.ERROR_CODE_INVALID_ARGUMENT, 'that chunk does not continue the upload');
          return;
        }
        upload.buf.set(request.data, offset);
        upload.received += request.data.length;
        reply(
          MessageType.MESSAGE_TYPE_ENTRY_UPLOAD_CHUNK_RESPONSE,
          EntryUploadChunkResponse.encode({ receivedBytes: upload.received.toString() }).finish(),
        );
        return;
      }

      case MessageType.MESSAGE_TYPE_ENTRY_UPLOAD_ABORT_REQUEST: {
        const request = EntryUploadAbortRequest.decode(env.payload);
        const upload = this.uploads.get(request.uploadHandle);
        if (upload !== undefined && upload.deviceId === socket.deviceId) {
          this.uploads.delete(request.uploadHandle);
        }
        reply(MessageType.MESSAGE_TYPE_ENTRY_UPLOAD_ABORT_RESPONSE, EntryUploadAbortResponse.encode({}).finish());
        return;
      }

      case MessageType.MESSAGE_TYPE_ENTRY_LATEST_REQUEST: {
        const latest = this.entries[this.entries.length - 1];
        reply(
          MessageType.MESSAGE_TYPE_ENTRY_LATEST_RESPONSE,
          EntryLatestResponse.encode(
            latest
              ? { meta: latest.meta, ciphertext: latest.meta.inline ? latest.ciphertext : new Uint8Array(0) }
              : { ciphertext: new Uint8Array(0) },
          ).finish(),
        );
        return;
      }

      case MessageType.MESSAGE_TYPE_ENTRY_HISTORY_REQUEST: {
        const request = EntryHistoryRequest.decode(env.payload);
        this.historyRequests++;
        const limit = request.limit === 0 ? DEFAULT_HISTORY_LIMIT : request.limit;
        const before = Number(request.beforeUnixMs);
        const newestFirst = [...this.entries]
          .reverse()
          .filter((e) => before === 0 || Number(e.meta.createdAtUnixMs) < before);
        const page = newestFirst.slice(0, limit);
        const more = newestFirst.length > page.length;
        reply(
          MessageType.MESSAGE_TYPE_ENTRY_HISTORY_RESPONSE,
          EntryHistoryResponse.encode({
            entries: page.map((e) => e.meta),
            nextBeforeUnixMs: more ? page[page.length - 1].meta.createdAtUnixMs : '0',
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

      case MessageType.MESSAGE_TYPE_ENTRY_FETCH_CHUNK_REQUEST: {
        const request = EntryFetchChunkRequest.decode(env.payload);
        const found = this.entries.find((e) => e.meta.entryId === request.entryId);
        if (!found) {
          fail(ErrorCode.ERROR_CODE_NOT_FOUND, 'no such entry');
          return;
        }
        const range = this.range(found.ciphertext, Number(request.offset), Number(request.length));
        if (range === null) {
          fail(ErrorCode.ERROR_CODE_INVALID_ARGUMENT, 'offset is past the end of the entry');
          return;
        }
        reply(
          MessageType.MESSAGE_TYPE_ENTRY_FETCH_CHUNK_RESPONSE,
          EntryFetchChunkResponse.encode({ meta: found.meta, offset: request.offset, data: range }).finish(),
        );
        return;
      }

      case MessageType.MESSAGE_TYPE_ENTRY_LIMITS_REQUEST: {
        // A relay that predates the limits request does not know it; the
        // client then falls back to the limits such a relay always had.
        if (this.limits === null) {
          fail(ErrorCode.ERROR_CODE_UNSUPPORTED_MESSAGE, 'unsupported message');
          return;
        }
        reply(MessageType.MESSAGE_TYPE_ENTRY_LIMITS_RESPONSE, EntryLimitsResponse.encode(this.limits).finish());
        return;
      }

      case MessageType.MESSAGE_TYPE_GROUP_INFO_REQUEST: {
        reply(
          MessageType.MESSAGE_TYPE_GROUP_INFO_RESPONSE,
          GroupInfoResponse.encode({ name: this.groupName, operatorUrl: '' }).finish(),
        );
        return;
      }

      default:
        this.handleStore(socket, env, reply, fail);
    }
  }

  /** handleStore answers the store's requests (SPEC §6b), and refuses everything else. */
  private handleStore(
    socket: RelaySocket,
    env: Envelope,
    reply: (type: MessageType, payload: Uint8Array) => void,
    fail: (code: ErrorCode, message: string) => void,
  ): void {
    const config = this.storeConfig;
    const isStore =
      env.type >= MessageType.MESSAGE_TYPE_STORE_LIMITS_REQUEST &&
      env.type <= MessageType.MESSAGE_TYPE_STORE_DELETE_RESPONSE;
    if (config === null || !isStore) {
      fail(ErrorCode.ERROR_CODE_UNSUPPORTED_MESSAGE, 'unsupported message');
      return;
    }

    switch (env.type) {
      case MessageType.MESSAGE_TYPE_STORE_LIMITS_REQUEST: {
        const used = this.storeUsedBytes();
        reply(
          MessageType.MESSAGE_TYPE_STORE_LIMITS_RESPONSE,
          StoreLimitsResponse.encode({
            enabled: true,
            maxBytes: config.maxBytes.toString(),
            usedBytes: used.toString(),
            availableBytes: Math.max(config.maxBytes - used, 0).toString(),
            maxItems: config.maxItems.toString(),
            usedItems: this.items.length.toString(),
            maxItemBytes: config.maxItemBytes.toString(),
          }).finish(),
        );
        return;
      }

      case MessageType.MESSAGE_TYPE_STORE_LIST_REQUEST: {
        // One page: the client asks for the next only when a cursor says so.
        reply(
          MessageType.MESSAGE_TYPE_STORE_LIST_RESPONSE,
          StoreListResponse.encode({
            items: [...this.items].reverse().map((it) => it.meta),
            nextCursor: '',
          }).finish(),
        );
        return;
      }

      case MessageType.MESSAGE_TYPE_STORE_LINKS_REQUEST: {
        const links = [...this.storeLinks.entries()]
          .filter(([, link]) => link.length > 0)
          .sort(([a], [b]) => (a < b ? -1 : a > b ? 1 : 0))
          .map(([epoch, link]) => ({ epoch: epoch.toString(), link }));
        reply(MessageType.MESSAGE_TYPE_STORE_LINKS_RESPONSE, StoreLinksResponse.encode({ links }).finish());
        return;
      }

      case MessageType.MESSAGE_TYPE_STORE_UPLOAD_BEGIN_REQUEST: {
        const request = StoreUploadBeginRequest.decode(env.payload);
        const size = Number(request.size);
        const refusal = this.storeRefusal(config, request.itemId, request.epoch, size);
        if (refusal !== null) {
          fail(refusal.code, refusal.message);
          return;
        }
        const handle = this.beginUpload(socket, 'store', request.itemId, request.epoch, size);
        reply(
          MessageType.MESSAGE_TYPE_STORE_UPLOAD_BEGIN_RESPONSE,
          StoreUploadBeginResponse.encode({ uploadHandle: handle, chunkBytes: this.chunkBytes.toString() }).finish(),
        );
        return;
      }

      case MessageType.MESSAGE_TYPE_STORE_PUT_REQUEST: {
        const request = StorePutRequest.decode(env.payload);
        const size = Number(request.size);
        let blob: Uint8Array;
        if (request.uploadHandle !== undefined) {
          const upload = this.finishUpload(socket, 'store', request.uploadHandle, request.itemId, request.epoch, request.size);
          if (typeof upload === 'string') {
            fail(ErrorCode.ERROR_CODE_INVALID_ARGUMENT, upload);
            return;
          }
          if (request.epoch !== this.epoch.toString()) {
            fail(ErrorCode.ERROR_CODE_EPOCH_CONFLICT, 'the group was re-keyed during the upload');
            return;
          }
          blob = upload;
        } else {
          const refusal = this.storeRefusal(config, request.itemId, request.epoch, size);
          if (refusal !== null) {
            fail(refusal.code, refusal.message);
            return;
          }
          blob = request.ciphertext ?? new Uint8Array(0);
          if (blob.length !== size) {
            fail(ErrorCode.ERROR_CODE_INVALID_ARGUMENT, 'size does not match the ciphertext');
            return;
          }
        }
        const meta: StoreItemMeta = {
          itemId: request.itemId,
          savedAtUnixMs: this.now().toString(),
          size: request.size,
          wrapEpoch: request.epoch,
          contentId: request.itemId,
          contentEpoch: request.epoch,
          wrappedItemKeys: request.wrappedItemKeys,
          sealedMeta: request.sealedMeta,
          metaRev: '0',
        };
        this.items.push({ meta, blob });
        reply(MessageType.MESSAGE_TYPE_STORE_PUT_RESPONSE, StorePutResponse.encode({ item: meta }).finish());
        this.broadcast(socket, MessageType.MESSAGE_TYPE_STORE_ITEM_ADDED, StoreItemAdded.encode({ item: meta }).finish());
        return;
      }

      case MessageType.MESSAGE_TYPE_STORE_FETCH_CHUNK_REQUEST: {
        const request = StoreFetchChunkRequest.decode(env.payload);
        const found = this.items.find((it) => it.meta.itemId === request.itemId);
        if (found === undefined) {
          fail(ErrorCode.ERROR_CODE_NOT_FOUND, 'no such item');
          return;
        }
        const range = this.range(found.blob, Number(request.offset), Number(request.length));
        if (range === null) {
          fail(ErrorCode.ERROR_CODE_INVALID_ARGUMENT, 'offset is past the end of the item');
          return;
        }
        reply(
          MessageType.MESSAGE_TYPE_STORE_FETCH_CHUNK_RESPONSE,
          StoreFetchChunkResponse.encode({ item: found.meta, offset: request.offset, data: range }).finish(),
        );
        return;
      }

      case MessageType.MESSAGE_TYPE_STORE_UPDATE_META_REQUEST: {
        const request = StoreUpdateMetaRequest.decode(env.payload);
        const found = this.items.find((it) => it.meta.itemId === request.itemId);
        if (found === undefined) {
          fail(ErrorCode.ERROR_CODE_NOT_FOUND, 'no such item');
          return;
        }
        if (found.meta.metaRev !== request.expectedMetaRev) {
          fail(ErrorCode.ERROR_CODE_REVISION_CONFLICT, 'another device edited this item first');
          return;
        }
        found.meta = {
          ...found.meta,
          sealedMeta: request.sealedMeta,
          metaRev: (BigInt(found.meta.metaRev) + 1n).toString(),
        };
        reply(
          MessageType.MESSAGE_TYPE_STORE_UPDATE_META_RESPONSE,
          StoreUpdateMetaResponse.encode({ item: found.meta }).finish(),
        );
        this.broadcast(socket, MessageType.MESSAGE_TYPE_STORE_ITEM_UPDATED, StoreItemUpdated.encode({ item: found.meta }).finish());
        return;
      }

      case MessageType.MESSAGE_TYPE_STORE_DELETE_REQUEST: {
        const request = StoreDeleteRequest.decode(env.payload);
        const index = this.items.findIndex((it) => it.meta.itemId === request.itemId);
        if (index === -1) {
          fail(ErrorCode.ERROR_CODE_NOT_FOUND, 'no such item');
          return;
        }
        this.items.splice(index, 1);
        reply(MessageType.MESSAGE_TYPE_STORE_DELETE_RESPONSE, StoreDeleteResponse.encode({}).finish());
        this.broadcast(socket, MessageType.MESSAGE_TYPE_STORE_ITEM_DELETED, StoreItemDeleted.encode({ itemId: request.itemId }).finish());
        return;
      }

      default:
        fail(ErrorCode.ERROR_CODE_UNSUPPORTED_MESSAGE, 'unsupported message');
    }
  }

  /** storeRefusal is why a new item may not be saved, or null when it may. */
  private storeRefusal(
    config: StoreConfig,
    itemId: string,
    epoch: string,
    size: number,
  ): { code: ErrorCode; message: string } | null {
    if (epoch !== this.epoch.toString()) {
      return { code: ErrorCode.ERROR_CODE_EPOCH_CONFLICT, message: 'the item is not sealed at the group’s epoch' };
    }
    if (this.items.some((it) => it.meta.itemId === itemId)) {
      return { code: ErrorCode.ERROR_CODE_INVALID_ARGUMENT, message: 'item id already in use' };
    }
    if (!(size > 0) || size > config.maxItemBytes) {
      return { code: ErrorCode.ERROR_CODE_TOO_LARGE, message: 'the item is over the store’s item limit' };
    }
    if (this.items.length >= config.maxItems || this.storeUsedBytes() + size > config.maxBytes) {
      return { code: ErrorCode.ERROR_CODE_QUOTA_EXCEEDED, message: 'the store is full' };
    }
    return null;
  }

  /** storeUsedBytes counts the items' blobs and the store uploads still open, which hold their room. */
  private storeUsedBytes(): number {
    let used = 0;
    for (const it of this.items) {
      used += it.blob.length;
    }
    for (const upload of this.uploads.values()) {
      if (upload.kind === 'store') {
        used += upload.size;
      }
    }
    return used;
  }

  /** chunkBytes is the size every chunk but the last has. */
  private get chunkBytes(): number {
    const advertised = this.limits === null ? 0 : Number(this.limits.chunkBytes);
    return advertised > 0 ? advertised : SINGLE_FRAME_MAX_BYTES;
  }

  private beginUpload(socket: RelaySocket, kind: Upload['kind'], id: string, epoch: string, size: number): string {
    const handle = `upload-${this.nextId++}`;
    this.uploads.set(handle, {
      kind,
      deviceId: socket.deviceId,
      id,
      epoch,
      size,
      chunkBytes: this.chunkBytes,
      buf: new Uint8Array(size),
      received: 0,
    });
    return handle;
  }

  /** finishUpload takes a complete upload out of the table, or says why it cannot be committed. */
  private finishUpload(
    socket: RelaySocket,
    kind: Upload['kind'],
    handle: string,
    id: string,
    epoch: string,
    size: string,
  ): Uint8Array | string {
    const upload = this.uploads.get(handle);
    if (upload === undefined || upload.deviceId !== socket.deviceId || upload.kind !== kind) {
      return 'no such upload';
    }
    if (upload.id !== id || upload.epoch !== epoch || upload.size.toString() !== size) {
      return 'the commit does not match the upload it names';
    }
    if (upload.received !== upload.size) {
      return 'the upload is not complete';
    }
    this.uploads.delete(handle);
    return upload.buf;
  }

  /** range cuts [offset, offset+length) out of a blob; a length of zero or over a chunk is one chunk. */
  private range(blob: Uint8Array, offset: number, length: number): Uint8Array | null {
    if (!(offset >= 0 && offset < blob.length)) {
      return null;
    }
    const want = length === 0 || length > this.chunkBytes ? this.chunkBytes : length;
    return blob.slice(offset, Math.min(offset + want, blob.length));
  }

  /**
   * evictEntries removes the oldest entries while the history is over its
   * caps, and tells every device, as the real relay does at its storage limit
   * (SPEC §4.5). The newest entry always stays.
   */
  private evictEntries(): void {
    let bytes = this.entries.reduce((sum, e) => sum + e.ciphertext.length, 0);
    while (this.entries.length > 1 && (this.entries.length > this.maxEntries || bytes > this.maxEntryBytes)) {
      const gone = this.entries.shift() as StoredEntry;
      bytes -= gone.ciphertext.length;
      this.broadcast(null, MessageType.MESSAGE_TYPE_ENTRY_EVICTED, EntryEvicted.encode({ entryId: gone.meta.entryId }).finish());
    }
  }

  /** broadcast pushes an event to every connected device but `except`. */
  private broadcast(except: RelaySocket | null, type: MessageType, payload: Uint8Array): void {
    for (const live of this.sockets.values()) {
      if (live !== except) {
        live.push({ id: 'event', type, payload });
      }
    }
  }

  /** storeLinks are the store links rekeys sent, by the epoch each leads into. */
  readonly storeLinks = new Map<bigint, Uint8Array>();

  /** limits are what the relay answers EntryLimitsRequest with; null answers as a relay that predates it. */
  limits: EntryLimitsResponse | null;

  /** lastHeader is the sealed header of the newest entry, as the relay stores it. */
  lastHeader(): Uint8Array {
    return this.entries[this.entries.length - 1].meta.header;
  }

  /** groupName is what the admin has called the group. */
  groupName: string;

  /** deleted is set by deleteGroup: every device of the group is refused from then on. */
  deleted = false;

  /** rename is the admin renaming the group: connected devices are told at once. */
  rename(name: string): void {
    this.groupName = name;
    this.broadcast(null, MessageType.MESSAGE_TYPE_GROUP_RENAMED, GroupRenamed.encode({ name }).finish());
  }

  /**
   * deleteGroup is the admin deleting the group: live sockets close with the
   * group-deleted code, and the endpoint answers 410 for the devices it had.
   */
  deleteGroup(): void {
    this.deleted = true;
    for (const socket of this.sockets.values()) {
      socket.drop('group deleted', 4410);
    }
    this.sockets.clear();
    this.formerDevices = new Set(this.devices.keys());
    this.devices.clear();
  }

  /**
   * close drops every socket and forgets everything held: what ending the demo
   * does, so nothing it saved outlives it in memory.
   */
  close(): void {
    this.closed = true;
    for (const socket of this.sockets.values()) {
      socket.drop('the relay closed');
    }
    for (const socket of this.joiners.values()) {
      socket.drop('the relay closed');
    }
    this.sockets.clear();
    this.joiners.clear();
    this.devices.clear();
    this.entries = [];
    this.items = [];
    this.uploads.clear();
    this.pairings.clear();
    this.offers.clear();
    this.storeLinks.clear();
    this.onPairingStarted = null;
    this.onRevoked = null;
  }

  private formerDevices = new Set<string>();

  /** closed is set by close: the relay answers nothing after it. */
  private closed = false;

  /** deviceStatus is the status a plain GET of the endpoint would answer, for the client's deviceStatus option. */
  readonly deviceStatus = async (_base: string, deviceId: string): Promise<number | null> => {
    if (this.formerDevices.has(deviceId)) {
      return 410;
    }
    return this.devices.has(deviceId) ? 426 : 401;
  };

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
        createdAtUnixMs: this.now().toString(),
        lastSeenUnixMs: '0',
      },
    });
    return deviceId;
  }
}

/**
 * deviceIdOf reads the device_id query parameter. It is parsed by hand rather
 * than with `URL`, whose React Native shim is not the WHATWG class (see
 * src/core/protocol/urls.ts) — this file runs on the phone as well as in Node.
 */
function deviceIdOf(url: string): string {
  const match = /[?&]device_id=([^&#]*)/.exec(url);
  return match === null ? '' : decodeURIComponent(match[1]);
}

/** RelaySocket is the relay's end of one connection. */
class RelaySocket {
  joinerFor: string | null = null;

  private openHandler: () => void = () => {};
  private messageHandler: (frame: Uint8Array) => void = () => {};
  private closeHandler: (reason: string, code?: number) => void = () => {};
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
    this.closed = true;
    queueMicrotask(() => this.errorHandler(reason));
  }

  push(env: { id: string; type: MessageType; payload: Uint8Array }): void {
    if (this.closed) {
      return;
    }
    queueMicrotask(() => this.messageHandler(Envelope.encode(env).finish()));
  }

  drop(reason: string, code?: number): void {
    this.closed = true;
    queueMicrotask(() => this.closeHandler(reason, code));
  }
}
