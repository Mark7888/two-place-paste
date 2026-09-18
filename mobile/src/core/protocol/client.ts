/**
 * The protocol client: one method per flow of SPEC §3 and §6, mirroring the Go
 * client core (`pkg/tppclient`) so the two implementations stay legible
 * against each other.
 *
 * # Deliberate invariant
 *
 * Nothing here touches the clipboard. The screens and the quick-settings tile
 * read and write it; this module does not know a clipboard exists, which is
 * what structurally guarantees SPEC §3.3's rule that a rekey never touches it.
 *
 * # Connection lifetime
 *
 * Android connects only while the app is foregrounded (SPEC §5.2):
 * `connect()` dials and keeps re-dialling with backoff, `disconnect()` stops.
 * The quick-settings tile connects, syncs once and disconnects. Nothing here
 * runs a socket in the background.
 *
 * # Epochs
 *
 * A client holds exactly one (epoch, group key) pair (spec/crypto.md §7).
 * Entries below that epoch are skipped — never decrypted with a retained old
 * key, because forward secrecy across revocation is the whole point of the
 * rekey — and a wrapped key for a newer epoch, pushed during a rekey or
 * waiting at connect time, advances it.
 */

import {
  CreateGroupRequest,
  CreateGroupResponse,
  DeviceListRequest,
  DeviceListResponse,
  RekeyRequest,
  RekeyResponse,
  WrappedKey,
} from '../../protocol/gen/tpp/v1/group';
import {
  DeviceRevoked,
  EpochChanged,
  WrappedKeyAvailable,
} from '../../protocol/gen/tpp/v1/events';
import {
  EntryFetchRequest,
  EntryFetchResponse,
  EntryHistoryRequest,
  EntryHistoryResponse,
  EntryLatestRequest,
  EntryLatestResponse,
  EntryMeta as WireEntryMeta,
  EntryPutRequest,
  EntryPutResponse,
} from '../../protocol/gen/tpp/v1/entry';
import { Envelope, MessageType } from '../../protocol/gen/tpp/v1/envelope';
import {
  PairingComplete,
  PairingJoinNotice,
  PairingJoinRequest,
  PairingOfferAcceptRequest,
  PairingOfferRequest,
  PairingOfferResponse,
  PairingStartRequest,
  PairingStartResponse,
  PairingWrappedKeyUpload,
} from '../../protocol/gen/tpp/v1/pairing';
import { Error as ErrorFrame } from '../../protocol/gen/tpp/v1/error';
import {
  encodeFrame,
  decodeFrame,
  generateDeviceKey,
  generateGroupKey,
  generateNonce,
  newEntryID,
  openEntry,
  publicKey as derivePublicKey,
  sealEntry,
  unwrap,
  wrap,
} from '../crypto';
import { Connection, SocketFactory, webSocketFactory } from './connection';
import { ProtocolError, TppError } from './errors';
import {
  decodePairingOffer,
  decodePairingPayload,
  encodePairingOffer,
  encodePairingPayload,
  fingerprint,
} from './pairing';
import {
  ClientState,
  SecureStore,
  devicePublicKey,
  inGroup,
  loadState,
  newState,
  saveState,
} from './state';
import { splitCreationURL, normalizeServerURL, websocketURL } from './urls';

/** MAX_CIPHERTEXT_BYTES is the relay's per-entry cap, measured on the ciphertext (SPEC §4.3). */
export const MAX_CIPHERTEXT_BYTES = 10 << 20;

/** Reconnect bounds. A relay that is down comes back; a phone that hammers it while it does is a second problem. */
const MIN_BACKOFF_MS = 500;
const MAX_BACKOFF_MS = 30_000;

/** PAIRING_WRAP_TIMEOUT_MS bounds a joiner's wait for the inviter to hand over the group key. */
export const PAIRING_WRAP_TIMEOUT_MS = 120_000;

/**
 * OFFER_WAIT_TIMEOUT_MS bounds a device's wait for a member to accept the code
 * it is showing. It matches the offer's own lifetime, because the wait is over
 * either way once the relay stops holding it (SPEC §3.2: five minutes).
 */
export const OFFER_WAIT_TIMEOUT_MS = 5 * 60_000;

/** Device is one member of the group, as the relay reports it — what a revocation dialog renders, by name. */
export interface Device {
  id: string;
  name: string;
  publicKey: Uint8Array;
  createdAt: Date;
  /** lastSeen is null when the device has never connected. */
  lastSeen: Date | null;
  /** self is true for the device this client runs on. */
  self: boolean;
}

/** Roster is the group's device list at one epoch. */
export interface Roster {
  devices: Device[];
  epoch: bigint;
}

/** EntryMeta is everything the relay knows about an entry: no content type, no filename, no plaintext size. */
export interface EntryMeta {
  id: string;
  epoch: bigint;
  size: number;
  createdAt: Date;
  expiresAt: Date;
  inline: boolean;
}

/** Item is one clipboard payload in plaintext. Nothing here reaches the relay in the clear. */
export interface Item {
  contentType: string;
  filename: string;
  body: Uint8Array;
  createdAt: Date;
  meta: EntryMeta | null;
}

/** HistoryPage is a page of entry metadata, newest first (SPEC §6). */
export interface HistoryPage {
  entries: EntryMeta[];
  /** nextBefore pages backwards; null when the listing reached the end. */
  nextBefore: Date | null;
}

