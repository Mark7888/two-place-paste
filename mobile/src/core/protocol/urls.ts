/**
 * Relay URLs (SPEC §3.1, §5.1).
 *
 * These are parsed here rather than with the global `URL`, which is not the
 * same class on the two runtimes this code has to work on.
 *
 * Node's `URL` is WHATWG-compliant. React Native's is a regex-backed shim
 * (`react-native/Libraries/Blob/URL.js`) that has getters for `protocol`,
 * `host` and `pathname` but **no setters** for them — and Metro emits sloppy
 * mode (`@react-native/babel-preset` sets `strictMode: false`), so assigning
 * to one is not a TypeError, it is a silent no-op. A `websocketURL` written
 * against the WHATWG setters therefore passed every unit test on Node and, on
 * a phone, returned the base URL unchanged: the client dialled
 * `https://relay.example.com/` instead of `wss://relay.example.com/ws`, the
 * relay answered the admin page rather than an upgrade, and every flow died
 * with "the relay could not be reached: socket error".
 *
 * Parsing by hand is the only way to make the two runtimes agree, and it is
 * what keeps these tests — which run on Node — honest about what ships.
 */

import { utf8 } from '../bytes';
import { TppError } from './errors';

/** WEBSOCKET_PATH is where the relay mounts the transport. */
export const WEBSOCKET_PATH = '/ws';

/**
 * ABSOLUTE_URL splits `scheme://authority[path][?query][#fragment]`. It is
 * deliberately only as clever as this system needs: a relay address is an
 * origin and at most one path segment, never a URL with userinfo or a
 * relative reference.
 */
