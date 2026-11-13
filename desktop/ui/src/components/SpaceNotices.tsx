/**
 * The out-of-space banners and popup (SPEC §7.2).
 *
 * Two banners are advice: the store is nearly full, and history entries were
 * removed early to make room. Each closes with X until it is next triggered,
 * and Settings can hide both for good. A save or upload the relay refused for
 * lack of room is a failure, so it is a popup and always shown.
 *
 * The service keeps all of it, not this page: a refusal from auto-watch, or
 * an eviction, most likely happened while no window was open. When the relay
 * names its operator's website, every one of them offers to open it.
 */

import type { SpaceBanner, SpaceFailure, SpaceView } from "../api";
import { bytes } from "../format";
import { Icon } from "./Icon";
import { Modal } from "./Modal";

/** operatorHost is the host a link to the operator's website shows. */
export function operatorHost(url: string): string {
  try {
    return new URL(url).host;
  } catch {
    return url;
  }
}

/** OperatorLink opens the operator's website in a new tab. */
export function OperatorLink({ url, className }: { url: string; className?: string }) {
  return (
    <a className={className} href={url} target="_blank" rel="noopener noreferrer">
      Open operator website
    </a>
  );
}

const failures: Record<SpaceFailure, { title: string; body: string }> = {
  store_full: {
    title: "The store is full",
    body: "That did not fit in the group's store. Delete some items, or ask your relay operator for more room.",
  },
  relay_store_full: {
    title: "The relay's storage is full",
    body: "The group's store has room, but the relay's storage for every store together does not. Contact your relay operator.",
  },
  relay_full: {
    title: "The relay can't store this right now",
    body: "Try again later. If it keeps happening for a few minutes, contact your relay operator.",
  },
};

function plural(n: number, one: string, many: string): string {
  return `${n} ${n === 1 ? one : many}`;
}

/** storeUsage names how full the store is, by whichever limits it has. */
function storeUsage(space: SpaceView): string {
  const parts: string[] = [];
  if (space.store_max_bytes > 0) {
    parts.push(`${bytes(space.store_used_bytes)} of ${bytes(space.store_max_bytes)}`);
  }
  if (space.store_max_items > 0) {
    parts.push(`${space.store_used_items} of ${plural(space.store_max_items, "item", "items")}`);
  }
  return parts.join(", ");
}

export function SpaceNotices({
  space,
  operatorUrl,
  onDismiss,
}: {
  space: SpaceView;
  operatorUrl?: string;
  onDismiss: (banner: SpaceBanner) => void;
}) {
  const failure = space.failure ? failures[space.failure] : null;
  const link = operatorUrl ? <OperatorLink url={operatorUrl} className="action quiet" /> : null;
  const close = (banner: SpaceBanner) => (
    <button
      type="button"
      className="icon-button"
      aria-label="Close"
      title="Close"
      onClick={() => onDismiss(banner)}
    >
      <Icon name="close" size={16} />
    </button>
  );

  return (
    <>
      {space.store_nearly_full ? (
        <div className="notice warn space" role="status">
          <Icon name="store" />
          <span className="grow">
            Your store is almost full ({storeUsage(space)}). Delete some items, or ask your relay operator
            for more room.
          </span>
          {link}
          {close("store")}
        </div>
      ) : null}
      {space.evicted > 0 ? (
        <div className="notice warn space" role="status">
          <Icon name="history" />
          <span className="grow">
            History is full: the relay removed {plural(space.evicted, "older entry", "older entries")} early
            to make room for new ones. Ask your relay operator for more room.
          </span>
          {link}
          {close("evicted")}
        </div>
      ) : null}
      <Modal
        open={failure !== null}
        title={failure?.title ?? ""}
        onClose={() => onDismiss("failure")}
        footer={
          <>
            {operatorUrl ? <OperatorLink url={operatorUrl} className="action" /> : null}
            <button type="button" className="action primary" onClick={() => onDismiss("failure")}>
              OK
            </button>
          </>
        }
      >
        <p>{failure?.body}</p>
      </Modal>
    </>
  );
}