/** Handlers are the callbacks a screen or the tile hooks into. None of them may touch the clipboard. */
export interface Handlers {
  onConnected?: () => void;
  onDisconnected?: (reason: string) => void;
  /** onEpoch fires when this client installs a new group key, from a rekey it performed or one it was handed. */
  onEpoch?: (epoch: bigint) => void;
  /** onDeviceRevoked fires when the group loses a device (SPEC §3.3). A UI refreshes its roster. */
  onDeviceRevoked?: (deviceId: string, epoch: bigint) => void;
  /** onDevicePaired fires on the inviting device once a joiner has been handed the group key. */
  onDevicePaired?: (device: Device) => void;
  /**
   * onProbablyRevoked fires when the relay is reachable but refuses this
   * device's socket: the likeliest cause is that it was revoked while away
   * (SPEC §3.3 step 5). See `probeRefusal` for why this is inferred rather
   * than read from a status code.
   */
  onProbablyRevoked?: () => void;
}

/** ClientOptions configures a Client. */
export interface ClientOptions {
  /** store persists state across launches: on Android, Keystore-backed encrypted storage. */
  store: SecureStore;
  /** deviceName is what the revocation dialog on other devices shows. Not a secret, and not clipboard content. */
  deviceName: string;
  /** socketFactory dials the relay. Defaults to the platform's global WebSocket. */
  socketFactory?: SocketFactory;
  /** now supplies UTC timestamps; tests replace it. */
  now?: () => Date;
  /** handlers are optional callbacks. */
  handlers?: Handlers;
  /** healthCheck reports whether the relay answers /healthz, used to tell "revoked" from "offline". */
  healthCheck?: (baseUrl: string) => Promise<boolean>;
}

/** Invitation is a pairing in progress on the inviting device (SPEC §3.2 step 1). */
export interface Invitation {
  /** payload is what the user transports out of band: rendered as a QR code, or copied as text. The two are one string. */
  payload: string;
  token: string;
  expiresAt: Date;
  /** joined resolves when a device joins with this invitation. */
  joined: Promise<Device>;
}

/**
 * Offer is a pairing offer this device is showing while it waits for a member
 * of some group to accept it (docs/plans/joiner-emitted-pairing.md).
 *
 * `code` is what the user transports out of band: rendered as a QR code, or
 * copied as text. The two are one string, exactly as for an invitation.
 *
 * The socket that minted it stays open for as long as the offer is live:
 * PairingComplete is pushed to it when a member accepts, and the relay has no
 * other way to reach a device with no identity. `accepted` consumes that;
 * `cancel` gives it up.
 */
export interface Offer {
  code: string;
  offerCode: string;
  expiresAt: Date;
  /** accepted resolves once a member has accepted and this device is in the group. */
  accepted: Promise<void>;
  /** cancel withdraws an offer the user is no longer showing. */
  cancel(): void;
}

/**
 * OfferAcceptance is a scanned or pasted offer waiting for the user's consent.
 * It is the shape of Revocation below, split for a sharper reason.
 *
 * A hostile invitation costs a joiner nothing: it holds no key to lose. A
 * hostile offer is accepted by a *member*, who hands over the group key. So
 * nothing is wrapped and no device is admitted until `confirm` is called, and a
 * screen that has not rendered `deviceName` and `fingerprint` has nothing to
 * confirm with.
 */
export interface OfferAcceptance {
  /** deviceName is what the offering device calls itself. Display text from a device that is not in the group yet. */
  deviceName: string;
  /** fingerprint is the offered public key rendered for a person to compare with what that device shows. */
  fingerprint: string;
  /** publicKey is the key the group key will be wrapped to, read out of band rather than from the relay. */
  publicKey: Uint8Array;
  confirm(): Promise<Device>;
}

/**
 * Revocation is a prepared revocation: the roster the user must be shown, and
 * the one method that carries it out.
 *
 * This split is the client's half of SPEC §3.3 step 2. `prepareRevoke`
 * gathers what a confirmation dialog needs and stops; nothing is revoked and
 * no key is generated until `confirm` is called, and a UI that has not
 * rendered `roster` has nothing to confirm with.
 */
export interface Revocation {
  roster: Roster;
  target: Device;
  /** remaining is every device that keeps access, including this one. Each gets its own copy of the new group key. */
  remaining: Device[];
  confirm(): Promise<Roster>;
}

const asBigInt = (s: string): bigint => BigInt(s === '' ? '0' : s);
const msToDate = (ms: string): Date | null => (ms === '' || ms === '0' ? null : new Date(Number(ms)));

function toDevice(
  d: { deviceId: string; name: string; publicKey: Uint8Array; createdAtUnixMs: string; lastSeenUnixMs: string },
  selfId: string,
): Device {
  return {
    id: d.deviceId,
    name: d.name,
    publicKey: d.publicKey,
    createdAt: msToDate(d.createdAtUnixMs) ?? new Date(0),
    lastSeen: msToDate(d.lastSeenUnixMs),
    self: d.deviceId === selfId,
  };
}

function toEntryMeta(m: WireEntryMeta): EntryMeta {
  return {
    id: m.entryId,
    epoch: asBigInt(m.epoch),
    size: Number(m.size),
    createdAt: msToDate(m.createdAtUnixMs) ?? new Date(0),
    expiresAt: msToDate(m.expiresAtUnixMs) ?? new Date(0),
    inline: m.inline,
  };
}