const ABSOLUTE_URL = /^([A-Za-z][A-Za-z\d+.-]*):\/\/([^/?#]*)([^?#]*)/;

/** HOSTNAME is a registered name or an IPv4 address; BRACKETED is an IPv6 literal. */
const HOSTNAME = /^[A-Za-z\d._~%-]+$/;
const BRACKETED = /^\[[\dA-Fa-f:.]+\]$/;

/** RelayURL is a relay address taken apart: the pieces this system uses, and no others. */
interface RelayURL {
  scheme: 'http' | 'https';
  /** host is the authority, lowercased, port included when there is one. */
  host: string;
  /** path is everything from the first "/" to the query, and is "" when there is none. */
  path: string;
}

/**
 * parse validates a relay URL and splits it. `what` names the kind of URL for
 * the error message, which is shown to the user and never carries a secret —
 * a creation URL's token is in its path, so the message quotes only what the
 * user typed, which they already have in front of them.
 */
function parse(raw: string, what: string): RelayURL {
  const trimmed = raw.trim();
  const match = ABSOLUTE_URL.exec(trimmed);
  if (match === null) {
    throw new TppError('invalid', `${what} "${raw}" is not a URL`);
  }
  const scheme = match[1].toLowerCase();
  if (scheme !== 'http' && scheme !== 'https') {
    throw new TppError('invalid', `${what} "${raw}" must be http or https`);
  }

  // Userinfo is refused rather than stripped: a relay address that carries
  // credentials is one this client has no use for, and dropping them silently
  // would dial somewhere the user did not mean.
  const authority = match[2];
  if (authority.includes('@')) {
    throw new TppError('invalid', `${what} "${raw}" must not carry credentials`);
  }
  const host = normalizeHost(authority);
  if (host === null) {
    throw new TppError('invalid', `${what} "${raw}" has no usable host`);
  }
  return { scheme, host, path: match[3] };
}

/**
 * normalizeHost lowercases an authority and checks its host and port, or
 * returns null when it is not one. Hosts are case-insensitive, and lowercasing
 * is what keeps a typed address and a scanned one producing the same socket
 * URL.
 */
function normalizeHost(authority: string): string | null {
  const lowered = authority.toLowerCase();
  let host = lowered;
  let port = '';

  if (lowered.startsWith('[')) {
    // An IPv6 literal keeps its brackets, and its colons are not the port's.
    const end = lowered.indexOf(']');
    if (end === -1) {
      return null;
    }
    host = lowered.slice(0, end + 1);
    const rest = lowered.slice(end + 1);
    if (rest !== '') {
      if (!rest.startsWith(':')) {
        return null;
      }
      port = rest.slice(1);
    }
    if (!BRACKETED.test(host)) {
      return null;
    }
  } else {
    const colon = lowered.indexOf(':');
    if (colon !== -1) {
      host = lowered.slice(0, colon);
      port = lowered.slice(colon + 1);
    }
    if (!HOSTNAME.test(host)) {
      return null;
    }
  }

  if (port === '') {
    return host;
  }
  if (!/^\d{1,5}$/.test(port) || Number(port) === 0 || Number(port) > 65535) {
    return null;
  }
  return `${host}:${port}`;
}

/**
 * normalizeServerURL validates a relay base URL and strips everything that is
 * not scheme, host and port. A base URL with a path would silently produce
 * wrong WebSocket and creation URLs.
 */
export function normalizeServerURL(raw: string): string {
  const { scheme, host } = parse(raw, 'server URL');
  return `${scheme}://${host}`;
}

/**
 * splitCreationURL splits https://<host>/<token> — the string behind the QR
 * code on the admin screen — into its base URL and token (SPEC §3.1).
 */
export function splitCreationURL(raw: string): { base: string; token: string } {
  const { scheme, host, path } = parse(raw, 'creation URL');
  const token = path.replace(/^\/+|\/+$/g, '');
  if (token === '' || token.includes('/')) {
    throw new TppError('invalid', `creation URL "${raw}" does not end in a single-segment token`);
  }
  return { base: `${scheme}://${host}`, token };
}

/**
 * websocketURL derives the transport endpoint from a base URL. `deviceId` is
 * the connection's credential and is omitted for the three unauthenticated
 * flows: group creation, pairing-join and offer minting.
 */
/**
 * deviceStatusURL is the transport endpoint over plain HTTP(S). A GET of it is
 * not an upgrade, so the relay answers with the status it would refuse the
 * upgrade with — 401 for a revoked device, 410 for one whose group the admin
 * deleted — which the WebSocket API itself never shows.
 */
export function deviceStatusURL(base: string, deviceId: string): string {
  const { scheme, host } = parse(base, 'server URL');
  return `${scheme}://${host}${WEBSOCKET_PATH}?device_id=${encodeURIComponent(deviceId)}`;
}

export function websocketURL(base: string, deviceId: string): string {
  const { scheme, host } = parse(base, 'server URL');
  const transport = scheme === 'https' ? 'wss' : 'ws';
  const query = deviceId === '' ? '' : `?device_id=${encodeURIComponent(deviceId)}`;
  return `${transport}://${host}${WEBSOCKET_PATH}${query}`;
}

/**
 * OPERATOR_GROUP_PARAM is the query parameter an operator link carries the
 * group's id in (SPEC §4.4).
 */
export const OPERATOR_GROUP_PARAM = 'group_id';

/**
 * validOperatorURL reports whether `url` is a website the app may open: an
 * absolute http:// or https:// URL with a host, without credentials, spaces
 * or control characters. Anything else — javascript:, intent:, file:, a bare
 * host — is never opened, whatever the relay sent. The Go client applies the
 * same rules (pkg/tppclient/operator.go), and both test the same cases.
 */
export function validOperatorURL(url: string): boolean {
  const lower = url.toLowerCase();
  let rest: string;
  if (lower.startsWith('https://')) {
    rest = url.slice('https://'.length);
  } else if (lower.startsWith('http://')) {
    rest = url.slice('http://'.length);
  } else {
    return false;
  }
  for (let i = 0; i < url.length; i++) {
    const c = url.charCodeAt(i);
    if (c <= 0x20 || c === 0x7f) {
      return false;
    }
  }
  const end = rest.search(/[/?#]/);
  const host = end < 0 ? rest : rest.slice(0, end);
  return host !== '' && !host.includes('@') && !host.startsWith(':');
}

/**
 * operatorLink adds `groupId` to the operator's website as `group_id`,
 * keeping any query already there and any fragment at the end, so the
 * operator's site can tell which group came from the app. It returns null
 * when the URL is not one the app may open, or there is no group.
 */
export function operatorLink(base: string, groupId: string): string | null {
  if (groupId === '' || !validOperatorURL(base)) {
    return null;
  }
  const hash = base.indexOf('#');
  let head = hash < 0 ? base : base.slice(0, hash);
  const fragment = hash < 0 ? null : base.slice(hash + 1);
  const param = `${OPERATOR_GROUP_PARAM}=${escapeQueryValue(groupId)}`;
  if (!head.includes('?')) {
    head += `?${param}`;
  } else if (head.endsWith('?') || head.endsWith('&')) {
    head += param;
  } else {
    head += `&${param}`;
  }
  return fragment === null ? head : `${head}#${fragment}`;
}

/**
 * escapeQueryValue percent-encodes every UTF-8 byte but RFC 3986's unreserved
 * ones. encodeURIComponent would leave !'()* as they are, and the Go client
 * encodes them, so this is written out to match it byte for byte.
 */
function escapeQueryValue(value: string): string {
  let out = '';
  for (const byte of utf8(value)) {
    const c = String.fromCharCode(byte);
    if (/[A-Za-z0-9\-_.~]/.test(c)) {
      out += c;
    } else {
      out += `%${byte.toString(16).toUpperCase().padStart(2, '0')}`;
    }
  }
  return out;
}
