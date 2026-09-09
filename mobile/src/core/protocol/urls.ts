/**
 * Relay URLs (SPEC §3.1, §5.1).
 *
 * `URL` is used rather than a regular expression: Hermes ships it, Node ships
 * it, and hand-parsing a URL is how a client ends up dialling a host the user
 * did not type.
 */

import { TppError } from './errors';

/** WEBSOCKET_PATH is where the relay mounts the transport. */
export const WEBSOCKET_PATH = '/ws';

/**
 * normalizeServerURL validates a relay base URL and strips everything that is
 * not scheme, host and port. A base URL with a path would silently produce
 * wrong WebSocket and creation URLs.
 */
export function normalizeServerURL(raw: string): string {
  let url: URL;
  try {
    url = new URL(raw.trim());
  } catch (cause) {
    throw new TppError('invalid', `server URL "${raw}" is not a URL`, { cause });
  }
  if (url.protocol !== 'http:' && url.protocol !== 'https:') {
    throw new TppError('invalid', `server URL "${raw}" must be http or https`);
  }
  if (!url.host) {
    throw new TppError('invalid', 'server URL has no host');
  }
  return `${url.protocol}//${url.host}`;
}

/**
 * splitCreationURL splits https://<host>/<token> — the string behind the QR
 * code on the admin screen — into its base URL and token (SPEC §3.1).
 */
export function splitCreationURL(raw: string): { base: string; token: string } {
  let url: URL;
  try {
    url = new URL(raw.trim());
  } catch (cause) {
    throw new TppError('invalid', `creation URL "${raw}" is not a URL`, { cause });
  }
  const token = url.pathname.replace(/^\/+|\/+$/g, '');
  if (token === '' || token.includes('/')) {
    throw new TppError('invalid', `creation URL "${raw}" does not end in a single-segment token`);
  }
  return { base: normalizeServerURL(`${url.protocol}//${url.host}`), token };
}

/**
 * websocketURL derives the transport endpoint from a base URL. `deviceId` is
 * the connection's credential and is omitted for the two unauthenticated
 * flows, group creation and pairing-join.
 */
export function websocketURL(base: string, deviceId: string): string {
  const url = new URL(normalizeServerURL(base));
  url.protocol = url.protocol === 'https:' ? 'wss:' : 'ws:';
  url.pathname = WEBSOCKET_PATH;
  if (deviceId !== '') {
    url.searchParams.set('device_id', deviceId);
  }
  return url.toString();
}