/** Client is the protocol client. One instance per app. */
export class Client {
  private state: ClientState;
  private readonly store: SecureStore;
  private readonly socketFactory: SocketFactory;
  private readonly now: () => Date;
  private readonly handlers: Handlers;
  private readonly healthCheck: (baseUrl: string) => Promise<boolean>;

  private conn: Connection | null = null;
  private dialling: Promise<Connection> | null = null;
  private wanted = false;
  private backoffMs = MIN_BACKOFF_MS;
  private retryTimer: ReturnType<typeof setTimeout> | null = null;
  private refusals = 0;
  private invitations = new Map<string, (device: Device) => void>();

  private constructor(state: ClientState, options: ClientOptions) {
    this.state = state;
    this.store = options.store;
    this.socketFactory = options.socketFactory ?? webSocketFactory;
    this.now = options.now ?? (() => new Date());
    this.handlers = options.handlers ?? {};
    this.healthCheck = options.healthCheck ?? defaultHealthCheck;
  }

  /**
   * open loads this installation's state, generating a device keypair on first
   * launch (spec/crypto.md §3).
   */
  static async open(options: ClientOptions): Promise<Client> {
    const state = await loadState(options.store, options.deviceName);
    if (state.deviceName !== options.deviceName && options.deviceName !== '') {
      state.deviceName = options.deviceName;
      await saveState(options.store, state);
    }
    return new Client(state, options);
  }

  /** snapshot returns the current state. It carries both secrets: for persistence and tests, never for display or a log. */
  snapshot(): ClientState {
    return { ...this.state };
  }

  /** epoch is the group key generation this client currently holds. */
  get epoch(): bigint {
    return this.state.epoch;
  }

  /** inGroup reports whether this device belongs to a group yet. */
  get inGroup(): boolean {
    return inGroup(this.state);
  }

  /** connected reports whether a socket is live right now. */
  get connected(): boolean {
    return this.conn !== null && !this.conn.isClosed;
  }

  /** serverUrl is the relay this device is paired with, or "". */
  get serverUrl(): string {
    return this.state.serverUrl;
  }

  /** deviceId is this device's identifier on the relay, or "". */
  get deviceId(): string {
    return this.state.deviceId;
  }

  /**
   * publicKey is this device's X25519 public key.
   *
   * It is public, and it is here for one reason: a device showing a pairing
   * offer has to render its own fingerprint, so the user can check it against
   * the one the accepting device is about to show them. Nothing secret is
   * reachable through this.
   */
  get publicKey(): Uint8Array {
    return devicePublicKey(this.state);
  }

  // -------------------------------------------------------------------------
  // Connection
  // -------------------------------------------------------------------------

  /**
   * connect brings the client online and keeps it there until `disconnect`:
   * the app calls it when it comes to the foreground, and the tile calls it
   * around one sync (SPEC §5.2).
   */
  async connect(): Promise<void> {
    if (!this.inGroup) {
      throw new TppError('no-group', 'this device is not in a group');
    }
    this.wanted = true;
    await this.connection();
  }

  /** disconnect drops the socket and stops re-dialling. Called when the app goes to the background. */
  disconnect(): void {
    this.wanted = false;
    if (this.retryTimer !== null) {
      clearTimeout(this.retryTimer);
      this.retryTimer = null;
    }
    const conn = this.conn;
    this.conn = null;
    this.dialling = null;
    conn?.close('the app is no longer in the foreground');
  }

  /** connection returns a live connection, dialling if there is none. */
  private async connection(): Promise<Connection> {
    if (this.conn !== null && !this.conn.isClosed) {
      return this.conn;
    }
    if (this.dialling !== null) {
      return this.dialling;
    }
    this.dialling = this.dial();
    try {
      return await this.dialling;
    } finally {
      this.dialling = null;
    }
  }

  private async dial(): Promise<Connection> {
    const url = websocketURL(this.state.serverUrl, this.state.deviceId);
    let conn: Connection;
    try {
      conn = await Connection.connect(this.socketFactory, url);
    } catch (err) {
      await this.probeRefusal();
      throw err;
    }
    this.refusals = 0;
    this.backoffMs = MIN_BACKOFF_MS;
    this.conn = conn;
    conn.events((env) => {
      void this.onEvent(env);
    });
    conn.closed((reason) => {
      if (this.conn === conn) {
        this.conn = null;
      }
      this.handlers.onDisconnected?.(reason);
      this.scheduleRedial();
    });
    this.handlers.onConnected?.();
    return conn;
  }

  /**
   * probeRefusal decides whether a failed dial means "revoked" or "offline".
   *
   * The Go client reads the 401 the relay answers a revoked device's upgrade
   * with. The WebSocket API a browser and React Native expose does not surface
   * the response status at all — a refused upgrade and an unreachable host are
   * the same `onerror` — so this asks a question the API does answer: is the
   * relay itself up? A relay that serves /healthz while refusing this device's
   * socket twice running is almost certainly a relay that no longer has this
   * device's record. It is reported as "probably revoked" and the UI says so
   * in those words; nothing is deleted on the strength of a guess.
   */
  private async probeRefusal(): Promise<void> {
    this.refusals++;
    if (this.refusals < 2) {
      return;
    }
    let healthy = false;
    try {
      healthy = await this.healthCheck(this.state.serverUrl);
    } catch {
      healthy = false;
    }
    if (healthy) {
      this.handlers.onProbablyRevoked?.();
    }
  }

