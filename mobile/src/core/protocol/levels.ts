/**
 * Protocol levels and relay capabilities (SPEC §5.3). The Go client's
 * `protocol.go` is the twin of this file; the two must agree.
 *
 * Apps and relays upgrade independently. Almost every change stays
 * compatible: a relay announces a new feature as a capability and the app
 * checks for it. A change that cannot stay compatible raises the protocol
 * level instead, which is rare and deliberate.
 */

import { Capability } from '../../protocol/gen/tpp/v1/hello';

/**
 * PROTOCOL_LEVEL is the level this build speaks. Every connection states it
 * as the `proto` URL parameter, so a relay can refuse an app too old for it
 * before the upgrade.
 */
export const PROTOCOL_LEVEL = 1;

/** PROTOCOL_PARAM is the URL parameter the level travels in. */
export const PROTOCOL_PARAM = 'proto';

/**
 * OUTDATED_RETRY_MS is how long a client the relay refused for its age waits
 * before asking again while the app stays open. Nothing changes until the app
 * or the relay is updated; coming back to the foreground asks at once anyway.
 */
export const OUTDATED_RETRY_MS = 60 * 60 * 1000;

/** RelayInfo is what a relay supports, as its HelloResponse stated it. */
export interface RelayInfo {
  protocolMin: number;
  protocolMax: number;
  capabilities: Capability[];
}

/** LEGACY_RELAY is a relay that predates HelloRequest: level 1, nothing extra. */
export const LEGACY_RELAY: RelayInfo = { protocolMin: 1, protocolMax: 1, capabilities: [] };

/** relayHas reports whether a relay offers a capability. */
export function relayHas(relay: RelayInfo, capability: Capability): boolean {
  return relay.capabilities.includes(capability);
}

/** Refusal is the relay refusing this build at the upgrade for its age. */
export interface Refusal {
  protocolMin: number;
  protocolMax: number;
}

/**
 * refusalForAge reads the answer to a plain GET of the transport endpoint and
 * reports whether it is the relay refusing this build for its age.
 *
 * The status alone means nothing: the relay's WebSocket library answers 426
 * to every request that is not an upgrade, so a plain GET from an app the
 * relay accepts gets a 426 too. Only the JSON body identifies the refusal.
 */
export function refusalForAge(status: number, body: string): Refusal | null {
  if (status !== 426) {
    return null;
  }
  let parsed: unknown;
  try {
    parsed = JSON.parse(body);
  } catch {
    return null;
  }
  if (typeof parsed !== 'object' || parsed === null) {
    return null;
  }
  const fields = parsed as { error?: unknown; protocol_min?: unknown; protocol_max?: unknown };
  if (fields.error !== 'client_outdated') {
    return null;
  }
  return {
    protocolMin: typeof fields.protocol_min === 'number' ? fields.protocol_min : 0,
    protocolMax: typeof fields.protocol_max === 'number' ? fields.protocol_max : 0,
  };
}
