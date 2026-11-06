/**
 * Several groups on one installation (spec/crypto.md §10.1,
 * docs/internal/plans/multi-group-mobile.md).
 *
 * One group is in use: its state is the client's, under the `session` entry a
 * single-group build reads, so a downgrade carries on with it. The others are
 * set aside — parked — together under one more entry. This file is the
 * record's shape and the ref a screen names a group by; the client does the
 * switching.
 */

import { sha256 } from '@noble/hashes/sha2.js';

import { toHex, utf8 } from '../bytes';
import { TppError } from './errors';
import { fromStored, toStored, type ClientState, type StoredState } from './state';

/** PARKED_STATE_NAME is the secure-store entry the groups set aside are kept under. */
export const PARKED_STATE_NAME = 'parked-groups';

/** PARKED_VERSION is the record shape this build reads and writes. */
export const PARKED_VERSION = 1;

/** ParkedGroup is a group set aside, as a screen shows it: no id and no key. */
export interface ParkedGroup {
  /** ref names the group to `switchGroup` and `removeParked`. */
  ref: string;
  /** groupName is the group's display name as it was when it was set aside. */
  groupName: string;
  /** serverUrl is the relay the group lives on. */
  serverUrl: string;
}

/** GROUP_REF_INFO separates a ref's hash from every other use of SHA-256 here. */
const GROUP_REF_INFO = 'tpp/v1/group-ref';

/**
 * groupRef is a stable name for a group that says nothing about it: the first
 * 16 hex characters of SHA-256 over "tpp/v1/group-ref" and the group id — the
 * same as the Go core's `GroupRef`. Group ids are bearer credentials on the
 * wire, so a screen gets this instead.
 */
export function groupRef(groupId: string): string {
  if (groupId === '') {
    return '';
  }
  return toHex(sha256(utf8(GROUP_REF_INFO + groupId)).subarray(0, 8));
}

/** The stored record: a version and the groups, each in the shape `session` holds. */
interface ParkedRecord {
  version: number;
  groups: StoredState[];
}

/** encodeParked serializes the groups set aside for the secure store. */
export function encodeParked(groups: readonly ClientState[]): string {
  const record: ParkedRecord = { version: PARKED_VERSION, groups: groups.map(toStored) };
  return JSON.stringify(record);
}

/**
 * decodeParked parses what encodeParked wrote. A record of another version is
 * an error, never a guess: the caller leaves it as it is rather than
 * overwriting groups it cannot read.
 */
export function decodeParked(raw: string): ClientState[] {
  let record: ParkedRecord;
  try {
    record = JSON.parse(raw) as ParkedRecord;
  } catch (cause) {
    throw new TppError('invalid', 'the groups set aside could not be decoded', { cause });
  }
  if (record.version !== PARKED_VERSION || !Array.isArray(record.groups)) {
    throw new TppError(
      'invalid',
      `the groups set aside are in record version ${String(record.version)}, this build reads ${PARKED_VERSION}`,
    );
  }
  return record.groups.map(fromStored);
}

/** withGroup returns groups with `state` in it, replacing an older record of the same group. */
export function withGroup(groups: readonly ClientState[], state: ClientState): ClientState[] {
  return [...withoutGroup(groups, state.groupId), state];
}

/** withoutGroup returns groups without the one whose id is given. */
export function withoutGroup(groups: readonly ClientState[], groupId: string): ClientState[] {
  return groups.filter((g) => g.groupId !== groupId);
}