  private scheduleRedial(): void {
    if (!this.wanted || this.retryTimer !== null) {
      return;
    }
    const delay = jitter(this.backoffMs);
    this.backoffMs = Math.min(this.backoffMs * 2, MAX_BACKOFF_MS);
    this.retryTimer = setTimeout(() => {
      this.retryTimer = null;
      if (!this.wanted) {
        return;
      }
      void this.connection().catch(() => {
        // The failure is already reported through onDisconnected; the next
        // attempt is scheduled by this same path.
        this.scheduleRedial();
      });
    }, delay);
  }

  // -------------------------------------------------------------------------
  // Events the relay pushes
  // -------------------------------------------------------------------------

  private async onEvent(env: Envelope): Promise<void> {
    switch (env.type) {
      case MessageType.MESSAGE_TYPE_WRAPPED_KEY_AVAILABLE: {
        // A key that was waiting for this device arrives here on connect, so a
        // device that was offline during a rekey catches up without asking.
        const event = WrappedKeyAvailable.decode(env.payload);
        await this.installWrappedKey(asBigInt(event.epoch), event.wrappedGroupKey);
        break;
      }
      case MessageType.MESSAGE_TYPE_EPOCH_CHANGED: {
        // A notification only: the wrapped key is a separate frame, and this
        // must never touch the local clipboard (spec/crypto.md §7).
        EpochChanged.decode(env.payload);
        break;
      }
      case MessageType.MESSAGE_TYPE_DEVICE_REVOKED: {
        const event = DeviceRevoked.decode(env.payload);
        this.handlers.onDeviceRevoked?.(event.deviceId, asBigInt(event.epoch));
        break;
      }
      case MessageType.MESSAGE_TYPE_PAIRING_JOIN_NOTICE: {
        // The inviter is the only device holding the group key, so wrapping for
        // the joiner happens here and cannot be deferred (SPEC §3.2 step 4).
        await this.completePairing(PairingJoinNotice.decode(env.payload));
        break;
      }
      default:
        // An event this client does not understand is ignored, never fatal.
        break;
    }
  }

  /**
   * installWrappedKey unwraps a delivered group key and advances the epoch.
   *
   * A key for an epoch this client already has, or an older one, is dropped:
   * epochs only move forward, and keeping an older key would silently defeat
   * the forward secrecy the rekey exists for.
   */
  private async installWrappedKey(epoch: bigint, wrapped: Uint8Array): Promise<void> {
    if (epoch <= this.state.epoch) {
      return;
    }
    const groupKey = unwrap(wrapped, this.state.devicePrivateKey, epoch);
    await this.setGroupKey(epoch, groupKey);
  }

  private async setGroupKey(epoch: bigint, groupKey: Uint8Array): Promise<void> {
    this.state = { ...this.state, epoch, groupKey };
    await saveState(this.store, this.state);
    this.handlers.onEpoch?.(epoch);
  }

  // -------------------------------------------------------------------------
  // Group creation and pairing (SPEC §3.1, §3.2)
  // -------------------------------------------------------------------------

  /**
   * createGroup turns a creation URL — https://<host>/<token>, the string
   * behind the QR code on the admin screen — into a group with this device as
   * its first member (SPEC §3.1 steps 4-5).
   *
   * The device generates the group key locally at epoch 1 and wraps it to
   * itself; the relay only ever receives the wrapped form.
   */
  async createGroup(creationURL: string): Promise<void> {
    const { base, token } = splitCreationURL(creationURL);
    const publicKey = devicePublicKey(this.state);
    const groupKey = generateGroupKey();
    // Epoch 1 is stated by the wire contract rather than assumed, but the wrap
    // has to be built before the response exists, so this wraps at 1 and
    // checks the answer below.
    const wrappedGroupKey = wrap(groupKey, publicKey, 1n);

    // Group creation is one of the two frames an unauthenticated connection
    // may send: it is how this device acquires an identity. The socket is
    // dropped afterwards and the client dials again with the credential.
    const conn = await Connection.connect(this.socketFactory, websocketURL(base, ''));
    let response: CreateGroupResponse;
    try {
      response = await conn.call(
        MessageType.MESSAGE_TYPE_CREATE_GROUP_REQUEST,
        {
          codec: CreateGroupRequest,
          message: {
            token,
            devicePublicKey: publicKey,
            wrappedGroupKey,
            deviceName: this.state.deviceName,
          },
        },
        CreateGroupResponse,
      );
    } finally {
      conn.close('group created');
    }
    if (asBigInt(response.epoch) !== 1n) {
      throw new TppError(
        'invalid',
        `the relay created the group at epoch ${response.epoch}, want 1`,
      );
    }

    this.state = {
      ...this.state,
      serverUrl: base,
      groupId: response.groupId,
      deviceId: response.deviceId,
      epoch: 1n,
      groupKey,
    };
    await saveState(this.store, this.state);
    await this.connect();
  }

