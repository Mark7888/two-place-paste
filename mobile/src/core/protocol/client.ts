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

// Installs the UTF-8 the protobuf codec reaches for. It must be imported
// above the generated messages below: on Hermes they cannot encode or
// decode a single field without it.
import './textEncoding';

import {
  CreateGroupRequest,
  CreateGroupResponse,
  DeviceListRequest,
  DeviceListResponse,
  GroupInfoRequest,
  GroupInfoResponse,
  RekeyRequest,
  RekeyResponse,
  WrappedKey,
} from '../../protocol/gen/tpp/v1/group';
import {
  DeviceRevoked,
  EntryEvicted,
  EpochChanged,
  GroupRenamed,
  StoreItemAdded,
  StoreItemDeleted,
  StoreItemUpdated,
  StoreStateChanged,
  WrappedKeyAvailable,
} from '../../protocol/gen/tpp/v1/events';
import {
  StoreDeleteRequest,
  StoreDeleteResponse,
  StoreFetchChunkRequest,
  StoreFetchChunkResponse,
  StoreItemMeta,
  StoreLimitsRequest,
  StoreLimitsResponse,
  StoreLinksRequest,
  StoreLinksResponse,
  StoreListRequest,
  StoreListResponse,
  StorePutRequest,
  StorePutResponse,
  StoreUpdateMetaRequest,
  StoreUpdateMetaResponse,
  StoreUploadBeginRequest,
  StoreUploadBeginResponse,
} from '../../protocol/gen/tpp/v1/store';
import {
  EntryFetchChunkRequest,
  EntryFetchChunkResponse,
  EntryFetchRequest,
  EntryFetchResponse,
  EntryLimitsRequest,
  EntryLimitsResponse,
  EntryUploadAbortRequest,
  EntryUploadAbortResponse,
  EntryUploadBeginRequest,
  EntryUploadBeginResponse,
  EntryUploadChunkRequest,
  EntryUploadChunkResponse,
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
import { Error as ErrorFrame, ErrorCode } from '../../protocol/gen/tpp/v1/error';
import {
  decodeFrameHead,
  encodeFrame,
  encodeFrameHead,
  entryAAD,
  decodeFrame,
  generateDeviceKey,
  generateGroupKey,
  generateNonce,
  newEntryID,
  contentKey as deriveContentKey,
  decodeStoreMeta,
  encodeStoreMeta,
  META_KEY_SIZE,
  openEntry,
  openEntrySliced,
  openEntrySlicedWithKey,
  sealEntrySliced,
  openEntryWithKey,
  openStoreLink,
  openStoreMeta,
  sealStoreMeta,
  unwrapItemKeys,
  wrapItemKeys,
  openEntryHeader,
  publicKey as derivePublicKey,
  sealEntry,
  sealEntryHeader,
  sealEntryHeaderV2,
  sealStoreLink,
  sha256Sliced,
  storeKey,
  unwrap,
  wrap,
  NONCE_SIZE,
  TAG_SIZE,
  VERSION,
} from '../crypto';
import { Connection, REQUEST_TIMEOUT_MS, SocketFactory, fieldHead, webSocketFactory, type Codec } from './connection';
import { DEFAULT_TTL_MS, decodeEntryMeta, encodeEntryMeta, type EntryCache } from './entryCache';
import { ProtocolError, TppError, isCode } from './errors';
import {
  PARKED_STATE_NAME,
  decodeParked,
  encodeParked,
  groupRef,
  withGroup,
  withoutGroup,
  type ParkedGroup,
} from './groups';
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
import {
  deviceStatusURL,
  operatorLink,
  splitCreationURL,
  normalizeServerURL,
  validOperatorURL,
  websocketURL,
} from './urls';
import { hashHex, type StoreDetails, type StoreItem, type StoreLimits, type StoreListing } from './store';
import { randomBytes } from '../random';
import { concat, toHex, u8 } from '../bytes';

/**
 * SINGLE_FRAME_MAX_BYTES is the most ciphertext one frame carries, in either
 * direction (SPEC §4.3). A larger entry is uploaded in chunks and read back in
 * ranges; the crypto is unchanged — the entry is sealed and opened whole.
 */
export const SINGLE_FRAME_MAX_BYTES = 10 << 20;

/**
 * MAX_CIPHERTEXT_BYTES is the per-entry cap of a relay that predates chunked
 * transfer.
 *
 * @deprecated The cap is the relay's to state: use `Client.limits()`.
 */
export const MAX_CIPHERTEXT_BYTES = SINGLE_FRAME_MAX_BYTES;

/** Limits are a relay's entry limits, in ciphertext bytes. */
export interface Limits {
  maxEntryBytes: number;
  singleFrameMaxBytes: number;
  /** chunkBytes is zero when the relay predates chunked transfer. */
  chunkBytes: number;
  inlineMaxBytes: number;
  /**
   * headerV2 is set when the relay accepts version 2 entry headers
   * (spec/crypto.md §5.5.1). One that predates them refuses them, so this
   * client writes version 1 to it.
   */
  headerV2: boolean;
}

/** LEGACY_LIMITS are what a relay that predates chunked transfer allows. */
const LEGACY_LIMITS: Limits = {
  maxEntryBytes: SINGLE_FRAME_MAX_BYTES,
  singleFrameMaxBytes: SINGLE_FRAME_MAX_BYTES,
  chunkBytes: 0,
  inlineMaxBytes: 0,
  headerV2: false,
};

/** Progress reports a chunked transfer of an entry larger than one frame, in ciphertext bytes. */
export interface Progress {
  entryId: string;
  direction: 'upload' | 'download';
  done: number;
  total: number;
  /**
   * filename and contentType say what is moving, for the progress bar. They
   * come from the entry being written, or from the sealed header of the one
   * being read, and are empty when that header could not be opened.
   */
  filename: string;
  contentType: string;
  /**
   * waiting is true while a download is queued behind another one: large
   * downloads run one at a time, so the relay's bandwidth goes to one entry
   * rather than splitting until every chunk times out.
   */
  waiting: boolean;
}

/** Fetched is an entry's sealed container and metadata, and where they were read from. */
type Fetched = { meta: EntryMeta; ciphertext: Uint8Array; source: 'relay' | 'cache' | 'memory' };

/** EntryHint is what a caller already knows about an entry it is about to read. */
export type EntryHint = Pick<EntryMeta, 'size' | 'header'>;

/** Reconnect bounds. A relay that is down comes back; a phone that hammers it while it does is a second problem. */
const MIN_BACKOFF_MS = 500;
const MAX_BACKOFF_MS = 30_000;

/** SLICED_OPEN_MIN_BYTES is the size above which an entry is decrypted in slices. */
const SLICED_OPEN_MIN_BYTES = 1 << 20;

/**
 * SLICED_SEAL_MIN_BYTES is the size above which an entry is sealed in slices,
 * so the app keeps answering taps while a large upload is encrypted.
 */
const SLICED_SEAL_MIN_BYTES = 1 << 20;

/**
 * ExternalBody is a body the app holds outside JavaScript — a file shared to
 * it, staged by the platform — together with a way to seal it there.
 *
 * Sealing an 80 MB file in JavaScript took most of an upload's time on the
 * phone, whose engine has no JIT. The client still makes every decision that
 * matters: it draws the nonce, derives the content key, builds the associated
 * data and the frame's head, and checks the container that comes back. Only
 * the bulk XChaCha20-Poly1305 over the frame is done by `seal`, which must
 * produce exactly what `sealEntry` would (spec/crypto.md §5.2).
 */
export interface ExternalBody {
  /** size is the body's length in bytes. */
  size: number;
  /** sha256 is the body's SHA-256, worked out by whoever staged it (see `uploadHash`). */
  sha256: Uint8Array;
  /**
   * seal returns the entry container: `head`, then the XChaCha20-Poly1305
   * sealing under `key`, `nonce` and `aad` of `frameHead` followed by the body.
   * It comes back in memory, or staged by the platform (`StagedContainer`),
   * which a large upload sends without bringing it into JavaScript at all.
   */
  seal(params: ExternalSeal): Promise<Uint8Array | StagedContainer>;
}

/**
 * StagedContainer is a sealed container the platform holds in a file of its
 * own. A chunked upload sends it a chunk at a time straight from that file
 * when the socket can (`Socket.sendSpliced`), so the bytes never cross into
 * JavaScript; otherwise it is read in, a chunk at a time.
 */
export interface StagedContainer {
  /** token names the file for `Socket.sendSpliced`. */
  token: string;
  size: number;
  /** head is the container's first bytes, read back from the file, for the shape check. */
  head: Uint8Array;
  /** read brings the whole container into memory: for an upload that fits in one frame. */
  read(): Promise<Uint8Array>;
  /** readSlice brings part of it into memory: a chunk, for a socket that cannot splice. */
  readSlice(offset: number, length: number): Promise<Uint8Array>;
  /** release deletes the file. The client calls it once the upload is over, either way. */
  release(): void;
}

/** Sealed is a sealed container, in memory or staged. */
type Sealed = Uint8Array | StagedContainer;

/** sealedSize is a sealed container's length. */
function sealedSize(c: Sealed): number {
  return c instanceof Uint8Array ? c.length : c.size;
}

/**
 * DownloadSink is where a download goes when it should not enter JavaScript:
 * staged files on the platform, which receives the ranges into one
 * (`Socket.capture`), opens it there, and hands back only the frame's head.
 *
 * As with `ExternalBody`, the client keeps every decision: it reads the
 * nonce from the container, derives the key, builds the associated data,
 * applies the epoch rules, decodes the frame's head, and checks the sizes —
 * and a store item's hash. `open` must refuse what `openEntry` refuses, and
 * write nothing of a container whose tag does not verify (spec/crypto.md §5.4).
 */
export interface DownloadSink {
  /** create makes an empty staged file and returns its token. */
  create(): Promise<string>;
  /** write writes bytes at `offset`: a range the socket handed over whole. */
  write(token: string, offset: number, data: Uint8Array): Promise<void>;
  /** read reads `length` bytes from `offset`. */
  read(token: string, offset: number, length: number): Promise<Uint8Array>;
  /**
   * open opens staged container `token` — version, nonce, sealed frame —
   * into a new staged file holding the frame, and returns its token, length
   * and first bytes (enough for the frame's head).
   */
  open(token: string, params: { key: Uint8Array; nonce: Uint8Array; aad: Uint8Array }): Promise<{ token: string; size: number; head: Uint8Array }>;
  /** digest is the SHA-256 of a staged file from `offset` to its end. */
  digest(token: string, offset: number): Promise<Uint8Array>;
  /** discard deletes a staged file. One kept in the cache stays there. */
  discard(token: string): void;

  /**
   * cached returns the sealed container kept under `key`, if one is and has
   * not expired: a staged file to open, its size, and what was kept with it.
   * It is never deleted by `discard`; `forget` drops it.
   *
   * The cache holds containers only — still sealed — for up to an hour, as
   * the in-memory path's `EntryCache` does, so a preview, a save and an open
   * of the same file are one download.
   */
  cached?(key: string): Promise<{ token: string; size: number; info: string } | null>;
  /** keep moves staged container `token` into the cache under `key`, with `info`, until `keepUntil` (ms). */
  keep?(key: string, token: string, info: string, keepUntil: number): Promise<void>;
  /** forget drops what the cache keeps under `key`: it would not open. */
  forget?(key: string): void;
  /** clear drops everything the cache keeps: the group in use changed. */
  clear?(): void;
}

/** SEALED_CACHE_TTL_MS is how long a container downloaded natively is kept: an hour, as `EntryCache` keeps one. */
export const SEALED_CACHE_TTL_MS = DEFAULT_TTL_MS;

/**
 * StagedItem is a decrypted entry or store item whose body stayed on the
 * platform: `staged` says where it is in which staged file. The caller saves,
 * opens or shares it from there, and calls `release` when done.
 */
export interface StagedItem {
  contentType: string;
  filename: string;
  createdAt: Date;
  meta: EntryMeta | null;
  staged: { token: string; offset: number; size: number; release(): void };
}

/** isStaged tells a staged item from one in memory. */
export function isStaged(item: Item | StagedItem): item is StagedItem {
  return 'staged' in item;
}

/**
 * stagedFrame reads an opened frame's head and makes it a `StagedItem`,
 * refusing a head that does not parse or a body whose length is not the one
 * the head says.
 */
function stagedFrame(
  opened: { token: string; size: number; head: Uint8Array },
  sink: DownloadSink,
  meta: EntryMeta | null,
): StagedItem {
  try {
    const head = decodeFrameHead(opened.head);
    if (head.headLength + head.bodyLength !== opened.size) {
      throw new Error('trailing bytes after the frame');
    }
    return {
      contentType: head.contentType,
      filename: head.filename,
      createdAt: new Date(Number(head.createdAt)),
      meta,
      staged: { token: opened.token, offset: head.headLength, size: head.bodyLength, release: () => sink.discard(opened.token) },
    };
  } catch (err) {
    sink.discard(opened.token);
    throw err;
  }
}

/** release lets go of a staged container; one in memory needs nothing. */
function release(c: Sealed | null): void {
  if (c !== null && !(c instanceof Uint8Array)) {
    c.release();
  }
}

/** ExternalSeal is what `ExternalBody.seal` seals with. */
export interface ExternalSeal {
  key: Uint8Array;
  nonce: Uint8Array;
  aad: Uint8Array;
  /** head is the container's version byte and nonce, which come before the ciphertext. */
  head: Uint8Array;
  /** frameHead is the encoded frame up to the body (`encodeFrameHead`). */
  frameHead: Uint8Array;
}

/**
 * UploadBody is the body of an entry or store item being written: bytes in
 * memory, with their hash when the caller already has it, or an
 * `ExternalBody`.
 */
export type UploadBody = { body: Uint8Array; sha256?: Uint8Array } | { external: ExternalBody };

/**
 * lostWith reports whether a request failed because the socket it went out on
 * is gone — not because the relay refused it or was slow to answer.
 */
function lostWith(err: unknown, conn: Connection): boolean {
  return err instanceof TppError && err.kind === 'disconnected' && conn.isClosed;
}

/** LostSocket carries a read lost with its socket out of a helper, to be asked again; it is never thrown further. */
class LostSocket extends Error {
  constructor(readonly lost: unknown) {
    super('the socket was lost', { cause: lost });
    this.name = 'LostSocket';
  }
}

/** uploadSize is an upload's body length. */
function uploadSize(item: UploadBody): number {
  return 'external' in item ? item.external.size : item.body.length;
}

/**
 * uploadHash is a body's SHA-256: the one the caller passed, or worked out here.
 *
 * Hashing is the slowest step of a large upload on the phone — slower than
 * the encryption, in pure JavaScript with no JIT — and a file shared to the
 * app has already been hashed by the platform while it was copied, at native
 * speed. That hash is taken as given: it is of the same bytes, which the app
 * copied itself, and a wrong one only makes the entry fail its check on the
 * devices that read it, as a corrupted body would.
 */
async function uploadHash(item: UploadBody): Promise<Uint8Array> {
  const given = 'external' in item ? item.external.sha256 : item.sha256;
  if (given === undefined) {
    return sha256Sliced((item as { body: Uint8Array }).body);
  }
  if (given.length !== 32) {
    throw new TppError('invalid', `a body hash is 32 bytes, not ${given.length}`);
  }
  return given;
}

/**
 * sealUpload seals an upload's frame into the container the relay stores
 * (spec/crypto.md §5.2): in slices when it is large and in memory, so the app
 * keeps answering taps, and by the platform when it is an `ExternalBody`.
 */
async function sealUpload(
  item: UploadBody & { contentType: string; filename?: string },
  createdAt: Date,
  groupKey: Uint8Array,
  nonce: Uint8Array,
  epoch: bigint,
  id: string,
): Promise<Sealed> {
  const info = { contentType: item.contentType, filename: item.filename ?? '', createdAt: BigInt(createdAt.getTime()) };
  if (!('external' in item)) {
    const frame = encodeFrame({ ...info, body: item.body });
    return frame.length > SLICED_SEAL_MIN_BYTES
      ? sealEntrySliced(groupKey, nonce, epoch, id, frame)
      : sealEntry(groupKey, nonce, epoch, id, frame);
  }
  const ext = item.external;
  const frameHead = encodeFrameHead(info, ext.size);
  const head = concat(u8(VERSION), nonce);
  const container = await ext.seal({
    key: deriveContentKey(groupKey, nonce, epoch),
    nonce,
    aad: entryAAD(epoch, id),
    head,
    frameHead,
  });
  // The tag cannot be checked without opening it, but the shape can: a
  // container of any other length, or under another head, is not this frame.
  const want = head.length + frameHead.length + ext.size + TAG_SIZE;
  const size = sealedSize(container);
  const first = container instanceof Uint8Array ? container : container.head;
  if (size !== want || first.length < head.length || !head.every((b, i) => first[i] === b)) {
    release(container);
    throw new TppError('invalid', `the sealed body is ${size} bytes, want ${want}`);
  }
  return container;
}

/** RANGE_RETRIES is how many times a download asks again for a range a dropped socket lost. */
const RANGE_RETRIES = 3;

/**
 * MIN_TRANSFER_BYTES_PER_SEC is the slowest link a chunk is given time for,
 * the same floor the Go client uses: each chunk request gets the usual
 * request timeout plus its bytes at this rate, so a slow link is slow rather
 * than failed. An 8 MiB chunk gets 20 s + 64 s.
 */
const MIN_TRANSFER_BYTES_PER_SEC = 128 * 1024;

/** chunkTimeoutMs is how long one chunk request of `bytes` may take. */
function chunkTimeoutMs(bytes: number): number {
  return REQUEST_TIMEOUT_MS + Math.ceil((bytes / MIN_TRANSFER_BYTES_PER_SEC) * 1000);
}

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

/** EntryMeta is everything the relay knows about an entry, plus what this client read from its sealed header. */
export interface EntryMeta {
  id: string;
  epoch: bigint;
  size: number;
  createdAt: Date;
  expiresAt: Date;
  inline: boolean;
  /**
   * header is what the writer said the entry holds (spec/crypto.md §5.5),
   * without fetching it; null when the entry has none or this client cannot
   * open it (another epoch, or tampering). Advisory: the decrypted Item is the
   * authority.
   */
  header: EntryHeaderView | null;
}

/** EntryHeaderView is an entry's type, name and plaintext size, as its writer sealed them. */
export interface EntryHeaderView {
  contentType: string;
  filename: string;
  /** size is the plaintext body length in bytes. */
  size: number;
  /**
   * sha256Hex is the body's hash, from a version 2 header; absent from a
   * version 1 header. A history view checks it against a StoreIndex to mark
   * entries already saved to the store.
   */
  sha256Hex?: string;
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
  /**
   * onGroupDeleted fires when the server admin deleted this device's group:
   * the relay closed the socket saying so, or answered 410 for this device.
   * The client stops re-dialling. Unlike onProbablyRevoked this is not a guess.
   */
  onGroupDeleted?: () => void;
  /** onGroupName fires when the group's display name is first learned or changes. It may be empty. */
  onGroupName?: (name: string) => void;
  /**
   * onOperatorUrl fires when a connection learns the relay operator's
   * website, or learns that it changed (SPEC §4.4). `url` is the plain base
   * URL; `operatorLink` is the link to open. Replacing the identity (forget,
   * a group switch) clears it without firing: the screens redraw then anyway.
   */
  onOperatorUrl?: (url: string) => void;
  /**
   * onEntryEvicted fires when the relay removed an entry before its expiry to
   * stay within its storage limit. A history view drops it.
   */
  onEntryEvicted?: (entryId: string) => void;
  /** onProgress reports a chunked upload or download, once per chunk. */
  onProgress?: (progress: Progress) => void;
  /**
   * onStoreItemAdded and onStoreItemUpdated fire when another device saved or
   * edited a store item, with its metadata opened; a StoreIndex takes them
   * with put. An item that does not open is skipped: the next listStore
   * reports it.
   */
  onStoreItemAdded?: (item: StoreItem) => void;
  onStoreItemUpdated?: (item: StoreItem) => void;
  /** onStoreItemDeleted fires when another device deleted a store item. */
  onStoreItemDeleted?: (itemId: string) => void;
  /** onStoreState fires when the admin turned the store on or off, or it closed to be emptied and opened again. */
  onStoreState?: (enabled: boolean) => void;
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
  /**
   * deviceStatus asks the relay, over plain HTTP, what it would answer this
   * device's upgrade with: 401 revoked, 410 group deleted. It resolves with the
   * status, or null when the relay cannot be reached.
   */
  deviceStatus?: (baseUrl: string, deviceId: string) => Promise<number | null>;
  /**
   * cache keeps large downloaded entries for a while, still encrypted, so
   * opening one twice downloads it once. Without one, nothing is kept.
   */
  cache?: EntryCache;
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
 * of some group to accept it (docs/internal/plans/joiner-emitted-pairing.md).
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

/**
 * toEntryMeta converts the wire metadata, opening the entry's sealed header
 * with the key this client holds. A header that does not open is not an
 * error: the entry's type is unknown until it is fetched, and the fetch
 * applies the real checks.
 */
function toEntryMeta(m: WireEntryMeta, key: { epoch: bigint; groupKey: Uint8Array } | null): EntryMeta {
  const epoch = asBigInt(m.epoch);
  let header: EntryHeaderView | null = null;
  if (key !== null && m.header.length > 0 && epoch === key.epoch) {
    try {
      const h = openEntryHeader(key.groupKey, m.header, epoch, m.entryId);
      header = { contentType: h.contentType, filename: h.filename, size: Number(h.bodyLen) };
      if (h.sha256 !== undefined) {
        header.sha256Hex = toHex(h.sha256);
      }
    } catch {
      header = null;
    }
  }
  return {
    id: m.entryId,
    epoch,
    size: Number(m.size),
    createdAt: msToDate(m.createdAtUnixMs) ?? new Date(0),
    expiresAt: msToDate(m.expiresAtUnixMs) ?? new Date(0),
    inline: m.inline,
    header,
  };
}

/** Client is the protocol client. One instance per app. */
export class Client {
  private state: ClientState;
  private readonly store: SecureStore;
  private readonly socketFactory: SocketFactory;
  private readonly now: () => Date;
  private readonly handlers: Handlers;
  /** limitsCache is the relay's entry limits as stated on one connection; a new connection asks again. */
  private limitsCache: { conn: Connection; limits: Limits } | null = null;
  private readonly healthCheck: (baseUrl: string) => Promise<boolean>;
  private readonly deviceStatus: (baseUrl: string, deviceId: string) => Promise<number | null>;
  private readonly cache: EntryCache | null;
  /** deleted is set once the relay has said this device's group was deleted; nothing re-dials after it. */
  private deleted = false;
  /** downloads are the entry reads in flight, by id: a second read of one joins the first. */
  private readonly downloads = new Map<string, Promise<Fetched>>();
  /**
   * recent is the last small entry read, kept in memory so opening a preview
   * and then copying it reads it once. Large ones go to `cache` instead.
   */
  private recent: { entry: Fetched; at: number } | null = null;
  /** rangeQueue makes large downloads take turns; see `acquireRanges`. */
  private rangeQueue: Promise<void> = Promise.resolve();

  private conn: Connection | null = null;
  private dialling: Promise<Connection> | null = null;
  private wanted = false;
  /** transfers counts chunked uploads and downloads in flight; see `disconnect`. */
  private transfers = 0;
  /** lingering is a disconnect that waits for the transfers in flight to finish first. */
  private lingering = false;
  /** wantedWaiters are downloads that lost their socket in the background, waiting for `connect`. */
  private wantedWaiters: (() => void)[] = [];
  private backoffMs = MIN_BACKOFF_MS;
  private retryTimer: ReturnType<typeof setTimeout> | null = null;
  private refusals = 0;
  private invitations = new Map<string, (device: Device) => void>();
  /**
   * storeKeys are the store keys and links of one group key, opened as needed
   * and never persisted (spec/crypto.md §7a.2). A new group key starts over.
   */
  private storeKeys: { groupKeyHex: string; keys: Map<bigint, Uint8Array>; links: Map<bigint, Uint8Array> | null } | null =
    null;

  /**
   * parked are the groups set aside (groups.ts). parkedLocked is set when the
   * stored record could not be read, so it is never overwritten with a list
   * that lost its groups.
   */
  private parked: ClientState[] = [];
  private parkedLocked = false;
  /**
   * writes orders every write of this client's records. A write started for
   * one group — a rekey's, say — always lands before a switch replaces it, so
   * the active entry can never end up holding the group switched away from.
   */
  private writes: Promise<void> = Promise.resolve();
  /** identityOps serialises the operations that replace the identity: forget and the group switches. */
  private identityOps: Promise<void> = Promise.resolve();
  /**
   * operatorUrlValue is the relay operator's website as the relay last stated
   * it. It is not persisted: it belongs to the relay of the identity in use,
   * so resetTo clears it until the next connection asks again.
   */
  private operatorUrlValue = '';

  private constructor(state: ClientState, options: ClientOptions) {
    this.state = state;
    this.store = options.store;
    this.socketFactory = options.socketFactory ?? webSocketFactory;
    this.now = options.now ?? (() => new Date());
    this.handlers = options.handlers ?? {};
    this.healthCheck = options.healthCheck ?? defaultHealthCheck;
    this.deviceStatus = options.deviceStatus ?? defaultDeviceStatus;
    this.cache = options.cache ?? null;
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
    const client = new Client(state, options);
    await client.loadParked();
    return client;
  }

  /** snapshot returns the current state. It carries both secrets: for persistence and tests, never for display or a log. */
  snapshot(): ClientState {
    return { ...this.state };
  }

  /** epoch is the group key generation this client currently holds. */
  get epoch(): bigint {
    return this.state.epoch;
  }

  /** entryMeta converts wire metadata, reading its header with this client's key. */
  private entryMeta(m: WireEntryMeta): EntryMeta {
    return toEntryMeta(m, this.inGroup ? { epoch: this.state.epoch, groupKey: this.state.groupKey } : null);
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

  /** groupName is the group's display name as the server admin set it, or "". */
  get groupName(): string {
    return this.state.groupName;
  }

  /**
   * operatorUrl is the relay operator's website as the relay last stated it:
   * the plain base URL, without a group id, or "" when the relay names none,
   * predates it, or has not answered on this identity yet.
   */
  get operatorUrl(): string {
    return this.operatorUrlValue;
  }

  /**
   * operatorLink is the link to open for the operator's website: operatorUrl
   * with this device's group id added, or null when there is none to open.
   */
  get operatorLink(): string | null {
    if (this.operatorUrlValue === '' || this.state.groupId === '') {
      return null;
    }
    return operatorLink(this.operatorUrlValue, this.state.groupId);
  }

  /** groupDeleted reports whether the relay has said the admin deleted this device's group. */
  get groupDeleted(): boolean {
    return this.deleted;
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
    this.lingering = false;
    const waiters = this.wantedWaiters;
    this.wantedWaiters = [];
    for (const wake of waiters) {
      wake();
    }
    await this.connection();
  }

  /**
   * disconnect drops the socket and stops re-dialling. Called when the app goes
   * to the background.
   *
   * A chunked transfer the user started is let finish first: switching apps
   * for a moment during a 50 MB download should not throw it away. The socket
   * stays up until the last transfer ends and is closed then, unless the app
   * has come back by that time. Nothing else is done on it in the meantime —
   * no re-dial, and nothing new is started on the app's behalf.
   */
  disconnect(): void {
    this.wanted = false;
    if (this.retryTimer !== null) {
      clearTimeout(this.retryTimer);
      this.retryTimer = null;
    }
    if (this.transfers > 0) {
      this.lingering = true;
      return;
    }
    this.lingering = false;
    const conn = this.conn;
    this.conn = null;
    this.dialling = null;
    conn?.close('the app is no longer in the foreground');
  }

  /**
   * hold runs work the user started that has to outlive the app leaving the
   * screen — an upload handed to the background — and keeps the socket for it
   * the way a chunked transfer does: `disconnect` meanwhile lets it finish
   * and closes the socket after. Without it, the socket closed under a
   * background upload still encrypting, and the upload failed with the
   * connection lost.
   */
  async hold<T>(run: () => Promise<T>): Promise<T> {
    return this.transfer(run);
  }

  /**
   * transfer runs one chunked upload or download, keeping the socket open for
   * it if the app goes to the background meanwhile (see `disconnect`).
   */
  private async transfer<T>(run: () => Promise<T>): Promise<T> {
    this.transfers++;
    try {
      return await run();
    } finally {
      this.transfers--;
      if (this.transfers === 0 && this.lingering && !this.wanted) {
        this.disconnect();
      }
    }
  }

  /** whenWanted resolves the next time the app asks to be online. */
  private whenWanted(): Promise<void> {
    if (this.wanted) {
      return Promise.resolve();
    }
    return new Promise((resolve) => this.wantedWaiters.push(resolve));
  }

  /** connection returns a live connection, dialling if there is none. */
  /**
   * readCall is `call` for a request that only reads — the latest entry, the
   * history, an entry, a range of a store item, the device list, the limits —
   * asked again, once, on a fresh socket when the socket it went out on was
   * lost under it.
   *
   * A socket can drop under a request that has nothing to do with why: a
   * large transfer on the same socket, a network handover. The client
   * reconnects by itself, but the request that was in flight failed with
   * "connection lost", and a preview, a refresh or a sync failed with it.
   * Asking a read again changes nothing on the relay, so it is asked again;
   * a request that writes is not, since it may have been carried out before
   * the socket went. A request that timed out on a live socket is not asked
   * again either: the relay is there, and slow.
   */
  private async readCall<Req, Resp>(
    type: MessageType,
    request: { codec: Codec<Req>; message: Req },
    response: Codec<Resp>,
    timeoutMs?: number,
  ): Promise<Resp> {
    for (let attempt = 0; ; attempt++) {
      const conn = await this.connection();
      try {
        return await conn.call(type, request, response, timeoutMs);
      } catch (err) {
        if (attempt === 0 && lostWith(err, conn)) {
          continue;
        }
        throw err;
      }
    }
  }

  private async connection(): Promise<Connection> {
    if (this.deleted) {
      throw new TppError('group-deleted', "the server admin deleted this device's group");
    }
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
    conn.closed((reason, code) => {
      if (this.conn === conn) {
        this.conn = null;
      }
      if (code === GROUP_DELETED_CLOSE_CODE) {
        this.markDeleted();
      }
      this.handlers.onDisconnected?.(reason);
      this.scheduleRedial();
    });
    this.handlers.onConnected?.();
    // A rename made while this device was away is not replayed as an event,
    // so the name is asked for on every connect.
    void this.refreshGroupInfo(conn);
    return conn;
  }

  /** markDeleted records that the admin deleted the group, once, and stops re-dialling. */
  private markDeleted(): void {
    if (this.deleted) {
      return;
    }
    this.deleted = true;
    if (this.retryTimer !== null) {
      clearTimeout(this.retryTimer);
      this.retryTimer = null;
    }
    this.handlers.onGroupDeleted?.();
  }

  /**
   * refreshGroupInfo asks the relay for the group's name and the operator's
   * website. A relay too old to know the request refuses it, and both stay
   * what they were.
   */
  private async refreshGroupInfo(conn: Connection): Promise<void> {
    try {
      const response = await conn.call(
        MessageType.MESSAGE_TYPE_GROUP_INFO_REQUEST,
        { codec: GroupInfoRequest, message: {} },
        GroupInfoResponse,
      );
      this.setOperatorUrl(response.operatorUrl);
      await this.setGroupName(response.name);
    } catch {
      // Not worth a status line: the name is a label, and the next connect
      // asks again.
    }
  }

  /**
   * setOperatorUrl records the operator's website and tells the screens when
   * it changed. One the app may not open is recorded as none.
   */
  private setOperatorUrl(url: string): void {
    const valid = validOperatorURL(url) ? url : '';
    if (this.operatorUrlValue === valid) {
      return;
    }
    this.operatorUrlValue = valid;
    this.handlers.onOperatorUrl?.(valid);
  }

  /** setGroupName records the group's name and tells the screens when it changed. */
  private async setGroupName(name: string): Promise<void> {
    if (this.state.groupName === name) {
      return;
    }
    this.state = { ...this.state, groupName: name };
    await this.persist();
    this.handlers.onGroupName?.(name);
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
    // The relay answers a plain GET of the endpoint with the status it refused
    // the upgrade with. 410 is definite: the admin deleted the group.
    let status: number | null = null;
    try {
      status = await this.deviceStatus(this.state.serverUrl, this.state.deviceId);
    } catch {
      status = null;
    }
    if (status === 410) {
      this.markDeleted();
      return;
    }
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
    if (!this.wanted || this.deleted || this.retryTimer !== null) {
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
      case MessageType.MESSAGE_TYPE_ENTRY_EVICTED: {
        const event = EntryEvicted.decode(env.payload);
        this.forgetEntry(event.entryId);
        this.handlers.onEntryEvicted?.(event.entryId);
        break;
      }
      case MessageType.MESSAGE_TYPE_STORE_ITEM_ADDED:
      case MessageType.MESSAGE_TYPE_STORE_ITEM_UPDATED: {
        const added = env.type === MessageType.MESSAGE_TYPE_STORE_ITEM_ADDED;
        const m = added ? StoreItemAdded.decode(env.payload).item : StoreItemUpdated.decode(env.payload).item;
        const handler = added ? this.handlers.onStoreItemAdded : this.handlers.onStoreItemUpdated;
        if (m === undefined || handler === undefined) {
          break;
        }
        // Events are handled off the read loop, so opening an item of an
        // earlier epoch may ask for the links.
        try {
          handler(await this.readStoreItem(m));
        } catch {
          // Skipped: the next listing reports it as unreadable.
        }
        break;
      }
      case MessageType.MESSAGE_TYPE_STORE_ITEM_DELETED: {
        this.handlers.onStoreItemDeleted?.(StoreItemDeleted.decode(env.payload).itemId);
        break;
      }
      case MessageType.MESSAGE_TYPE_STORE_STATE_CHANGED: {
        this.handlers.onStoreState?.(StoreStateChanged.decode(env.payload).enabled);
        break;
      }
      case MessageType.MESSAGE_TYPE_GROUP_RENAMED: {
        const event = GroupRenamed.decode(env.payload);
        await this.setGroupName(event.name);
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
    await this.persist();
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
      groupName: '',
    };
    this.deleted = false;
    await this.persist();
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
   * code to show (docs/internal/plans/joiner-emitted-pairing.md §3 steps 1-3).
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
   * dialog must show (docs/internal/plans/joiner-emitted-pairing.md §5). It contacts
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
      groupName: '',
    };
    this.deleted = false;
    await this.persist();
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
    await this.exclusive(async () => {
      await this.resetTo(newState(this.state.deviceName));
      await this.persist();
    });
  }

  /**
   * resetTo installs `state` as this client's identity, offline, and drops
   * everything that belonged to the one before: the socket, invitations, the
   * entry cache, store keys. It does not write `state`.
   */
  private async resetTo(state: ClientState): Promise<void> {
    this.disconnect();
    this.invitations.clear();
    // Entries of a group this device is no longer using are no business of its cache.
    this.recent = null;
    await this.cache?.clear().catch(() => undefined);
    this.refusals = 0;
    this.deleted = false;
    this.storeKeys = null;
    this.limitsCache = null;
    this.operatorUrlValue = '';
    this.backoffMs = MIN_BACKOFF_MS;
    this.state = state;
  }

  /** persist queues a write of `state` (by default the current one) behind every earlier write. */
  private persist(state: ClientState = this.state): Promise<void> {
    const run = this.writes.then(() => saveState(this.store, state));
    this.writes = run.catch(() => undefined);
    return run;
  }

  /** exclusive runs one identity operation after the ones before it have finished. */
  private exclusive<T>(op: () => Promise<T>): Promise<T> {
    const run = this.identityOps.then(op);
    this.identityOps = run.then(
      () => undefined,
      () => undefined,
    );
    return run;
  }

  // -------------------------------------------------------------------------
  // Several groups (spec/crypto.md §10.1)
  // -------------------------------------------------------------------------

  /** activeRef names the group in use, or is "" when this device is in none. */
  get activeRef(): string {
    return this.inGroup ? groupRef(this.state.groupId) : '';
  }

  /**
   * parkedGroups lists the groups set aside, oldest first. A copy of the group
   * in use, left by an interrupted switch, is not listed.
   */
  parkedGroups(): ParkedGroup[] {
    return this.parked
      .filter((g) => g.groupId !== this.state.groupId)
      .map((g) => ({ ref: groupRef(g.groupId), groupName: g.groupName, serverUrl: g.serverUrl }));
  }

  /**
   * parkActive sets the group in use aside and leaves this client in no
   * group, with a new keypair, ready to create a group or pair into one:
   * adding a group is leaving one without deleting its keys. A group the
   * relay said was deleted is discarded instead. With no group it does
   * nothing.
   */
  async parkActive(): Promise<void> {
    await this.exclusive(async () => {
      if (!this.inGroup) {
        return;
      }
      this.requireParkedReadable();
      this.disconnect();
      const outgoing = this.state;
      if (!this.deleted) {
        const next = withGroup(this.parked, outgoing);
        await this.writeParked(next);
        this.parked = next;
      }
      await this.resetTo(newState(outgoing.deviceName));
      await this.persist();
    });
  }

  /**
   * switchGroup makes the group set aside under `ref` the one in use, and
   * sets the one in use aside in its place (or discards it, if the relay said
   * it was deleted). It leaves the client offline: the caller connects, as a
   * launch with that group would.
   *
   * The writes are ordered so that an interruption leaves a group stored
   * twice, never lost (spec/crypto.md §10.1). The device name is the
   * installation's, so the incoming group takes the one in use now.
   */
  async switchGroup(ref: string): Promise<void> {
    await this.exclusive(async () => {
      this.requireParkedReadable();
      const incoming = this.parked.find((g) => groupRef(g.groupId) === ref);
      if (ref === '' || incoming === undefined || incoming.groupId === this.state.groupId) {
        throw new TppError('unknown-group', 'That group is no longer on this device.');
      }
      this.disconnect();
      const outgoing = this.state;
      const next: ClientState = { ...incoming, deviceName: outgoing.deviceName };

      // 1. The group in use joins the list; the incoming one is still in it.
      let both = this.parked;
      if (inGroup(outgoing) && !this.deleted) {
        both = withGroup(both, outgoing);
        await this.writeParked(both);
        this.parked = both;
      }
      // 2. The incoming group becomes the one in use.
      await this.persist(next);
      // 3. And leaves the list. A failure here only leaves a duplicate,
      // which the next open removes.
      const rest = withoutGroup(both, next.groupId);
      await this.writeParked(rest).catch(() => undefined);
      this.parked = rest;
      await this.resetTo(next);
    });
  }

  /**
   * removeParked deletes the keys of a group set aside. Like `forget` it is
   * local: the relay still lists this device until another member revokes it.
   * The group in use cannot be removed this way.
   */
  async removeParked(ref: string): Promise<void> {
    await this.exclusive(async () => {
      this.requireParkedReadable();
      const target = this.parked.find((g) => groupRef(g.groupId) === ref);
      if (ref === '' || target === undefined || target.groupId === this.state.groupId) {
        throw new TppError('unknown-group', 'That group is no longer on this device.');
      }
      const next = withoutGroup(this.parked, target.groupId);
      await this.writeParked(next);
      this.parked = next;
    });
  }

  /**
   * loadParked reads the groups set aside when the client opens, and drops a
   * copy of the group in use that an interrupted switch left behind. It never
   * fails the open: an unreadable record is left alone.
   */
  private async loadParked(): Promise<void> {
    let raw: string | null;
    try {
      raw = await this.store.load(PARKED_STATE_NAME);
    } catch {
      this.parkedLocked = true;
      return;
    }
    if (raw === null) {
      return;
    }
    let groups: ClientState[];
    try {
      groups = decodeParked(raw);
    } catch {
      this.parkedLocked = true;
      return;
    }
    const active = this.state.groupId;
    if (active !== '' && groups.some((g) => g.groupId === active)) {
      groups = withoutGroup(groups, active);
      await this.writeParked(groups).catch(() => undefined);
    }
    this.parked = groups;
  }

  /** writeParked queues a write of the list; an empty list removes the entry. */
  private writeParked(groups: readonly ClientState[]): Promise<void> {
    const run = this.writes.then(() =>
      groups.length === 0
        ? this.store.clear(PARKED_STATE_NAME)
        : this.store.save(PARKED_STATE_NAME, encodeParked(groups)),
    );
    this.writes = run.catch(() => undefined);
    return run;
  }

  private requireParkedReadable(): void {
    if (this.parkedLocked) {
      throw new TppError(
        'parked-unreadable',
        'The other groups were saved by a newer version of TwoPlacePaste; update it to use them.',
      );
    }
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
    const response = await this.readCall(
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
      // The store link below seals the store key of the epoch being
      // replaced, so this device must hold that epoch's group key. One behind
      // a rekey it has not picked up would seal the wrong key and break the
      // chain for every device; it catches up first.
      if (this.state.epoch !== roster.epoch) {
        throw new TppError(
          'epoch-ahead',
          `this device holds the key of epoch ${this.state.epoch}, the group is at ${roster.epoch}`,
        );
      }
      // A brand-new group key at epoch+1 — never a ratchet from the old one,
      // because revocation must be a hard break — wrapped once per remaining
      // device with a fresh ephemeral key each time, and sent in one frame the
      // relay applies atomically, with the store link into the new epoch
      // (spec/crypto.md §7a.2). A relay that predates the store ignores it.
      const newKey = generateGroupKey();
      const epoch = roster.epoch + 1n;
      const wrappedKeys: WrappedKey[] = remaining.map((d) => ({
        deviceId: d.id,
        wrappedGroupKey: wrap(newKey, d.publicKey, epoch),
      }));
      const storeLink = sealStoreLink(storeKey(newKey, epoch), storeKey(this.state.groupKey, roster.epoch), epoch);

      const conn = await this.connection();
      const response = await conn.call(
        MessageType.MESSAGE_TYPE_REKEY_REQUEST,
        {
          codec: RekeyRequest,
          message: {
            revokedDeviceId: target.id,
            expectedEpoch: roster.epoch.toString(),
            wrappedKeys,
            storeLink,
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

  /**
   * putEntry encrypts an item and stores it as the group's latest entry. Last
   * write to reach the relay wins.
   *
   * The body is bytes in memory — with `sha256`, its hash, when the caller
   * already has it, which saves hashing a large body again — or an
   * `ExternalBody` the platform seals (see `uploadHash`, `sealUpload`).
   */
  async putEntry(
    item: { contentType: string; filename?: string; createdAt?: Date } & UploadBody,
  ): Promise<EntryMeta> {
    this.requireGroup();
    if (item.contentType === '') {
      throw new TppError('invalid', 'an entry needs a content type');
    }
    const createdAt = item.createdAt ?? this.now();
    const entryId = newEntryID();
    const epoch = this.state.epoch;
    const groupKey = this.state.groupKey;
    const sealed = await sealUpload(item, createdAt, groupKey, generateNonce(), epoch, entryId);
    try {
      return await this.putSealed(item, sealed, entryId, epoch, groupKey);
    } finally {
      release(sealed);
    }
  }

  /** putSealed uploads a sealed entry: in one frame, or in chunks. */
  private async putSealed(
    item: { contentType: string; filename?: string } & UploadBody,
    sealed: Sealed,
    entryId: string,
    epoch: bigint,
    groupKey: Uint8Array,
  ): Promise<EntryMeta> {
    const size = sealedSize(sealed);
    // The limits say which header version the relay takes, and whether a
    // large entry can be chunked. They are asked once per connection.
    const limits = await this.limits();
    const summary = {
      contentType: item.contentType,
      filename: item.filename ?? '',
      bodyLen: BigInt(uploadSize(item)),
    };
    // The key read with the epoch, not the current one: a rekey during the
    // awaits above must not seal the header under another epoch's key.
    const header = limits.headerV2
      ? sealEntryHeaderV2(groupKey, epoch, entryId, { ...summary, sha256: await uploadHash(item) })
      : sealEntryHeader(groupKey, epoch, entryId, summary);

    let response: EntryPutResponse;
    if (size > SINGLE_FRAME_MAX_BYTES) {
      // Larger than one frame: chunked, if the relay can. Refused here rather
      // than by the relay when it cannot, or when the entry is over its cap:
      // the caller deserves the answer before the upload, not after it.
      if (limits.chunkBytes === 0 || size > limits.maxEntryBytes) {
        throw new TppError('too-large', `ciphertext is ${size} bytes, the relay accepts ${limits.maxEntryBytes}`);
      }
      response = await this.transfer(() =>
        this.putChunked(entryId, epoch, sealed, header, limits, {
          filename: item.filename ?? '',
          contentType: item.contentType,
        }),
      );
    } else {
      // One frame: a staged container this small is read in whole.
      const container = sealed instanceof Uint8Array ? sealed : await sealed.read();
      const conn = await this.connection();
      response = await conn.call(
        MessageType.MESSAGE_TYPE_ENTRY_PUT_REQUEST,
        {
          codec: EntryPutRequest,
          message: {
            entryId,
            epoch: epoch.toString(),
            size: container.length.toString(),
            header,
            ciphertext: container,
          },
        },
        EntryPutResponse,
      );
    }
    const meta = response.meta ? this.entryMeta(response.meta) : null;
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
    const response = await this.readCall(
      MessageType.MESSAGE_TYPE_ENTRY_LATEST_REQUEST,
      { codec: EntryLatestRequest, message: {} },
      EntryLatestResponse,
    );
    if (!response.meta) {
      throw new TppError('no-entry', 'the group has no entry');
    }
    const meta = this.entryMeta(response.meta);
    if (!meta.inline) {
      // A large entry lives in the blob backend and is pulled by id.
      return this.read(meta.id, meta);
    }
    return await this.open(meta, response.ciphertext);
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
    const response = await this.readCall(
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
      entries: response.entries.map((m) => this.entryMeta(m)),
      nextBefore: msToDate(response.nextBeforeUnixMs),
    };
  }

  /** getEntry fetches one entry by id and decrypts it — what the history tab does when the user taps an entry. */
  async getEntry(entryId: string, hint?: EntryHint): Promise<Item> {
    this.requireGroup();
    return this.read(entryId, hint);
  }

  /**
   * getEntryStaged is `getEntry` for an entry whose body should not enter
   * JavaScript — a large file to save, open or share. The ranges go to a
   * staged file (captured by the socket where it can), the container is
   * opened there, and the result is a `StagedItem`; only the frame's head is
   * read here. A relay that cannot serve ranges gets the ordinary read, and
   * an `Item`.
   *
   * It does not join a read already running, nor use the cache: both hold
   * the container in memory, which is what this avoids.
   */
  async getEntryStaged(entryId: string, hint: EntryHint | undefined, sink: DownloadSink): Promise<Item | StagedItem> {
    this.requireGroup();
    const key = `entry:${entryId}`;
    const cached = (await sink.cached?.(key).catch(() => null)) ?? null;
    if (cached !== null) {
      const meta = decodeEntryMeta(cached.info);
      if (meta !== null && meta.id === entryId && Number(meta.size) === cached.size) {
        try {
          return await this.openStagedEntry(meta, cached.token, sink);
        } catch (err) {
          // An epoch refusal is the entry's, not the copy's: the relay's copy
          // would be refused the same way.
          if (err instanceof TppError && (err.kind === 'stale-entry' || err.kind === 'epoch-ahead')) {
            throw err;
          }
        }
      }
      // A copy that does not open, or does not match, is dropped and the
      // entry read again from the relay.
      sink.forget?.(key);
    }

    const limits = await this.limits();
    if (limits.chunkBytes === 0) {
      return this.read(entryId, hint);
    }
    const rx = await sink.create();
    try {
      const { meta } = await this.transfer(() => this.readRanges(entryId, limits, hint, { sink, token: rx }));
      const item = await this.openStagedEntry(meta, rx, sink);
      // Kept once it has opened, so the cache never holds a container that
      // does not; a cache that cannot keep it is only a cache that does not help.
      const until = Math.min(this.now().getTime() + SEALED_CACHE_TTL_MS, meta.expiresAt.getTime());
      await sink.keep?.(key, rx, encodeEntryMeta(meta), until).catch(() => undefined);
      return item;
    } finally {
      // A container moved into the cache is not deleted by this.
      sink.discard(rx);
    }
  }

  /** openStagedEntry opens a staged entry container natively, after the epoch rules. */
  private async openStagedEntry(meta: EntryMeta, token: string, sink: DownloadSink): Promise<StagedItem> {
    this.checkEpoch(meta);
    const head = await sink.read(token, 0, 1 + NONCE_SIZE);
    if (head.length !== 1 + NONCE_SIZE || head[0] !== VERSION) {
      throw new TppError('invalid', `entry ${meta.id} is not a version ${VERSION} container`);
    }
    const nonce = head.slice(1);
    const opened = await sink.open(token, {
      key: deriveContentKey(this.state.groupKey, nonce, meta.epoch),
      nonce,
      aad: entryAAD(meta.epoch, meta.id),
    });
    return stagedFrame(opened, sink, meta);
  }

  /**
   * read fetches and decrypts one entry. A copy from the cache that does not
   * open — damaged on disk, say — is dropped and the entry read again from
   * the relay, once. An epoch refusal is not retried: the relay's copy is the
   * same entry at the same epoch, and would be refused the same way.
   */
  private async read(entryId: string, hint?: EntryHint): Promise<Item> {
    const fetched = await this.fetch(entryId, hint);
    try {
      return await this.open(fetched.meta, fetched.ciphertext);
    } catch (err) {
      const epochRefusal =
        err instanceof TppError && (err.kind === 'stale-entry' || err.kind === 'epoch-ahead');
      if (fetched.source === 'relay' || epochRefusal) {
        throw err;
      }
      this.forgetEntry(entryId);
      const fresh = await this.fetch(entryId, hint);
      return await this.open(fresh.meta, fresh.ciphertext);
    }
  }

  /** forgetEntry drops an entry from memory and the cache: it was evicted, or its copy did not open. */
  private forgetEntry(entryId: string): void {
    if (this.recent?.entry.meta.id === entryId) {
      this.recent = null;
    }
    void this.cache?.remove(entryId).catch(() => undefined);
  }

  /**
   * limits returns the relay's entry limits, asking once per connection. A
   * relay that predates chunked transfer answers that it does not know the
   * question, and gets the limits it has always had.
   */
  async limits(): Promise<Limits> {
    // Asked again, once, on a fresh socket if the first is lost (see readCall).
    try {
      return await this.limitsOn(await this.connection());
    } catch (err) {
      if (!(err instanceof LostSocket)) {
        throw err;
      }
    }
    try {
      return await this.limitsOn(await this.connection());
    } catch (err) {
      throw err instanceof LostSocket ? err.lost : err;
    }
  }

  /** limitsOn asks one socket for the limits, which are kept for as long as it lasts. */
  private async limitsOn(conn: Connection): Promise<Limits> {
    if (this.limitsCache?.conn === conn) {
      return this.limitsCache.limits;
    }
    let limits = LEGACY_LIMITS;
    try {
      const resp = await conn.call(
        MessageType.MESSAGE_TYPE_ENTRY_LIMITS_REQUEST,
        { codec: EntryLimitsRequest, message: {} },
        EntryLimitsResponse,
      );
      // Never trust a relay to raise the frame cap: nothing here would read
      // a larger frame.
      limits = {
        maxEntryBytes: Number(resp.maxEntryBytes),
        singleFrameMaxBytes: Math.min(Math.max(Number(resp.singleFrameMaxBytes), 1), SINGLE_FRAME_MAX_BYTES),
        chunkBytes: Math.min(Number(resp.chunkBytes), SINGLE_FRAME_MAX_BYTES),
        inlineMaxBytes: Number(resp.inlineMaxBytes),
        headerV2: resp.headerV2,
      };
    } catch (err) {
      if (lostWith(err, conn)) {
        throw new LostSocket(err);
      }
      if (!isCode(err, ErrorCode.ERROR_CODE_UNSUPPORTED_MESSAGE)) {
        throw err;
      }
    }
    this.limitsCache = { conn, limits };
    return limits;
  }

  /**
   * uploadChunk sends bytes `off` to `end` of a sealed container as chunk
   * `index` of an upload. A staged container goes straight from the
   * platform's file to the socket when the socket can splice, so the chunk
   * never crosses into JavaScript; otherwise it is read in first.
   *
   * The connection is looked up per chunk: an upload belongs to the device,
   * not the socket, so a reconnect mid-upload carries on.
   */
  private async uploadChunk(handle: string, index: number, container: Sealed, off: number, end: number): Promise<void> {
    const conn = await this.connection();
    const length = end - off;
    let data: Uint8Array;
    if (container instanceof Uint8Array) {
      data = container.subarray(off, end);
    } else if (conn.canSplice) {
      // The request up to its last field, `data`; the slice follows on the wire.
      const head = concat(
        EntryUploadChunkRequest.encode({ uploadHandle: handle, index, data: new Uint8Array(0) }).finish(),
        fieldHead(3, length),
      );
      await conn.callSpliced(
        MessageType.MESSAGE_TYPE_ENTRY_UPLOAD_CHUNK_REQUEST,
        head,
        { token: container.token, offset: off, length },
        EntryUploadChunkResponse,
        chunkTimeoutMs(length),
      );
      return;
    } else {
      data = await container.readSlice(off, length);
      if (data.length !== length) {
        throw new TppError('invalid', `a staged chunk read ${data.length} bytes, want ${length}`);
      }
    }
    await conn.call(
      MessageType.MESSAGE_TYPE_ENTRY_UPLOAD_CHUNK_REQUEST,
      { codec: EntryUploadChunkRequest, message: { uploadHandle: handle, index, data } },
      EntryUploadChunkResponse,
      chunkTimeoutMs(length),
    );
  }

  /**
   * putChunked uploads a sealed container larger than one frame. On any
   * failure the upload is aborted, so the room the relay reserved for it is
   * given back at once rather than at its timeout.
   */
  private async putChunked(
    entryId: string,
    epoch: bigint,
    container: Sealed,
    header: Uint8Array,
    limits: Limits,
    what: { filename: string; contentType: string },
  ): Promise<EntryPutResponse> {
    const size = sealedSize(container);
    const begin = await (await this.connection()).call(
      MessageType.MESSAGE_TYPE_ENTRY_UPLOAD_BEGIN_REQUEST,
      {
        codec: EntryUploadBeginRequest,
        message: { entryId, epoch: epoch.toString(), size: size.toString() },
      },
      EntryUploadBeginResponse,
    );
    const handle = begin.uploadHandle;
    let chunk = Number(begin.chunkBytes);
    if (!(chunk > 0 && chunk <= SINGLE_FRAME_MAX_BYTES)) {
      chunk = limits.chunkBytes;
    }
    const abort = async (cause: unknown): Promise<never> => {
      try {
        await (await this.connection()).call(
          MessageType.MESSAGE_TYPE_ENTRY_UPLOAD_ABORT_REQUEST,
          { codec: EntryUploadAbortRequest, message: { uploadHandle: handle } },
          EntryUploadAbortResponse,
        );
      } catch {
        // The relay abandons it at its timeout regardless.
      }
      throw cause;
    };

    const progress = (done: number) =>
      this.handlers.onProgress?.({ entryId, direction: 'upload', done, total: size, waiting: false, ...what });
    progress(0);
    try {
      for (let index = 0, off = 0; off < size; index++, off += chunk) {
        const end = Math.min(off + chunk, size);
        await this.uploadChunk(handle, index, container, off, end);
        progress(end);
      }
      return await (await this.connection()).call(
        MessageType.MESSAGE_TYPE_ENTRY_PUT_REQUEST,
        {
          codec: EntryPutRequest,
          message: {
            entryId,
            epoch: epoch.toString(),
            size: size.toString(),
            header,
            uploadHandle: handle,
          },
        },
        EntryPutResponse,
      );
    } catch (err) {
      return abort(err);
    }
  }

  /**
   * fetchRanges reads an entry one range at a time; the first range brings the
   * metadata.
   *
   * Progress is reported for any entry that takes more than one range — not
   * only for those past the single-frame cap, which left an entry between the
   * chunk size and that cap downloading in several ranges with no bar. When the
   * caller already knows the size (from the latest-entry or history listing),
   * the bar starts at zero before the first range is asked for, rather than
   * appearing only once a whole chunk has arrived.
   */
  private fetchRanges(
    entryId: string,
    limits: Limits,
    hint?: EntryHint,
  ): Promise<{ meta: EntryMeta; ciphertext: Uint8Array }> {
    return this.transfer(() => this.readRanges(entryId, limits, hint));
  }

  /**
   * acquireRanges waits for this download's turn and returns the function that
   * hands the turn on. Large downloads run one at a time: the relay serves one
   * device's requests in order, so two at once only means each chunk waits
   * behind the other's, and both get slower until they time out. A small entry
   * — one range — never queues.
   */
  private acquireRanges(): Promise<() => void> {
    let release!: () => void;
    const turn = new Promise<void>((resolve) => {
      release = resolve;
    });
    const previous = this.rangeQueue;
    this.rangeQueue = previous.then(() => turn);
    return previous.then(() => release);
  }

  /**
   * readRanges is the body of `fetchRanges`.
   *
   * A range lost to a dropped socket is asked for again from the same offset
   * rather than failing the download: once the app is back in the foreground
   * when the drop happened in the background, and after a short pause up to
   * RANGE_RETRIES times when it happened in the foreground. Reading a range is
   * idempotent, so asking twice costs nothing but the bytes.
   */
  private async readRanges(
    entryId: string,
    limits: Limits,
    hint?: EntryHint,
    into?: { sink: DownloadSink; token: string },
  ): Promise<{ meta: EntryMeta; ciphertext: Uint8Array }> {
    let retries = 0;
    let meta: EntryMeta | null = null;
    let buf = new Uint8Array(0);
    let filled = 0;
    let release: (() => void) | null = null;
    const chunked = (size: number) => size > limits.chunkBytes;
    const report = (done: number, total: number, header: EntryHeaderView | null, waiting = false) =>
      this.handlers.onProgress?.({
        entryId,
        direction: 'download',
        done,
        total,
        filename: header?.filename ?? '',
        contentType: header?.contentType ?? '',
        waiting,
      });
    // takeTurn queues this download behind any other large one (see
    // `acquireRanges`), saying so on the progress bar while it waits.
    const takeTurn = async (total: number, header: EntryHeaderView | null) => {
      report(filled, total, header, true);
      release = await this.acquireRanges();
      report(filled, total, header);
    };

    try {
      if (hint !== undefined && hint.size > 0 && chunked(hint.size)) {
        await takeTurn(hint.size, hint.header);
      }
      for (;;) {
        let resp: EntryFetchChunkResponse;
        let captured: number | null = null;
        try {
          const conn = await this.connection();
          const request = {
            codec: EntryFetchChunkRequest,
            message: { entryId, offset: filled.toString(), length: limits.chunkBytes.toString() },
          };
          if (into !== undefined && conn.canCapture) {
            // Straight to the staged file: the range never enters JavaScript.
            const got = await conn.callCapturing(
              MessageType.MESSAGE_TYPE_ENTRY_FETCH_CHUNK_REQUEST,
              request,
              EntryFetchChunkResponse,
              MessageType.MESSAGE_TYPE_ENTRY_FETCH_CHUNK_RESPONSE,
              { token: into.token, offset: filled },
              chunkTimeoutMs(limits.chunkBytes),
            );
            resp = got.reply;
            captured = got.captured;
          } else {
            resp = await conn.call(
              MessageType.MESSAGE_TYPE_ENTRY_FETCH_CHUNK_REQUEST,
              request,
              EntryFetchChunkResponse,
              chunkTimeoutMs(limits.chunkBytes),
            );
          }
        } catch (err) {
          if (!(err instanceof TppError && err.kind === 'disconnected')) {
            throw err;
          }
          if (!this.wanted) {
            // Lost while the app was away: carry on when it comes back.
            await this.whenWanted();
            continue;
          }
          if (retries >= RANGE_RETRIES) {
            throw err;
          }
          retries++;
          await new Promise((resolve) => setTimeout(resolve, jitter(MIN_BACKOFF_MS * 2 ** retries)));
          continue;
        }
        retries = 0;
        if (!resp.meta) {
          throw new TppError('no-entry', `the relay returned no metadata for entry ${entryId}`);
        }
        const m = this.entryMeta(resp.meta);
        const size = Number(m.size);
        if (meta === null) {
          if (!(size > 0 && size <= Math.max(limits.maxEntryBytes, SINGLE_FRAME_MAX_BYTES))) {
            throw new TppError('invalid', `entry ${entryId} reports ${size} bytes, outside the relay's limits`);
          }
          meta = m;
          if (into === undefined) {
            buf = new Uint8Array(size);
          }
        } else if (m.id !== meta.id || m.size !== meta.size) {
          throw new TppError('invalid', `entry ${entryId} changed while it was being read`);
        }
        const length = captured ?? resp.data.length;
        if (Number(resp.offset) !== filled || length === 0 || filled + length > size) {
          throw new TppError('invalid', `entry ${entryId}: the relay returned a range that does not continue the entry`);
        }
        if (into === undefined) {
          buf.set(resp.data, filled);
        } else if (captured === null) {
          await into.sink.write(into.token, filled, resp.data);
        }
        filled += length;
        if (filled === size) {
          if (chunked(size)) {
            report(filled, size, meta.header ?? hint?.header ?? null);
          }
          return { meta, ciphertext: buf };
        }
        if (chunked(size)) {
          if (release === null) {
            // No size was known up front, so the first range is what showed
            // this is a large entry. It takes its turn before the rest.
            await takeTurn(size, meta.header ?? hint?.header ?? null);
          } else {
            report(filled, size, meta.header ?? hint?.header ?? null);
          }
        }
      }
    } finally {
      (release as (() => void) | null)?.();
    }
  }

  /**
   * fetch pulls one entry's ciphertext. A read of an entry that is already
   * being read joins that one rather than downloading it a second time — a
   * preview and a copy of the same row are one download.
   */
  private fetch(entryId: string, hint?: EntryHint): Promise<Fetched> {
    const running = this.downloads.get(entryId);
    if (running !== undefined) {
      return running;
    }
    const download = this.fetchOnce(entryId, hint).finally(() => {
      this.downloads.delete(entryId);
    });
    this.downloads.set(entryId, download);
    return download;
  }

  /**
   * fetchOnce reads an entry from wherever it is nearest: the last small entry
   * read, then the cache of large ones, then the relay — in ranges from a relay
   * that can serve them, which is the only way to read an entry larger than
   * one frame, and in one frame from a relay that predates them.
   */
  private async fetchOnce(entryId: string, hint?: EntryHint): Promise<Fetched> {
    const now = this.now().getTime();
    if (this.recent !== null && this.recent.entry.meta.id === entryId) {
      if (now - this.recent.at < DEFAULT_TTL_MS) {
        return { ...this.recent.entry, source: 'memory' };
      }
      this.recent = null;
    }
    if (this.cache !== null) {
      const cached = await this.cache.get(entryId).catch(() => null);
      if (cached !== null) {
        return { ...cached, source: 'cache' };
      }
    }

    const limits = await this.limits();
    let got: { meta: EntryMeta; ciphertext: Uint8Array };
    if (limits.chunkBytes > 0) {
      got = await this.fetchRanges(entryId, limits, hint);
    } else {
      const response = await this.readCall(
        MessageType.MESSAGE_TYPE_ENTRY_FETCH_REQUEST,
        { codec: EntryFetchRequest, message: { entryId } },
        EntryFetchResponse,
      );
      if (!response.meta) {
        throw new TppError('no-entry', `the relay returned no metadata for entry ${entryId}`);
      }
      got = { meta: this.entryMeta(response.meta), ciphertext: response.ciphertext };
    }

    const fetched: Fetched = { ...got, source: 'relay' };
    if (limits.chunkBytes > 0 && got.ciphertext.length > limits.chunkBytes) {
      // Large: worth keeping on disk. Written in the background — the caller
      // is waiting for this entry, not for its copy — and a cache that cannot
      // be written is only a cache that does not help.
      void this.cache?.put(got).catch(() => undefined);
    } else {
      this.recent = { entry: fetched, at: now };
    }
    return fetched;
  }

  /** checkEpoch applies the epoch rules of spec/crypto.md §7 to an entry about to be opened. */
  private checkEpoch(meta: EntryMeta): void {
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
  }

  /**
   * open applies the epoch rules and decrypts. A large container is decrypted
   * a slice at a time (`openEntrySliced`), so the app keeps answering taps
   * through the seconds a big entry takes on the phone.
   */
  private async open(meta: EntryMeta, ciphertext: Uint8Array): Promise<Item> {
    this.checkEpoch(meta);
    // The epoch and the entry id used here are the ones the relay reports. A
    // client must never try other epochs, or other ids, to make an entry
    // decrypt: at its own epoch a failure is corruption or tampering.
    const plain =
      ciphertext.length > SLICED_OPEN_MIN_BYTES
        ? await openEntrySliced(this.state.groupKey, ciphertext, meta.epoch, meta.id)
        : openEntry(this.state.groupKey, ciphertext, meta.epoch, meta.id);
    const frame = decodeFrame(plain);
    return {
      contentType: frame.contentType,
      filename: frame.filename,
      body: frame.body,
      createdAt: new Date(Number(frame.createdAt)),
      meta,
    };
  }

  // -------------------------------------------------------------------------
  // The store (SPEC §6b, spec/crypto.md §7a), mirroring pkg/tppclient/store.go
  // -------------------------------------------------------------------------

  /** storeLimits asks the relay for the group's store limits and usage. A relay without a store has none. */
  async storeLimits(): Promise<StoreLimits> {
    this.requireGroup();
    try {
      const r = await this.readCall(
        MessageType.MESSAGE_TYPE_STORE_LIMITS_REQUEST,
        { codec: StoreLimitsRequest, message: {} },
        StoreLimitsResponse,
      );
      return {
        enabled: r.enabled,
        maxBytes: Number(r.maxBytes),
        usedBytes: Number(r.usedBytes),
        availableBytes: Number(r.availableBytes),
        maxItems: Number(r.maxItems),
        usedItems: Number(r.usedItems),
        maxItemBytes: Number(r.maxItemBytes),
      };
    } catch (err) {
      if (isCode(err, ErrorCode.ERROR_CODE_UNSUPPORTED_MESSAGE)) {
        return { enabled: false, maxBytes: 0, usedBytes: 0, availableBytes: 0, maxItems: 0, usedItems: 0, maxItemBytes: 0 };
      }
      throw err;
    }
  }

  /**
   * saveToStore encrypts an item and saves it in the group's store with the
   * details the user typed (§7a.3-§7a.5). It does not look for duplicates: a
   * screen checks the item's hash against its StoreIndex first, and asks.
   */
  async saveToStore(
    item: { contentType: string; filename?: string; createdAt?: Date } & UploadBody,
    details: StoreDetails,
  ): Promise<StoreItem> {
    this.requireGroup();
    if (item.contentType === '') {
      throw new TppError('invalid', 'a store item needs a content type');
    }
    const sum = await uploadHash(item);
    let meta: Uint8Array;
    try {
      meta = encodeStoreMeta({
        name: details.name,
        description: details.description,
        tags: details.tags,
        contentType: item.contentType,
        filename: item.filename ?? '',
        bodyLen: BigInt(uploadSize(item)),
        sha256: sum,
      });
    } catch (err) {
      throw new TppError('invalid', `the item's details: ${(err as Error).message}`, { cause: err });
    }
    const createdAt = item.createdAt ?? this.now();
    const itemId = newEntryID();
    const epoch = this.state.epoch;
    const groupKey = this.state.groupKey;

    // The blob is an entry container at this epoch, bound to the item's id;
    // its content key is the one wrapped below (§7a.4).
    const nonce = generateNonce();
    const contentKey = deriveContentKey(groupKey, nonce, epoch);
    const container = await sealUpload(item, createdAt, groupKey, nonce, epoch, itemId);
    let response: StorePutResponse;
    let key: Uint8Array;
    try {
      const size = sealedSize(container);
      const metaKey = randomBytes(META_KEY_SIZE);
      key = storeKey(groupKey, epoch);
      const wrapped = wrapItemKeys(key, generateNonce(), epoch, itemId, { contentKey, metaKey });
      const sealedMeta = sealStoreMeta(metaKey, generateNonce(), itemId, 0n, meta);

      // The limits are asked first, so a store that is off, full or too small
      // refuses before anything is uploaded.
      const limits = await this.storeLimits();
      if (!limits.enabled) {
        throw new ProtocolError(ErrorCode.ERROR_CODE_STORE_DISABLED, 'the store is turned off for this group');
      }
      if (limits.maxItemBytes > 0 && size > limits.maxItemBytes) {
        throw new TppError('too-large', `the item is ${size} bytes sealed, the store takes ${limits.maxItemBytes}`);
      }
      if (size > limits.availableBytes) {
        throw new ProtocolError(
          ErrorCode.ERROR_CODE_QUOTA_EXCEEDED,
          `the item is ${size} bytes sealed, the store has room for ${limits.availableBytes}`,
        );
      }

      const base = {
        itemId,
        epoch: epoch.toString(),
        size: size.toString(),
        wrappedItemKeys: wrapped,
        sealedMeta,
      };
      const what = { filename: item.filename ?? '', contentType: item.contentType };
      if (size > SINGLE_FRAME_MAX_BYTES) {
        response = await this.transfer(() => this.storeUploadChunked(base, container, what));
      } else {
        // One frame: a staged container this small is read in whole.
        const ciphertext = container instanceof Uint8Array ? container : await container.read();
        response = await (await this.connection()).call(
          MessageType.MESSAGE_TYPE_STORE_PUT_REQUEST,
          { codec: StorePutRequest, message: { ...base, ciphertext } },
          StorePutResponse,
          chunkTimeoutMs(size),
        );
      }
    } finally {
      release(container);
    }
    const m = response.item;
    if (
      m === undefined ||
      m.itemId !== itemId ||
      m.contentId !== itemId ||
      asBigInt(m.wrapEpoch) !== epoch ||
      asBigInt(m.contentEpoch) !== epoch
    ) {
      throw new TppError('invalid', 'the relay stored the item under other ids or epochs than the ones bound into it');
    }
    return this.openStoreItem(m, key);
  }

  /** storeUploadChunked uploads a blob larger than one frame and commits it, aborting on any failure. */
  private async storeUploadChunked(
    base: { itemId: string; epoch: string; size: string; wrappedItemKeys: Uint8Array; sealedMeta: Uint8Array },
    container: Sealed,
    what: { filename: string; contentType: string },
  ): Promise<StorePutResponse> {
    const size = sealedSize(container);
    const begin = await (await this.connection()).call(
      MessageType.MESSAGE_TYPE_STORE_UPLOAD_BEGIN_REQUEST,
      { codec: StoreUploadBeginRequest, message: { itemId: base.itemId, epoch: base.epoch, size: base.size } },
      StoreUploadBeginResponse,
    );
    const handle = begin.uploadHandle;
    let chunk = Number(begin.chunkBytes);
    if (!(chunk > 0 && chunk <= SINGLE_FRAME_MAX_BYTES)) {
      chunk = SINGLE_FRAME_MAX_BYTES;
    }
    const progress = (done: number) =>
      this.handlers.onProgress?.({ entryId: base.itemId, direction: 'upload', done, total: size, waiting: false, ...what });
    progress(0);
    try {
      for (let index = 0, off = 0; off < size; index++, off += chunk) {
        const end = Math.min(off + chunk, size);
        await this.uploadChunk(handle, index, container, off, end);
        progress(end);
      }
      return await (await this.connection()).call(
        MessageType.MESSAGE_TYPE_STORE_PUT_REQUEST,
        { codec: StorePutRequest, message: { ...base, uploadHandle: handle } },
        StorePutResponse,
      );
    } catch (err) {
      try {
        await (await this.connection()).call(
          MessageType.MESSAGE_TYPE_ENTRY_UPLOAD_ABORT_REQUEST,
          { codec: EntryUploadAbortRequest, message: { uploadHandle: handle } },
          EntryUploadAbortResponse,
        );
      } catch {
        // The relay abandons it at its timeout regardless.
      }
      throw err;
    }
  }

  /**
   * listStore lists and opens every item of the group's store, newest first.
   * An item that does not open is reported in `unreadable`, never dropped.
   */
  async listStore(): Promise<StoreListing> {
    this.requireGroup();
    const out: StoreListing = { items: [], unreadable: [] };
    let cursor = '';
    for (;;) {
      const resp = await this.readCall(
        MessageType.MESSAGE_TYPE_STORE_LIST_REQUEST,
        { codec: StoreListRequest, message: { limit: 0, cursor } },
        StoreListResponse,
      );
      for (const m of resp.items) {
        try {
          out.items.push(await this.readStoreItem(m));
        } catch (err) {
          if (!(err instanceof TppError && (err.kind === 'store-unreadable' || err.kind === 'epoch-ahead'))) {
            throw err;
          }
          out.unreadable.push({
            id: m.itemId,
            savedAt: msToDate(m.savedAtUnixMs) ?? new Date(0),
            reason:
              err.kind === 'epoch-ahead'
                ? 'saved with a group key this device has not received yet'
                : 'it could not be decrypted on this device',
          });
        }
      }
      cursor = resp.nextCursor;
      if (cursor === '') {
        return out;
      }
    }
  }

  /** readStoreItem opens one listed item, walking the links when its keys are wrapped at an earlier epoch (§7a.6). */
  private async readStoreItem(m: StoreItemMeta): Promise<StoreItem> {
    const key = await this.storeKeyAt(asBigInt(m.wrapEpoch));
    return this.openStoreItem(m, key);
  }

  /** openStoreItem unwraps an item's keys and opens its metadata at the revision the relay reports. */
  private openStoreItem(m: StoreItemMeta, key: Uint8Array): StoreItem {
    const wrapEpoch = asBigInt(m.wrapEpoch);
    const rev = asBigInt(m.metaRev);
    try {
      const keys = unwrapItemKeys(key, m.wrappedItemKeys, wrapEpoch, m.itemId);
      const meta = decodeStoreMeta(openStoreMeta(keys.metaKey, m.sealedMeta, m.itemId, rev));
      return {
        id: m.itemId,
        savedAt: msToDate(m.savedAtUnixMs) ?? new Date(0),
        name: meta.name,
        description: meta.description,
        tags: meta.tags,
        contentType: meta.contentType,
        filename: meta.filename,
        bodySize: Number(meta.bodyLen),
        sha256Hex: hashHex(meta.sha256),
        blobSize: Number(m.size),
        metaRev: rev,
        wrapEpoch,
        secrets: {
          contentKey: keys.contentKey,
          metaKey: keys.metaKey,
          contentId: m.contentId,
          contentEpoch: asBigInt(m.contentEpoch),
        },
      };
    } catch (err) {
      throw new TppError('store-unreadable', `store item ${m.itemId} did not open`, { cause: err });
    }
  }

  /**
   * fetchStoreItem downloads an item's blob and decrypts it: what Copy and
   * Save as file do. The body is checked against the hash in the item's
   * metadata.
   */
  async fetchStoreItem(it: StoreItem): Promise<Item> {
    this.requireGroup();
    const buf = new Uint8Array(it.blobSize);
    await this.readStoreBlob(it, (offset, data) => {
      buf.set(data, offset);
      return Promise.resolve();
    });
    const size = it.blobSize;

    const { contentKey, contentId, contentEpoch } = it.secrets;
    let plain: Uint8Array;
    try {
      plain =
        size > SLICED_OPEN_MIN_BYTES
          ? await openEntrySlicedWithKey(contentKey, buf, contentEpoch, contentId)
          : openEntryWithKey(contentKey, buf, contentEpoch, contentId);
    } catch (err) {
      throw new TppError('store-unreadable', `store item ${it.id} did not decrypt`, { cause: err });
    }
    const frame = decodeFrame(plain);
    if (frame.body.length !== it.bodySize || hashHex(await sha256Sliced(frame.body)) !== it.sha256Hex) {
      throw new TppError('store-unreadable', `store item ${it.id} does not match its metadata`);
    }
    return {
      contentType: frame.contentType,
      filename: frame.filename,
      body: frame.body,
      createdAt: new Date(Number(frame.createdAt)),
      meta: null,
    };
  }

  /**
   * readStoreBlob reads a store item's sealed blob range by range, handing
   * each range to `put` — or, when `capture` names a staged file and the
   * socket can, capturing it straight into that file.
   */
  private async readStoreBlob(
    it: StoreItem,
    put: (offset: number, data: Uint8Array) => Promise<void>,
    capture?: string,
  ): Promise<void> {
    const size = it.blobSize;
    const chunked = size > SINGLE_FRAME_MAX_BYTES;
    let filled = 0;
    const progress = (done: number) =>
      this.handlers.onProgress?.({
        entryId: it.id,
        direction: 'download',
        done,
        total: size,
        waiting: false,
        filename: it.filename,
        contentType: it.contentType,
      });
    const read = async () => {
      if (chunked) {
        progress(0);
      }
      while (filled < size) {
        const request = {
          codec: StoreFetchChunkRequest,
          message: { itemId: it.id, offset: filled.toString(), length: SINGLE_FRAME_MAX_BYTES.toString() },
        };
        const timeout = chunkTimeoutMs(Math.min(SINGLE_FRAME_MAX_BYTES, size - filled));
        let resp: StoreFetchChunkResponse | undefined;
        let captured: number | null = null;
        // A range lost with its socket is asked again, once, on a fresh one:
        // reading a range twice costs nothing but the bytes (see readCall).
        for (let attempt = 0; resp === undefined; attempt++) {
          const conn = await this.connection();
          try {
            if (capture !== undefined && conn.canCapture) {
              // Straight to the staged file: the range never enters JavaScript.
              const got = await conn.callCapturing(
                MessageType.MESSAGE_TYPE_STORE_FETCH_CHUNK_REQUEST,
                request,
                StoreFetchChunkResponse,
                MessageType.MESSAGE_TYPE_STORE_FETCH_CHUNK_RESPONSE,
                { token: capture, offset: filled },
                timeout,
              );
              resp = got.reply;
              captured = got.captured;
            } else {
              resp = await conn.call(MessageType.MESSAGE_TYPE_STORE_FETCH_CHUNK_REQUEST, request, StoreFetchChunkResponse, timeout);
            }
          } catch (err) {
            if (attempt > 0 || !lostWith(err, conn)) {
              throw err;
            }
          }
        }
        const length = captured ?? resp.data.length;
        if (
          resp.item === undefined ||
          resp.item.itemId !== it.id ||
          Number(resp.item.size) !== size ||
          Number(resp.offset) !== filled ||
          length === 0 ||
          filled + length > size
        ) {
          throw new TppError('invalid', `store item ${it.id}: the relay returned a range that does not continue the item`);
        }
        if (captured === null) {
          await put(filled, resp.data);
        }
        filled += length;
        if (chunked) {
          progress(filled);
        }
      }
    };
    await (chunked ? this.transfer(read) : read());
  }

  /**
   * fetchStoreItemStaged is `fetchStoreItem` for an item whose body should
   * not enter JavaScript: read into a staged file, opened there, and checked
   * against its metadata — size and hash — there too. It returns a
   * `StagedItem`.
   */
  async fetchStoreItemStaged(it: StoreItem, sink: DownloadSink): Promise<StagedItem> {
    this.requireGroup();
    const key = `store:${it.id}`;
    const cached = (await sink.cached?.(key).catch(() => null)) ?? null;
    if (cached !== null) {
      if (cached.size === it.blobSize) {
        try {
          return await this.openStagedStoreItem(it, cached.token, sink);
        } catch {
          // Dropped below, and read again from the relay.
        }
      }
      sink.forget?.(key);
    }
    const rx = await sink.create();
    try {
      await this.readStoreBlob(it, (offset, data) => sink.write(rx, offset, data), rx);
      const item = await this.openStagedStoreItem(it, rx, sink);
      await sink.keep?.(key, rx, '', this.now().getTime() + SEALED_CACHE_TTL_MS).catch(() => undefined);
      return item;
    } finally {
      sink.discard(rx);
    }
  }

  /** openStagedStoreItem opens a staged store blob natively and checks it against the item's metadata. */
  private async openStagedStoreItem(it: StoreItem, token: string, sink: DownloadSink): Promise<StagedItem> {
    const { contentKey, contentId, contentEpoch } = it.secrets;
    const head = await sink.read(token, 0, 1 + NONCE_SIZE);
    if (head.length !== 1 + NONCE_SIZE || head[0] !== VERSION) {
      throw new TppError('store-unreadable', `store item ${it.id} is not a version ${VERSION} container`);
    }
    const nonce = head.slice(1);
    let opened: { token: string; size: number; head: Uint8Array };
    try {
      opened = await sink.open(token, { key: contentKey, nonce, aad: entryAAD(contentEpoch, contentId) });
    } catch (err) {
      throw new TppError('store-unreadable', `store item ${it.id} did not decrypt`, { cause: err });
    }
    const item = stagedFrame(opened, sink, null);
    try {
      const sum = await sink.digest(item.staged.token, item.staged.offset);
      if (item.staged.size !== it.bodySize || hashHex(sum) !== it.sha256Hex) {
        throw new TppError('store-unreadable', `store item ${it.id} does not match its metadata`);
      }
    } catch (err) {
      item.staged.release();
      throw err;
    }
    return item;
  }

  /**
   * editStoreItem replaces an item's name, description and tags, starting from
   * the revision it was listed at: when another device edited it first the
   * relay refuses with REVISION_CONFLICT and nothing changes.
   */
  async editStoreItem(it: StoreItem, details: StoreDetails): Promise<StoreItem> {
    this.requireGroup();
    let encoded: Uint8Array;
    try {
      encoded = encodeStoreMeta({
        name: details.name,
        description: details.description,
        tags: details.tags,
        contentType: it.contentType,
        filename: it.filename,
        bodyLen: BigInt(it.bodySize),
        sha256: hexBytes(it.sha256Hex),
      });
    } catch (err) {
      throw new TppError('invalid', `the item's details: ${(err as Error).message}`, { cause: err });
    }
    const next = it.metaRev + 1n;
    const sealed = sealStoreMeta(it.secrets.metaKey, generateNonce(), it.id, next, encoded);
    const resp = await (await this.connection()).call(
      MessageType.MESSAGE_TYPE_STORE_UPDATE_META_REQUEST,
      {
        codec: StoreUpdateMetaRequest,
        message: { itemId: it.id, expectedMetaRev: it.metaRev.toString(), sealedMeta: sealed },
      },
      StoreUpdateMetaResponse,
    );
    if (resp.item === undefined || resp.item.itemId !== it.id || asBigInt(resp.item.metaRev) !== next) {
      throw new TppError('invalid', `the relay answered the edit of ${it.id} with another item or revision`);
    }
    return { ...it, name: details.name, description: details.description, tags: [...details.tags], metaRev: next };
  }

  /** deleteStoreItem deletes an item for every device of the group. A screen asks first. */
  async deleteStoreItem(itemId: string): Promise<void> {
    this.requireGroup();
    await (await this.connection()).call(
      MessageType.MESSAGE_TYPE_STORE_DELETE_REQUEST,
      { codec: StoreDeleteRequest, message: { itemId } },
      StoreDeleteResponse,
    );
  }

  /**
   * storeKeyAt returns the store key of epoch `w`: derived from the group key
   * at the current epoch and opened link by link below it. The links are
   * fetched once per group key.
   */
  private async storeKeyAt(w: bigint): Promise<Uint8Array> {
    const epoch = this.state.epoch;
    const groupKey = this.state.groupKey;
    if (w > epoch) {
      throw new TppError('epoch-ahead', `a store item is wrapped at epoch ${w}, this device is at ${epoch}`);
    }
    if (w < 1n) {
      throw new TppError('store-unreadable', `a store item is wrapped at epoch ${w}`);
    }
    const groupKeyHex = toHex(groupKey);
    if (this.storeKeys === null || this.storeKeys.groupKeyHex !== groupKeyHex) {
      this.storeKeys = { groupKeyHex, keys: new Map([[epoch, storeKey(groupKey, epoch)]]), links: null };
    }
    const cache = this.storeKeys;
    const cached = cache.keys.get(w);
    if (cached !== undefined) {
      return cached;
    }
    if (cache.links === null) {
      const resp = await this.readCall(
        MessageType.MESSAGE_TYPE_STORE_LINKS_REQUEST,
        { codec: StoreLinksRequest, message: {} },
        StoreLinksResponse,
      );
      cache.links = new Map(resp.links.map((l) => [asBigInt(l.epoch), l.link]));
    }
    let key = cache.keys.get(epoch) as Uint8Array;
    for (let e = epoch; e > w; e--) {
      const known = cache.keys.get(e - 1n);
      if (known !== undefined) {
        key = known;
        continue;
      }
      const link = cache.links.get(e);
      if (link === undefined) {
        throw new TppError('store-unreadable', `the relay has no store link into epoch ${e}`);
      }
      try {
        key = openStoreLink(key, link, e);
      } catch (err) {
        throw new TppError('store-unreadable', `the store link into epoch ${e} did not open`, { cause: err });
      }
      cache.keys.set(e - 1n, key);
    }
    return key;
  }

  private requireGroup(): void {
    if (!this.inGroup) {
      throw new TppError('no-group', 'this device is not in a group');
    }
  }
}

/** hexBytes parses the lowercase hex of a hash. */
function hexBytes(hex: string): Uint8Array {
  const out = new Uint8Array(hex.length / 2);
  for (let i = 0; i < out.length; i++) {
    out[i] = parseInt(hex.slice(2 * i, 2 * i + 2), 16);
  }
  return out;
}

/** jitter spreads reconnects so that every client of a relay that restarted does not come back in the same millisecond. */
function jitter(ms: number): number {
  return Math.round(ms * (0.8 + Math.random() * 0.4));
}

/**
 * GROUP_DELETED_CLOSE_CODE is the WebSocket close code the relay closes a
 * device's socket with when the admin deletes its group.
 */
const GROUP_DELETED_CLOSE_CODE = 4410;

/** defaultDeviceStatus asks the relay what it would answer this device's upgrade with. */
async function defaultDeviceStatus(baseUrl: string, deviceId: string): Promise<number | null> {
  try {
    const response = await fetch(deviceStatusURL(baseUrl, deviceId));
    return response.status;
  } catch {
    return null;
  }
}

/** defaultHealthCheck asks the relay whether it is up, which is how a refused socket is told from an unreachable host. */
async function defaultHealthCheck(baseUrl: string): Promise<boolean> {
  const response = await fetch(`${baseUrl}/healthz`);
  return response.ok;
}
