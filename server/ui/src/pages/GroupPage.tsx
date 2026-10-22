import { useId, useState } from "react";
import { api, type GroupDetail } from "../api";
import { Badge, GroupBadges } from "../components/Badge";
import { Empty, SectionHead, StatCard } from "../components/Card";
import { useLimitForm } from "../components/LimitFields";
import { ErrorNotice, Problems } from "../components/Notice";
import { Cell, Table } from "../components/Table";
import { UsageMeter } from "../components/UsageMeter";
import { useAction, type Action } from "../hooks/useAction";
import { useDocumentTitle } from "../hooks/useDocumentTitle";
import { useResource } from "../hooks/useResource";
import { ago, formatDate, humanBytes, limitText } from "../lib/format";
import { groupPath, groupsByTag } from "../lib/paths";
import { Link, useRouter } from "../router";

const limitSources = { group: "this group", tag: "its tag", default: "the server default" } as const;

/** GroupPage is one group: what it holds, its name, tag and storage limit,
 *  its devices, and the way to purge or delete it. Like every screen it shows
 *  counts, sizes and device names, never content. */
export function GroupPage({ id }: { id: string }) {
  const { data, error, version, reload } = useResource((signal) => api.group(id, signal), [id]);
  const { notify } = useRouter();
  const action = useAction();
  useDocumentTitle(data?.group.name ?? (error ? "No such group" : "Group"));

  // Every change reloads the page's data and confirms itself at the top, as
  // a form's redirect used to.
  const change = (save: () => Promise<void>, done: string) =>
    action.run(async () => {
      await save();
      notify(done);
      reload();
    });

  return (
    <>
      <ErrorNotice error={action.error ?? error?.message} />
      {data && (
        <>
          <Problems list={data.problems} />
          <Header data={data} />
          <Stats data={data} />

          <SectionHead title="Name" />
          <RenameForm
            key={`name-${version}`}
            name={data.group.name}
            action={action}
            onSave={(name) => change(() => api.renameGroup(id, name), "Group renamed. Its devices show the new name.")}
          />

          <SectionHead title="Tag">
            <Link className="count" to="/admin/tags">
              Manage tags
            </Link>
          </SectionHead>
          <TagForm
            key={`tag-${version}`}
            data={data}
            action={action}
            onSave={(tag) => change(() => api.setGroupTag(id, tag), "Tag saved.")}
          />

          <SectionHead title="Storage limit" />
          <LimitForm
            key={`limit-${version}`}
            data={data}
            action={action}
            onSave={(limit) => change(() => api.setGroupLimit(id, limit), "Storage limit saved.")}
          />

          <Devices data={data} />
          <DangerZone id={id} canPurge={data.can_purge} />
        </>
      )}
    </>
  );
}

function Header({ data }: { data: GroupDetail }) {
  const g = data.group;
  return (
    <>
      <p className="crumbs">
        <Link to="/admin/groups">Groups</Link> /{" "}
        {g.tag && (
          <>
            <Link to={groupsByTag(g.tag)}>{g.tag}</Link> /{" "}
          </>
        )}
        group
      </p>
      <h1>
        {g.name}
        <GroupBadges group={g} />
      </h1>
      <p className="lede">
        <code>{g.short_id}</code> · created {formatDate(g.created_at)}
      </p>
    </>
  );
}

function Stats({ data }: { data: GroupDetail }) {
  const g = data.group;
  const now = Date.now();
  return (
    <div className="stats">
      <StatCard title="Devices">
        <p className="figure">{g.devices}</p>
        <p className="sub">last active {ago(now, g.last_seen, "never")}</p>
      </StatCard>
      <StatCard title="Entries">
        <p className="figure">{g.entries}</p>
        <p className="sub">oldest {ago(now, g.oldest, "—")}</p>
      </StatCard>
      {g.usage && (
        <StatCard title="Blob storage" wide>
          <p className="figure">
            {humanBytes(g.usage.used_bytes)} <span className="muted">of {limitText(g.limit_bytes)}</span>
          </p>
          <UsageMeter used={g.usage.used_bytes} limit={g.limit_bytes} label="Blob storage used" />
          <p className="sub">
            limit from {limitSources[g.limit_source]}
            {g.usage.over_limit && " · the next upload evicts its oldest entries down to the limit"}
          </p>
        </StatCard>
      )}
    </div>
  );
}

function RenameForm({ name: stored, action, onSave }: { name: string; action: Action; onSave: (name: string) => void }) {
  const [name, setName] = useState(stored);
  const id = useId();
  return (
    <form
      className="card"
      onSubmit={(event) => {
        event.preventDefault();
        onSave(name);
      }}
    >
      <div className="form-row">
        <div className="field">
          <label htmlFor={id}>Display name</label>
          <input type="text" id={id} maxLength={120} required value={name} onChange={(e) => setName(e.target.value)} />
        </div>
        <button type="submit" disabled={action.busy}>
          Rename
        </button>
      </div>
      <span className="hint">The group's devices show this name. Connected ones update straight away.</span>
    </form>
  );
}

