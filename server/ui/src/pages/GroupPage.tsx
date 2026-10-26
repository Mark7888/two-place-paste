import { useId, useState } from "react";
import { api, type Device, type GroupDetail, type GroupStore, type LimitChange } from "../api";
import { Badge, GroupBadges } from "../components/Badge";
import { SectionHead, StatCard } from "../components/Card";
import { useLimitForm } from "../components/LimitFields";
import { ErrorNotice, Problems } from "../components/Notice";
import { SmartTable, type Column } from "../components/SmartTable";
import { UsageMeter } from "../components/UsageMeter";
import { useAction, type Action } from "../hooks/useAction";
import { useDocumentTitle } from "../hooks/useDocumentTitle";
import { useResource } from "../hooks/useResource";
import { ago, formatDate, humanBytes, limitText, plural } from "../lib/format";
import { groupPath, groupsByTag } from "../lib/paths";
import { Link, useRouter } from "../router";

const limitSources = { group: "this group", tag: "its tag", default: "the server default" } as const;

/** GroupPage is one group: what it holds, its name, tag and storage limit,
 *  its store, its devices, and the way to purge or delete it. Like every
 *  screen it shows counts, sizes and device names, never content. */
export function GroupPage({ handle }: { handle: string }) {
  const { data, error, version, reload } = useResource((signal) => api.group(handle, signal), [handle]);
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
            onSave={(name) => change(() => api.renameGroup(handle, name), "Group renamed. Its devices show the new name.")}
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
            onSave={(tag) => change(() => api.setGroupTag(handle, tag), "Tag saved.")}
          />

          <SectionHead title="Storage limit" />
          <LimitForm
            key={`limit-${version}`}
            data={data}
            action={action}
            onSave={(limit) => change(() => api.setGroupLimit(handle, limit), "Storage limit saved.")}
          />

          {data.store && (
            <StoreSection
              key={`store-${version}`}
              store={data.store}
              action={action}
              onSwitch={(enabled) =>
                change(
                  () => api.setGroupStore(handle, enabled),
                  enabled
                    ? "Store turned on. The group's devices can save items now."
                    : "Store turned off. Its items are kept until it is turned on again or purged.",
                )
              }
              onSaveLimit={(limit) => change(() => api.setGroupStoreLimit(handle, limit), "Store limit saved.")}
            />
          )}

          <Devices data={data} />
          <DangerZone
            handle={handle}
            canPurge={data.can_purge}
            canPurgeStore={!!data.store && data.store.used_items > 0 && !data.store.emptying}
          />
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
        <StatCard title="Blob storage">
          <p className="figure">
            {humanBytes(g.usage.used_bytes)} <span className="muted nowrap">of {limitText(g.limit_bytes)}</span>
          </p>
          <UsageMeter used={g.usage.used_bytes} limit={g.limit_bytes} label="Blob storage used" />
          <p className="sub">
            limit from {limitSources[g.limit_source]}
            {g.usage.over_limit && " · the next upload evicts its oldest entries down to the limit"}
          </p>
        </StatCard>
      )}
      {data.store && <StoreStat store={data.store} />}
    </div>
  );
}

/** StoreStat is the store's usage beside the blob storage at the top of the
 *  page; the Store section further down holds its switch and limit. */
