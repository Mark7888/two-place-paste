import { useCallback, useState } from "react";
import type { Group, GroupList } from "../api";
import { Badge } from "../components/Badge";
import { Notice, Problems } from "../components/Notice";
import { SmartTable, type Column, type DefaultSort } from "../components/SmartTable";
import { Dash } from "../components/Dash";
import { UsageMeter } from "../components/UsageMeter";
import { useDocumentTitle } from "../hooks/useDocumentTitle";
import { ago, days, formatDate, humanBytes, percent } from "../lib/format";
import { groupPath } from "../lib/paths";
import { Link, useRouter, useSearchParams, withParams } from "../router";

/** filterName is how the heading says what the list is narrowed to. */
function filterName(tag: string, stale: boolean): string {
  const parts: string[] = [];
  if (tag === "-") parts.push("untagged groups");
  else if (tag !== "") parts.push(`tag “${tag}”`);
  if (stale) parts.push("stale only");
  return parts.join(", ");
}

const defaultSort: DefaultSort = { key: "storage", dir: "desc" };

const baseColumns: Column<Group>[] = [
  {
    key: "name",
    header: "Group",
    className: "name",
    render: (g) => (
      <span className="stack">
        <span>
          <Link to={groupPath(g.handle)}>{g.name}</Link>
          {/* On a phone the tag rides in the card's heading instead of a
              column of its own, which saves the card a line. */}
          {g.tag && (
            <span className="mobile-only">
              {" "}
              <Badge kind="open">{g.tag}</Badge>
            </span>
          )}
          {g.stale && (
            <>
              {" "}
              <Badge kind="warn">stale</Badge>
            </>
          )}
          {g.usage?.over_limit && (
            <>
              {" "}
              <Badge kind="bad">over limit</Badge>
            </>
          )}
        </span>
        <code className="muted">
          {g.short_id} · {formatDate(g.created_at)}
        </code>
      </span>
    ),
  },
  { key: "tag", header: "Tag", hideOnMobile: true, render: (g) => (g.tag ? <Badge kind="open">{g.tag}</Badge> : <Dash />) },
  { key: "devices", header: "Devices", firstDir: "desc", align: "end", render: (g) => g.devices },
  { key: "last_active", header: "Last active", firstDir: "desc", render: (g) => ago(Date.now(), g.last_seen, "never") },
  { key: "entries", header: "Entries", firstDir: "desc", align: "end", render: (g) => g.entries },
  { key: "oldest", header: "Oldest", firstDir: "desc", render: (g) => ago(Date.now(), g.oldest, "—") },
];

const storageColumn: Column<Group> = {
  key: "storage",
  header: "Blob storage",
  firstDir: "desc",
  className: "usage",
  render: (g) => <UsageCell group={g} />,
};

/** GroupsPage lists every group, heaviest first, searchable and sortable,
 *  and narrowed by tag (?tag=name, or ?tag=- for the untagged ones) or to
 *  the stale ones (?stale=1). */
export function GroupsPage() {
  useDocumentTitle("Groups");
  const { location, navigate } = useRouter();
  const [params, setParams] = useSearchParams();
  const tag = params.get("tag") ?? "";
  const staleOnly = params.get("stale") === "1";
  const [meta, setMeta] = useState<GroupList | null>(null);
  const name = filterName(tag, staleOnly);

  // Until the first answer says whether storage is accounted, assume it is:
  // a column that appears is less jarring than one that vanishes.
  const columns = meta && !meta.has_usage ? baseColumns : [...baseColumns, storageColumn];
  const open = useCallback((g: Group) => navigate(groupPath(g.handle)), [navigate]);

  // A filter starts its list from the first page.
  const filterLink = (patch: Record<string, string | null>) => withParams(location, { ...patch, page: null });

  return (
    <>
      <h1>
        Groups
        {name && (
          <>
            {" "}
            <span className="muted">· {name}</span>
          </>
        )}
      </h1>
      <p className="lede">
        Every group on this relay, heaviest first. Open one to rename it, tag it, set its storage limit, see its
        devices, or purge or delete it.
      </p>

      <Problems list={meta?.problems} />
      {meta && meta.stale_count > 0 && !staleOnly && (
        <StaleNotice
          count={meta.stale_count}
          afterDays={meta.stale_after_days}
          showOnly={filterLink({ stale: "1", tag: null })}
        />
      )}

      <SmartTable<Group, GroupList>
        id="groups"
        caption="Groups"
        noun="groups"
        endpoint="/admin/api/groups"
        filters={{ tag, stale: staleOnly ? "1" : "" }}
        columns={columns}
        rowKey={(g) => g.handle}
        defaultSort={defaultSort}
        searchPlaceholder="Search groups, tags or devices"
        onRowClick={open}
        onResponse={setMeta}
        toolbar={
          <div className="table-filters">
            {meta && meta.tags.length > 0 && (
              <nav className="chips" aria-label="Filter by tag">
                <Link stay to={filterLink({ tag: null })} aria-current={tag === "" ? "page" : undefined}>
                  All <span className="muted">{meta.group_count}</span>
                </Link>
                {meta.tags.map((t) => (
                  <Link
                    stay
                    key={t.filter}
                    to={filterLink({ tag: t.filter })}
                    aria-current={t.filter === tag ? "page" : undefined}
                  >
                    {t.name} <span className="muted">{t.groups}</span>
                  </Link>
                ))}
                <Link className="manage" to="/admin/tags">
                  Manage tags
                </Link>
              </nav>
            )}
            <label className="check">
              <input
                type="checkbox"
                checked={staleOnly}
                onChange={(e) => setParams({ stale: e.target.checked ? "1" : null, page: null })}
              />{" "}
              Stale only
            </label>
          </div>
        }
        emptyText={
          tag || staleOnly ? (
            <>
              No groups here.{" "}
              <Link stay to={filterLink({ tag: null, stale: null })}>
                Show all groups
              </Link>
              .
            </>
          ) : (
            <>
              No groups yet. <Link to="/admin/tokens">Generate a creation token</Link> and hand it to the first device.
            </>
          )
        }
      />
    </>
  );
}

function StaleNotice({ count, afterDays, showOnly }: { count: number; afterDays: number; showOnly: string }) {
  const one = count === 1;
  return (
    <Notice kind="warn">
      {count} {one ? "group has" : "groups have"} had no device connect for more than {days(afterDays)}.{" "}
      {one ? "It is marked below; open it to delete it." : "They are marked below; open one to delete it."}{" "}
      <Link stay to={showOnly}>
        Show only stale groups
      </Link>
    </Notice>
  );
}

function UsageCell({ group }: { group: Group }) {
  const used = group.usage?.used_bytes ?? 0;
  const limit = group.limit_bytes;
  return (
    <span className="stack">
      <span>
        {humanBytes(used)}
        {limit > 0 && (
          <>
            {" "}
            <span className="muted">· {percent(used, limit)}%</span>
          </>
        )}
      </span>
      <UsageMeter used={used} limit={limit} label={`Blob storage used by ${group.name}`} />
    </span>
  );
}