function TagForm({ data, action, onSave }: { data: GroupDetail; action: Action; onSave: (tag: string) => void }) {
  const [tag, setTag] = useState(data.group.tag);
  const id = useId();
  return (
    <form
      className="card"
      onSubmit={(event) => {
        event.preventDefault();
        onSave(tag);
      }}
    >
      <div className="form-row">
        <div className="field">
          <label htmlFor={id}>Tag</label>
          <select id={id} value={tag} onChange={(e) => setTag(e.target.value)}>
            <option value="">No tag</option>
            {data.tags.map((t) => (
              <option key={t} value={t}>
                {t}
              </option>
            ))}
          </select>
        </div>
        <button type="submit" disabled={action.busy}>
          Save tag
        </button>
      </div>
      {data.tags.length === 0 && (
        <span className="hint">
          There are no tags yet. <Link to="/admin/tags">Create one</Link> first.
        </span>
      )}
    </form>
  );
}

function LimitForm({
  data,
  action,
  onSave,
}: {
  data: GroupDetail;
  action: Action;
  onSave: (limit: { enabled: boolean; size: string }) => void;
}) {
  const form = useLimitForm(data.own_limit);
  const id = useId();
  return (
    <form
      className="card"
      onSubmit={(event) => {
        event.preventDefault();
        onSave(form.change);
      }}
    >
      <label className="check">
        <input type="checkbox" checked={form.enabled} onChange={(e) => form.setEnabled(e.target.checked)} /> Give
        this group its own limit
      </label>
      <div className="form-row">
        <div className="field">
          <label htmlFor={id}>Limit</label>
          <input
            type="text"
            id={id}
            value={form.size}
            placeholder="e.g. 5 GiB"
            inputMode="text"
            onChange={(e) => form.setSize(e.target.value)}
          />
        </div>
        <button type="submit" disabled={action.busy}>
          Save limit
        </button>
      </div>
      <span className="hint">
        Overrides the tag's limit{data.tag_limit_bytes !== null && ` (${limitText(data.tag_limit_bytes)})`} and the
        server default ({limitText(data.default_limit_bytes)}). 0 means no limit of its own. Unticking keeps the value
        for later. Lowering it deletes nothing now: the group's next upload removes its oldest entries until it fits.
      </span>
    </form>
  );
}

function Devices({ data }: { data: GroupDetail }) {
  const now = Date.now();
  return (
    <>
      <SectionHead title="Devices">
        <span className="count">{data.group.devices} total</span>
      </SectionHead>
      {data.devices.length > 0 ? (
        <>
          <Table columns={[{ label: "Device" }, { label: "Paired" }, { label: "Last seen" }, { label: "Status" }]}>
            {data.devices.map((d) => (
              <tr key={d.short_id + d.created_at}>
                <Cell label="Device" className="name">
                  <span className="stack">
                    {d.name}
                    <code className="muted">{d.short_id}</code>
                  </span>
                </Cell>
                <Cell label="Paired">{formatDate(d.created_at)}</Cell>
                <Cell label="Last seen">{ago(now, d.last_seen, "never")}</Cell>
                <Cell label="Status">
                  {d.online ? <Badge kind="good">online</Badge> : <Badge kind="used">offline</Badge>}
                </Cell>
              </tr>
            ))}
          </Table>
          <p className="hint spaced">
            Devices are added by pairing and removed from another device of the group, which re-keys it. The server
            cannot re-key, so it cannot remove one.
          </p>
        </>
      ) : (
        <Empty>This group has no devices left.</Empty>
      )}
    </>
  );
}

function DangerZone({ id, canPurge }: { id: string; canPurge: boolean }) {
  return (
    <>
      <SectionHead title="Danger zone" />
      <div className="card danger-zone">
        {canPurge && (
          <div className="form-row spread">
            <div>
              <strong>Purge entries</strong>
              <br />
              <span className="muted">Delete everything the group holds now. Devices keep working.</span>
            </div>
            <Link className="button secondary" to={`${groupPath(id)}/purge`}>
              Purge entries…
            </Link>
          </div>
        )}
        <div className="form-row spread">
          <div>
            <strong>Delete group</strong>
            <br />
            <span className="muted">Remove the group, its devices and entries. Cannot be undone.</span>
          </div>
          <Link className="button danger" to={`${groupPath(id)}/delete`}>
            Delete group…
          </Link>
        </div>
      </div>
    </>
  );
}
