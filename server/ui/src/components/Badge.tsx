import type { ReactNode } from "react";
import type { Group } from "../api";

export type BadgeKind = "open" | "used" | "good" | "bad" | "warn" | "unknown";

/** Badge is a small rounded label: a tag, a status, a health state. */
export function Badge({ kind, children }: { kind: BadgeKind; children: ReactNode }) {
  return <span className={`tag ${kind}`}>{children}</span>;
}

/** GroupBadges marks a group's tag, and whether it is stale or over its
 *  limit, after its name. */
export function GroupBadges({ group }: { group: Group }) {
  return (
    <>
      {group.tag && (
        <>
          {" "}
          <Badge kind="open">{group.tag}</Badge>
        </>
      )}
      {group.stale && (
        <>
          {" "}
          <Badge kind="warn">stale</Badge>
        </>
      )}
      {group.usage?.over_limit && (
        <>
          {" "}
          <Badge kind="bad">over limit</Badge>
        </>
      )}
    </>
  );
}

/** StoreBadge says whether a group's store is on, off, or being emptied. */
export function StoreBadge({ store }: { store: { enabled: boolean; emptying: boolean } }) {
  if (store.emptying) return <Badge kind="used">emptying</Badge>;
  return store.enabled ? <Badge kind="good">on</Badge> : <Badge kind="used">off</Badge>;
}