function StoreStat({ store }: { store: GroupStore }) {
  return (
    <StatCard title="Store">
      <p className="figure">
        {humanBytes(store.used_bytes)} <span className="muted nowrap">of {limitText(store.limit_bytes)}</span>
      </p>
      <UsageMeter used={store.used_bytes} limit={store.limit_bytes} label="Store used" />
      <p className="sub">
        {store.used_items} of {store.max_items} {plural(store.max_items, "item", "items")} · limit from{" "}
        {limitSources[store.limit_source]}
        {store.emptying ? " · emptying" : !store.enabled && " · off"}
      </p>
    </StatCard>
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

/** StoreSection is the group's store (SPEC §4.6): off until the admin turns
 *  it on, its usage against its own limits, and its limit override. The
 *  store never evicts: a save that does not fit is refused. */
function StoreSection({
  store,
  action,
  onSwitch,
  onSaveLimit,
}: {
  store: GroupStore;
  action: Action;
  onSwitch: (enabled: boolean) => void;
  onSaveLimit: (limit: LimitChange) => void;
}) {
  const form = useLimitForm(store.own_limit);
  const id = useId();
  const items = `${store.used_items} ${plural(store.used_items, "item", "items")}`;
  return (
    <>
      <SectionHead title="Store">
        {store.emptying ? (
          <Badge kind="used">emptying</Badge>
        ) : store.enabled ? (
          <Badge kind="good">on</Badge>
        ) : (
          <Badge kind="used">off</Badge>
        )}
      </SectionHead>
      <div className="card">
        <div className="form-row spread">
          <div>
            <strong>{store.enabled ? "The store is on" : "The store is off"}</strong>
            <br />
            <span className="muted">
              {store.emptying
                ? "It is being emptied in the background, and closed to its devices until then."
                : store.enabled
                ? "The group's devices can save items that do not expire, beside their 24-hour history."
                : store.used_items > 0
                  ? `It holds ${items} (${humanBytes(store.used_bytes)}), kept while it is off.`
                  : "Turn it on to let the group's devices save items that do not expire."}
            </span>
          </div>
          <button
            type="button"
            className={store.enabled ? "secondary" : undefined}
            disabled={action.busy}
            onClick={() => {
              if (
                store.enabled &&
                !window.confirm(
                  `Turn the store off? Its devices can no longer read or save items. The ${items} it holds are kept until it is turned on again or purged.`,
                )
              ) {
                return;
              }
              onSwitch(!store.enabled);
            }}
          >
            {store.enabled ? "Turn store off" : "Turn store on"}
          </button>
        </div>
      </div>
      <form
        className="card"
        onSubmit={(event) => {
          event.preventDefault();
          onSaveLimit(form.change);
        }}
      >
        <label className="check">
          <input type="checkbox" checked={form.enabled} onChange={(e) => form.setEnabled(e.target.checked)} /> Give
          this group's store its own limit
        </label>
        <div className="form-row">
          <div className="field">
            <label htmlFor={id}>Store limit</label>
            <input
              type="text"
              id={id}
              value={form.size}
              placeholder="e.g. 100 MiB"
              inputMode="text"
              onChange={(e) => form.setSize(e.target.value)}
            />
          </div>
          <button type="submit" disabled={action.busy}>
            Save store limit
          </button>
        </div>
        <span className="hint">
          Overrides the tag's store limit{store.tag_limit_bytes !== null && ` (${limitText(store.tag_limit_bytes)})`}{" "}
          and the server default ({limitText(store.default_limit_bytes)}). 0 means no limit of its own. An item can
          be up to {humanBytes(store.max_item_bytes)}. Separate from
          the storage limit above. Lowering it deletes nothing: the store never evicts, so new saves are refused until
          it fits.
        </span>
      </form>
    </>
  );
}

const deviceColumns: Column<Device>[] = [
  {
    key: "name",
    header: "Device",
    className: "name",
    sortValue: (d) => d.name,
    render: (d) => (
      <span className="stack">
        {d.name}
        <code className="muted">{d.short_id}</code>
      </span>
    ),
  },
  {
    key: "paired",
    header: "Paired",
    firstDir: "desc",
    sortValue: (d) => Date.parse(d.created_at),
    render: (d) => formatDate(d.created_at),
  },
  {
    key: "last_seen",
    header: "Last seen",
    firstDir: "desc",
    sortValue: (d) => (d.last_seen ? Date.parse(d.last_seen) : 0),
    render: (d) => ago(Date.now(), d.last_seen, "never"),
  },
  {
    key: "status",
    header: "Status",
    firstDir: "desc",
    sortValue: (d) => (d.online ? 1 : 0),
    render: (d) => (d.online ? <Badge kind="good">online</Badge> : <Badge kind="used">offline</Badge>),
  },
];

function Devices({ data }: { data: GroupDetail }) {
  return (
    <>
      <SectionHead title="Devices">
        <span className="count">{data.group.devices} total</span>
      </SectionHead>
      <SmartTable<Device>
        caption="Devices"
        rows={data.devices}
        columns={deviceColumns}
        rowKey={(d) => d.short_id + d.created_at}
        defaultSort={{ key: "paired", dir: "asc" }}
        emptyText="This group has no devices left."
      />
      {data.devices.length > 0 && (
        <p className="hint spaced">
          Devices are added by pairing and removed from another device of the group, which re-keys it. The server
          cannot re-key, so it cannot remove one.
        </p>
      )}
    </>
  );
}

function DangerZone({
  handle,
  canPurge,
  canPurgeStore,
}: {
  handle: string;
  canPurge: boolean;
  canPurgeStore: boolean;
}) {
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
            <Link className="button secondary" to={`${groupPath(handle)}/purge`}>
              Purge entries…
            </Link>
          </div>
        )}
        {canPurgeStore && (
          <div className="form-row spread">
            <div>
              <strong>Purge store</strong>
              <br />
              <span className="muted">Delete every item in the store. The store stays on.</span>
            </div>
            <Link className="button secondary" to={`${groupPath(handle)}/purge-store`}>
              Purge store…
            </Link>
          </div>
        )}
        <div className="form-row spread">
          <div>
            <strong>Delete group</strong>
            <br />
            <span className="muted">Remove the group, its devices, entries and store. Cannot be undone.</span>
          </div>
          <Link className="button danger" to={`${groupPath(handle)}/delete`}>
            Delete group…
          </Link>
        </div>
      </div>
    </>
  );
}
