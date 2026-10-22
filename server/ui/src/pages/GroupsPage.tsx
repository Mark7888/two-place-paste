import { api, type Group, type GroupList } from "../api";
import { GroupBadges } from "../components/Badge";
import { Empty } from "../components/Card";
import { ErrorNotice, Notice, Problems } from "../components/Notice";
import { Cell, Table } from "../components/Table";
import { UsageMeter } from "../components/UsageMeter";
import { useDocumentTitle } from "../hooks/useDocumentTitle";
import { useResource } from "../hooks/useResource";
import { ago, days, formatDate, humanBytes, percent } from "../lib/format";
import { groupPath, groupsByTag } from "../lib/paths";
import { Link, useQueryParam } from "../router";

/** filterName is how the heading says what the list is narrowed to. */
function filterName(filter: string): string {
  if (filter === "") return "";
  if (filter === "-") return "untagged groups";
  return `tag “${filter}”`;
}

/** GroupsPage lists every group, heaviest first, or those under one tag
 *  (?tag=name, or ?tag=- for the untagged ones). */
export function GroupsPage() {
  useDocumentTitle("Groups");
  const filter = useQueryParam("tag");
  const { data, error } = useResource((signal) => api.groups(filter, signal), [filter]);
  const name = filterName(filter);

  return (
    <>
      <ErrorNotice error={error?.message} />
      {data && (
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

          <Problems list={data.problems} />
          <TagFilter data={data} />
          {data.stale_count > 0 && <StaleNotice count={data.stale_count} afterDays={data.stale_after_days} />}
          <GroupTable data={data} />
        </>
      )}
    </>
  );
}

function TagFilter({ data }: { data: GroupList }) {
  if (data.tags.length === 0) return null;
  return (
    <nav className="chips" aria-label="Filter by tag">
      <Link to="/admin/groups" aria-current={data.filter === "" ? "page" : undefined}>
        All <span className="muted">{data.group_count}</span>
      </Link>
      {data.tags.map((t) => (
        <Link key={t.filter} to={groupsByTag(t.filter)} aria-current={t.filter === data.filter ? "page" : undefined}>
          {t.name} <span className="muted">{t.groups}</span>
        </Link>
      ))}
      <Link className="manage" to="/admin/tags">
        Manage tags
      </Link>
    </nav>
  );
}

function StaleNotice({ count, afterDays }: { count: number; afterDays: number }) {
  const one = count === 1;
  return (
    <Notice kind="warn">
      {count} {one ? "group has" : "groups have"} had no device connect for more than {days(afterDays)}.{" "}
      {one ? "It is marked below; open it to delete it." : "They are marked below; open one to delete it."}
    </Notice>
  );
}

function GroupTable({ data }: { data: GroupList }) {
  if (data.groups.length === 0) {
    return data.filter ? (
      <Empty>
        No groups here. <Link to="/admin/groups">Show all groups</Link>.
      </Empty>
    ) : (
      <Empty>
        No groups yet. <Link to="/admin/tokens">Generate a creation token</Link> and hand it to the first device.
      </Empty>
    );
  }
  const now = Date.now();
  return (
    <Table
      columns={[
        { label: "Group" },
        { label: "Devices" },
        { label: "Last active" },
        { label: "Entries" },
        { label: "Oldest" },
        data.has_usage && { label: "Blob storage" },
      ]}
    >
      {data.groups.map((g) => (
        <tr key={g.id}>
          <Cell label="Group" className="name">
            <span className="stack">
              <span>
                <Link to={groupPath(g.id)}>{g.name}</Link>
                <GroupBadges group={g} />
              </span>
              <code className="muted">
                {g.short_id} · {formatDate(g.created_at)}
              </code>
            </span>
          </Cell>
          <Cell label="Devices">{g.devices}</Cell>
          <Cell label="Last active">{ago(now, g.last_seen, "never")}</Cell>
          <Cell label="Entries">{g.entries}</Cell>
          <Cell label="Oldest">{ago(now, g.oldest, "—")}</Cell>
          {g.usage && <UsageCell group={g} used={g.usage.used_bytes} />}
        </tr>
      ))}
    </Table>
  );
}

function UsageCell({ group, used }: { group: Group; used: number }) {
  const limit = group.limit_bytes;
  return (
    <Cell label="Blob storage" className="usage">
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
    </Cell>
  );
}