  /**
   * startPairing mints a pairing token and returns the payload to show
   * (SPEC §3.2 step 1).
   *
   * The payload carries a fresh ephemeral public key generated for this
   * attempt. The relay does not consume it and this client derives nothing
   * from it — the wrap of step 4 uses its own ephemeral key, with a lifetime of
   * one 81-byte container — so it is carried, not trusted.
   */
  async startPairing(): Promise<Invitation> {
    this.requireGroup();
    const conn = await this.connection();
    const response = await conn.call(
      MessageType.MESSAGE_TYPE_PAIRING_START_REQUEST,
      { codec: PairingStartRequest, message: {} },
      PairingStartResponse,
    );

    // The pairing ephemeral keypair and the wrap ephemeral keypair of
    // spec/crypto.md §4.2 are different keys with different lifetimes. This one
    // is generated per pairing attempt and discarded when it ends.
    const ephemeralPublic = derivePublicKey(generateDeviceKey());
    const payload = encodePairingPayload({
      serverUrl: this.state.serverUrl,
      pairingToken: response.pairingToken,
      inviterEphemeralPublicKey: ephemeralPublic,
    });

    let resolveJoined: (device: Device) => void = () => {};
    const joined = new Promise<Device>((resolve) => {
      resolveJoined = resolve;
    });
    this.invitations.set(response.pairingToken, resolveJoined);

    return {
      payload,
      token: response.pairingToken,
      expiresAt: msToDate(response.expiresAtUnixMs) ?? this.now(),
      joined,
    };
  }

  /** completePairing runs on the inviter when the relay reports a joiner (SPEC §3.2 step 4). */
  private async completePairing(notice: PairingJoinNotice): Promise<void> {
    if (!this.inGroup) {
      return;
    }
    const conn = await this.connection();
    const wrapped = wrap(this.state.groupKey, notice.devicePublicKey, this.state.epoch);
    await conn.call(
      MessageType.MESSAGE_TYPE_PAIRING_WRAPPED_KEY_UPLOAD,
      {
        codec: PairingWrappedKeyUpload,
        message: {
          pairingToken: notice.pairingToken,
          deviceId: notice.deviceId,
          wrappedGroupKey: wrapped,
        },
      },
      PairingComplete,
    );

    const device: Device = {
      id: notice.deviceId,
      name: notice.deviceName,
      publicKey: notice.devicePublicKey,
      createdAt: this.now(),
      lastSeen: null,
      self: false,
    };
    this.invitations.get(notice.pairingToken)?.(device);
    this.invitations.delete(notice.pairingToken);
    this.handlers.onDevicePaired?.(device);
  }

  /**
   * joinPairing joins the group described by a pairing payload — scanned from
   * a QR code or pasted as text (SPEC §3.2 steps 2, 5).
   *
   * The joining device generates nothing but its own identity, presents the
   * public half and waits for the inviter to hand back the group key. It
   * starts empty: it cannot decrypt anything created before it joined and does
   * not ask for that history.
   */
  async joinPairing(payloadText: string): Promise<void> {
    const payload = decodePairingPayload(payloadText);
    const base = normalizeServerURL(payload.serverUrl);
    const publicKey = devicePublicKey(this.state);

    // Pairing-join is the second frame an unauthenticated connection may send.
    // The socket must stay open afterwards: PairingComplete is pushed to it
    // once the inviter has wrapped the key.
    const conn = await Connection.connect(this.socketFactory, websocketURL(base, ''));
    let complete: PairingComplete;
    try {
      // Registered before the request goes out: the inviter can be fast enough
      // that PairingComplete arrives while the join is still in flight.
      const completed = conn.expect(
        MessageType.MESSAGE_TYPE_PAIRING_COMPLETE,
        PAIRING_WRAP_TIMEOUT_MS,
      );
      const failed = conn.expect(MessageType.MESSAGE_TYPE_ERROR, PAIRING_WRAP_TIMEOUT_MS);
      conn.send(MessageType.MESSAGE_TYPE_PAIRING_JOIN_REQUEST, PairingJoinRequest, {
        pairingToken: payload.pairingToken,
        deviceName: this.state.deviceName,
        devicePublicKey: publicKey,
      });

      const env = await Promise.race([completed, failed]);
      if (env.type === MessageType.MESSAGE_TYPE_ERROR) {
        const frame = ErrorFrame.decode(env.payload);
        throw new ProtocolError(frame.code, frame.message);
      }
      complete = PairingComplete.decode(env.payload);
    } finally {
      conn.close('pairing complete');
    }

    await this.installPairingComplete(base, complete);
  }

