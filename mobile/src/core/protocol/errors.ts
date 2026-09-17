/**
 * The conditions a caller branches on (docs/conventions.md §1, §11: wrap with
 * context, preserve the cause).
 *
 * Nothing here ever carries plaintext, a key or a token in its message: an
 * error reaches a log and a crash reporter (spec/crypto.md §10).
 */

import { ErrorCode } from '../../protocol/gen/tpp/v1/error';

/** TppErrorKind names the local conditions a UI reacts to differently. */
export type TppErrorKind =
  /** This device is not in a group yet: create one or pair first. */
  | 'no-group'
  /** The group has no unexpired entry. Not a failure — a newly paired device starts empty. */
  | 'no-entry'
  /**
   * The latest entry predates this client's epoch. Per spec/crypto.md §7 it is
   * skipped silently: no error to the user, no retry, no keeping the old key.
   */
  | 'stale-entry'
  /** The entry is newer than this client's epoch: it is behind a rekey and its wrapped key has not arrived. */
  | 'epoch-ahead'
  /** The relay no longer knows this device: it was revoked while away (SPEC §3.3 step 5). */
  | 'revoked'
  /** The connection is gone. */
  | 'disconnected'
  /** The client was closed. */
  | 'closed'
  /** A malformed or unusable value — a pairing code that is not one, a server URL that is not a URL. */
  | 'invalid';

/** TppError is a local failure with a kind a caller can switch on. */
export class TppError extends Error {
  readonly kind: TppErrorKind;

  constructor(kind: TppErrorKind, message: string, options?: { cause?: unknown }) {
    super(message, options);
    this.name = 'TppError';
    this.kind = kind;
  }
}

/** ProtocolError is an Error frame from the relay. Callers branch on `code`, never on `message` (SPEC §5.1). */
export class ProtocolError extends Error {
  readonly code: ErrorCode;

  constructor(code: ErrorCode, message: string) {
    super(`relay error ${ErrorCode[code] ?? code}: ${message}`);
    this.name = 'ProtocolError';
    this.code = code;
  }
}

/** isKind reports whether an error is a local failure of one kind. */
export function isKind(err: unknown, kind: TppErrorKind): boolean {
  return err instanceof TppError && err.kind === kind;
}

/** isCode reports whether an error is a relay error with one code. */
export function isCode(err: unknown, code: ErrorCode): boolean {
  return err instanceof ProtocolError && err.code === code;
}

/**
 * isEpochConflict means another device rekeyed first, or an entry was sealed
 * under an epoch the group has moved past: refetch and retry (SPEC §3.3 step 4).
 */
export function isEpochConflict(err: unknown): boolean {
  return isCode(err, ErrorCode.ERROR_CODE_EPOCH_CONFLICT);
}

/** isTooLarge means the ciphertext exceeded the 10 MB per-entry cap (SPEC §4.3). */
export function isTooLarge(err: unknown): boolean {
  return isCode(err, ErrorCode.ERROR_CODE_TOO_LARGE);
}

/** describe renders an error for the one-line status a screen shows. It never renders a cause chain into the UI. */
export function describe(err: unknown): string {
  if (err instanceof TppError || err instanceof ProtocolError) {
    return err.message;
  }
  if (err instanceof Error) {
    return err.message;
  }
  return String(err);
}