  /**
   * startOffer asks the relay to hold an offer for this device and returns the
   * code to show (docs/plans/joiner-emitted-pairing.md §3 steps 1-3).
   *
   * This is the direction the flows above cannot express: a device with no
   * group key cannot mint a pairing token, because that takes an authenticated
   * connection, so the relay holds an offer for it instead and a member accepts
   * it. Which way round the code travels is a question of which screen the user
   * is looking at, not of the protocol.
   *
   * `serverUrl` is the one real cost of this direction: a device with no group
   * has no relay URL either, so the user supplies it once. An empty value falls
   * back to the one this client already knows, if any.
   *
   * The returned offer holds a socket open. Await `accepted`, or call `cancel`
   * to give it up.
   */
  async startOffer(serverUrl: string): Promise<Offer> {
    if (this.inGroup) {
      // A device holding a group key cannot offer itself to another group
      // without discarding the key it has, and discarding it is a separate,
      // deliberate act — `forget`, from the Settings screen.
      throw new TppError('invalid', 'this device is already in a group');
    }
    const base = normalizeServerURL(serverUrl.trim() === '' ? this.state.serverUrl : serverUrl);
    const publicKey = devicePublicKey(this.state);

    // Offer minting is the third frame an unauthenticated connection may send.
    // The socket must stay open afterwards: PairingComplete is pushed to it
    // once a member accepts.
    const conn = await Connection.connect(this.socketFactory, websocketURL(base, ''));

    // Registered before the request goes out: a member watching the screen can
    // accept fast enough that the completion arrives while the mint is still in
    // flight. Nothing awaits these until the mint has succeeded, so a rejection
    // is claimed below rather than left to crash the app.
    const completed = conn.expect(MessageType.MESSAGE_TYPE_PAIRING_COMPLETE, OFFER_WAIT_TIMEOUT_MS);
    const failed = conn.expect(MessageType.MESSAGE_TYPE_ERROR, OFFER_WAIT_TIMEOUT_MS);
    completed.catch(() => undefined);
    failed.catch(() => undefined);

    let response: PairingOfferResponse;
    try {
      response = await conn.call(
        MessageType.MESSAGE_TYPE_PAIRING_OFFER_REQUEST,
        {
          codec: PairingOfferRequest,
          message: { deviceName: this.state.deviceName, devicePublicKey: publicKey },
        },
        PairingOfferResponse,
      );
    } catch (err) {
      conn.close('the pairing offer was refused');
      throw err;
    }

    const code = encodePairingOffer({
      serverUrl: base,
      offerCode: response.offerCode,
      devicePublicKey: publicKey,
      deviceName: this.state.deviceName,
    });

    const accepted = (async (): Promise<void> => {
      try {
        const env = await Promise.race([completed, failed]);
        if (env.type === MessageType.MESSAGE_TYPE_ERROR) {
          const frame = ErrorFrame.decode(env.payload);
          throw new ProtocolError(frame.code, frame.message);
        }
        await this.installPairingComplete(base, PairingComplete.decode(env.payload));
      } finally {
        conn.close('pairing offer answered');
      }
    })();
    // Nothing is lost if the screen never awaits this — a withdrawn offer
    // rejects — but an unhandled rejection would crash the app.
    accepted.catch(() => undefined);

    return {
      code,
      offerCode: response.offerCode,
      expiresAt: msToDate(response.expiresAtUnixMs) ?? this.now(),
      accepted,
      cancel: () => conn.close('the pairing offer was withdrawn'),
    };
  }

  /**
   * prepareAcceptOffer decodes an offer and returns what the confirmation
   * dialog must show (docs/plans/joiner-emitted-pairing.md §5). It contacts
   * nothing and changes nothing.
   *
   * The split is the client's half of the rule, not the screen's: `confirm` is
   * the only thing that wraps a key, so a screen cannot admit a device without
   * having had something to render.
   */
  async prepareAcceptOffer(codeText: string): Promise<OfferAcceptance> {
    this.requireGroup();
    const offer = decodePairingOffer(codeText);
    const base = normalizeServerURL(offer.serverUrl);
    if (base !== this.state.serverUrl) {
      // The accept travels over this client's own connection, so an offer held
      // by another relay could not be completed anyway. Saying so is better
      // than a not-found from a relay that never saw the code.
      throw new TppError(
        'invalid',
        `that code is held by ${base}, and this device is paired with ${this.state.serverUrl}`,
      );
    }

    let done = false;
    const confirm = async (): Promise<Device> => {
      if (done) {
        throw new TppError('invalid', 'that code has already been accepted');
      }
      this.requireGroup();
      // The wrap targets the key the user just confirmed, never one the relay
      // supplied — and the relay refuses an accept whose key is not the one the
      // offer was minted with, so a substituted key cannot complete the pairing
      // either.
      const wrapped = wrap(this.state.groupKey, offer.devicePublicKey, this.state.epoch);
      const conn = await this.connection();
      const complete = await conn.call(
        MessageType.MESSAGE_TYPE_PAIRING_OFFER_ACCEPT_REQUEST,
        {
          codec: PairingOfferAcceptRequest,
          message: {
            offerCode: offer.offerCode,
            devicePublicKey: offer.devicePublicKey,
            wrappedGroupKey: wrapped,
          },
        },
        PairingComplete,
      );
      done = true;

      const device: Device = {
        id: complete.deviceId,
        name: offer.deviceName,
        publicKey: offer.devicePublicKey,
        createdAt: this.now(),
        lastSeen: null,
        self: false,
      };
      this.handlers.onDevicePaired?.(device);
      return device;
    };

    return {
      deviceName: offer.deviceName,
      fingerprint: fingerprint(offer.devicePublicKey),
      publicKey: offer.devicePublicKey,
      confirm,
    };
  }

  /**
   * installPairingComplete is the last step of both pairing directions: unwrap
   * the group key, install `(epoch, group key)` and come online. It is one
   * function because the two directions differ only in who showed the code.
   */
  private async installPairingComplete(base: string, complete: PairingComplete): Promise<void> {
    const epoch = asBigInt(complete.epoch);
    const groupKey = unwrap(complete.wrappedGroupKey, this.state.devicePrivateKey, epoch);
    this.state = {
      ...this.state,
      serverUrl: base,
      groupId: complete.groupId,
      deviceId: complete.deviceId,
      epoch,
      groupKey,
    };
    await saveState(this.store, this.state);
    await this.connect();
  }

  /**
   * forget deletes this installation's identity and returns the client to the
   * state of a first launch: no group, and a brand-new device keypair.
   *
   * It is a local operation only. The relay still lists this device until
   * another device revokes it, and only that re-keys the group — which is why
   * the screen offering this says so. What it does guarantee is that the group
   * key is gone from this phone, so nothing here can read the group's entries
   * afterwards.
   *
   * A fresh keypair rather than the old one, because this is a new identity:
   * pairing again should look like a new device to the group, not like the
   * device that left.
   */
  async forget(): Promise<void> {
    this.disconnect();
    this.invitations.clear();
    this.refusals = 0;
    this.state = newState(this.state.deviceName);
    await saveState(this.store, this.state);
  }

  // -------------------------------------------------------------------------
  // Devices and revocation (SPEC §3.3)
  // -------------------------------------------------------------------------

  /**
   * devices returns the group roster and its epoch (SPEC §3.3 step 1). A UI
   * must render this — by name — before it offers to revoke anything.
   */
  async devices(): Promise<Roster> {
    this.requireGroup();
    const conn = await this.connection();
    const response = await conn.call(
      MessageType.MESSAGE_TYPE_DEVICE_LIST_REQUEST,
      { codec: DeviceListRequest, message: {} },
      DeviceListResponse,
    );
    return {
      epoch: asBigInt(response.epoch),
      devices: response.devices.map((d) => toDevice(d, this.state.deviceId)),
    };
  }

  /**
   * prepareRevoke gathers what the confirmation dialog of SPEC §3.3 step 2
   * needs and stops. It changes nothing on the relay.
   */
  async prepareRevoke(deviceId: string): Promise<Revocation> {
    this.requireGroup();
    if (deviceId === this.state.deviceId) {
      // The relay refuses this too. A device revoking itself would leave the
      // group with no way to hand the new key to anyone.
      throw new TppError('invalid', 'a device may not revoke itself');
    }
    const roster = await this.devices();
    const target = roster.devices.find((d) => d.id === deviceId);
    if (!target) {
      throw new TppError('invalid', `device ${deviceId} is not in this group`);
    }
    const remaining = roster.devices.filter((d) => d.id !== deviceId);

    let done = false;
    const confirm = async (): Promise<Roster> => {
      if (done) {
        throw new TppError('invalid', 'this revocation has already been carried out');
      }
      // A brand-new group key at epoch+1 — never a ratchet from the old one,
      // because revocation must be a hard break — wrapped once per remaining
      // device with a fresh ephemeral key each time, and sent in one frame the
      // relay applies atomically.
      const newKey = generateGroupKey();
      const epoch = roster.epoch + 1n;
      const wrappedKeys: WrappedKey[] = remaining.map((d) => ({
        deviceId: d.id,
        wrappedGroupKey: wrap(newKey, d.publicKey, epoch),
      }));

      const conn = await this.connection();
      const response = await conn.call(
        MessageType.MESSAGE_TYPE_REKEY_REQUEST,
        {
          codec: RekeyRequest,
          message: {
            revokedDeviceId: target.id,
            expectedEpoch: roster.epoch.toString(),
            wrappedKeys,
          },
        },
        RekeyResponse,
      );
      if (asBigInt(response.epoch) !== epoch) {
        throw new TppError(
          'invalid',
          `the relay reports epoch ${response.epoch} after the rekey, expected ${epoch}`,
        );
      }
      done = true;
      // The revoking device installs its own new key directly rather than
      // waiting for the copy the relay pushes back to it.
      await this.setGroupKey(epoch, newKey);
      return { epoch, devices: remaining };
    };

    return { roster, target, remaining, confirm };
  }

  // -------------------------------------------------------------------------
  // Entries (SPEC §6)
  // -------------------------------------------------------------------------

  /** putEntry encrypts an item and stores it as the group's latest entry. Last write to reach the relay wins. */
  async putEntry(item: {
    contentType: string;
    filename?: string;
    body: Uint8Array;
    createdAt?: Date;
  }): Promise<EntryMeta> {
    this.requireGroup();
    if (item.contentType === '') {
      throw new TppError('invalid', 'an entry needs a content type');
    }
    const createdAt = item.createdAt ?? this.now();
    const frame = encodeFrame({
      contentType: item.contentType,
      filename: item.filename ?? '',
      createdAt: BigInt(createdAt.getTime()),
      body: item.body,
    });
    const entryId = newEntryID();
    const epoch = this.state.epoch;
    const container = sealEntry(this.state.groupKey, generateNonce(), epoch, entryId, frame);
    if (container.length > MAX_CIPHERTEXT_BYTES) {
      // Refused here rather than by the relay: the cap is on the ciphertext,
      // and the caller deserves the answer before a 10 MB upload.
      throw new TppError(
        'invalid',
        `ciphertext is ${container.length} bytes, the cap is ${MAX_CIPHERTEXT_BYTES}`,
      );
    }

    const conn = await this.connection();
    const response = await conn.call(
      MessageType.MESSAGE_TYPE_ENTRY_PUT_REQUEST,
      {
        codec: EntryPutRequest,
        message: {
          entryId,
          epoch: epoch.toString(),
          size: container.length.toString(),
          ciphertext: container,
        },
      },
      EntryPutResponse,
    );
    const meta = response.meta ? toEntryMeta(response.meta) : null;
    if (meta === null || meta.id !== entryId) {
      // The relay filed the entry under an id this client did not bind, so
      // nothing — this client included — can decrypt it.
      throw new TppError(
        'invalid',
        'the relay stored the entry under a different id than the one bound into its ciphertext',
      );
    }
    return meta;
  }

  /**
   * getLatest returns the group's most recent entry, decrypted (SPEC §6).
   *
   * Epoch handling follows spec/crypto.md §7: same epoch decrypts; an older
   * one is 'stale-entry', which a caller shows as "nothing to sync" rather
   * than as a failure; a newer one is 'epoch-ahead', meaning this client is
   * behind a rekey and should retry once its wrapped key arrives. An empty
   * group is 'no-entry', which is what a freshly paired device sees.
   */
  async getLatest(): Promise<Item> {
    this.requireGroup();
    const conn = await this.connection();
    const response = await conn.call(
      MessageType.MESSAGE_TYPE_ENTRY_LATEST_REQUEST,
      { codec: EntryLatestRequest, message: {} },
      EntryLatestResponse,
    );
    if (!response.meta) {
      throw new TppError('no-entry', 'the group has no entry');
    }
    let meta = toEntryMeta(response.meta);
    let ciphertext = response.ciphertext;
    if (!meta.inline) {
      // A large entry lives in the blob backend and is pulled by id.
      const fetched = await this.fetch(meta.id);
      meta = fetched.meta;
      ciphertext = fetched.ciphertext;
    }
    return this.open(meta, ciphertext);
  }

  /**
   * getHistory lists entry metadata, newest first (SPEC §6).
   *
   * History is only ever pulled, and only when the user presses fetch: nothing
   * in this client lists it on reconnect. A client that did would be making
   * the user's clipboard history travel without being asked.
   */
  async getHistory(query: { limit?: number; before?: Date } = {}): Promise<HistoryPage> {
    this.requireGroup();
    const conn = await this.connection();
    const response = await conn.call(
      MessageType.MESSAGE_TYPE_ENTRY_HISTORY_REQUEST,
      {
        codec: EntryHistoryRequest,
        message: {
          limit: query.limit ?? 0,
          beforeUnixMs: query.before ? query.before.getTime().toString() : '0',
        },
      },
      EntryHistoryResponse,
    );
    return {
      entries: response.entries.map(toEntryMeta),
      nextBefore: msToDate(response.nextBeforeUnixMs),
    };
  }

  /** getEntry fetches one entry by id and decrypts it — what the history tab does when the user taps an entry. */
  async getEntry(entryId: string): Promise<Item> {
    this.requireGroup();
    const { meta, ciphertext } = await this.fetch(entryId);
    return this.open(meta, ciphertext);
  }

  private async fetch(entryId: string): Promise<{ meta: EntryMeta; ciphertext: Uint8Array }> {
    const conn = await this.connection();
    const response = await conn.call(
      MessageType.MESSAGE_TYPE_ENTRY_FETCH_REQUEST,
      { codec: EntryFetchRequest, message: { entryId } },
      EntryFetchResponse,
    );
    if (!response.meta) {
      throw new TppError('no-entry', `the relay returned no metadata for entry ${entryId}`);
    }
    return { meta: toEntryMeta(response.meta), ciphertext: response.ciphertext };
  }

  /** open applies the epoch rules and decrypts. */
  private open(meta: EntryMeta, ciphertext: Uint8Array): Item {
    if (meta.epoch < this.state.epoch) {
      throw new TppError(
        'stale-entry',
        `entry is at epoch ${meta.epoch}, this device is at ${this.state.epoch}`,
      );
    }
    if (meta.epoch > this.state.epoch) {
      throw new TppError(
        'epoch-ahead',
        `entry is at epoch ${meta.epoch}, this device is at ${this.state.epoch}`,
      );
    }
    // The epoch and the entry id used here are the ones the relay reports. A
    // client must never try other epochs, or other ids, to make an entry
    // decrypt: at its own epoch a failure is corruption or tampering.
    const frame = decodeFrame(openEntry(this.state.groupKey, ciphertext, meta.epoch, meta.id));
    return {
      contentType: frame.contentType,
      filename: frame.filename,
      body: frame.body,
      createdAt: new Date(Number(frame.createdAt)),
      meta,
    };
  }

  private requireGroup(): void {
    if (!this.inGroup) {
      throw new TppError('no-group', 'this device is not in a group');
    }
  }
}

/** jitter spreads reconnects so that every client of a relay that restarted does not come back in the same millisecond. */
function jitter(ms: number): number {
  return Math.round(ms * (0.8 + Math.random() * 0.4));
}

/** defaultHealthCheck asks the relay whether it is up, which is how a refused socket is told from an unreachable host. */
async function defaultHealthCheck(baseUrl: string): Promise<boolean> {
  const response = await fetch(`${baseUrl}/healthz`);
  return response.ok;
}
