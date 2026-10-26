import { useId, useState } from "react";
import { api, type Limit, type LimitChange, type Tag, type TagList } from "../api";
import { useLimitForm } from "../components/LimitFields";
import { ErrorNotice, Problems } from "../components/Notice";
import { SmartTable, useTableController, type Column, type DefaultSort } from "../components/SmartTable";
import { useAction, type Action } from "../hooks/useAction";
import { useDocumentTitle } from "../hooks/useDocumentTitle";
import { humanBytes, limitText } from "../lib/format";
import { groupsByTag } from "../lib/paths";
import { Link, useRouter } from "../router";

const defaultSort: DefaultSort = { key: "name", dir: "asc" };

/** TagsPage creates tags, gives a tag a storage limit for every group under
 *  it, and deletes tags. Groups are put under a tag from their own page. */
export function TagsPage() {
  useDocumentTitle("Tags");
  const [meta, setMeta] = useState<TagList | null>(null);
  const [created, setCreated] = useState(0);
  const { notify } = useRouter();
  const action = useAction();
  const table = useTableController();

  // Every change confirms itself at the top and reloads the table, which
  // keeps its search, sort and page.
  const change = (save: () => Promise<unknown>, done: string) =>
    action.run(async () => {
      await save();
      notify(done);
      table.refresh();
    });

  // The cells hold forms that act through this screen, so the columns are
  // made here rather than once for the module.
  const columns: Column<Tag>[] = [
    { key: "name", header: "Tag", className: "name", render: (t) => <Link to={groupsByTag(t.name)}>{t.name}</Link> },
    { key: "groups", header: "Groups", firstDir: "desc", align: "end", render: (t) => t.groups },
    { key: "devices", header: "Devices", firstDir: "desc", align: "end", render: (t) => t.devices },
    { key: "storage", header: "Blob storage", firstDir: "desc", render: (t) => humanBytes(t.used_bytes) },
    ...(meta?.has_store
      ? [
          {
            key: "store",
            header: "Store",
            firstDir: "desc" as const,
            render: (t: Tag) => (
              <span className="stack">
                <span>{humanBytes(t.store_bytes)}</span>
                <span className="muted nowrap">
                  on in {t.store_groups_on} of {t.groups}
                </span>
              </span>
            ),
          },
        ]
      : []),
    {
      key: "limit",
      header: "Storage limit",
      firstDir: "desc",
      className: "wide",
      // Keyed by the stored limit, so a save shows what the relay stored
      // rather than what was typed.
      render: (t) => (
        <TagLimitForm
          key={`${t.limit.enabled}-${t.limit.bytes}`}
          tag={t}
          stored={t.limit}
          label="Storage limit"
          placeholder="e.g. 5 GiB"
          action={action}
          onSave={(limit) => change(() => api.setTagLimit(t.name, limit), `Storage limit of “${t.name}” saved.`)}
        />
      ),
    },
    ...(meta?.has_store
      ? [
          {
            key: "store_limit",
            header: "Store limit",
            className: "wide",
            render: (t: Tag) => (
              <TagLimitForm
                key={`${t.store_limit.enabled}-${t.store_limit.bytes}`}
                tag={t}
                stored={t.store_limit}
                label="Store limit"
                placeholder="e.g. 100 MiB"
                action={action}
                onSave={(limit) =>
                  change(() => api.setTagStoreLimit(t.name, limit), `Store limit of “${t.name}” saved.`)
                }
              />
            ),
          },
        ]
      : []),
    {
      key: "delete",
      header: "Delete",
      headerHidden: true,
      className: "actions",
      render: (t) => (
        <DeleteTagButton
          tag={t}
          action={action}
          onDelete={() => change(() => api.deleteTag(t.name), `Tag “${t.name}” deleted. Its groups are untagged now.`)}
        />
      ),
    },
  ];

  return (
    <>
      <ErrorNotice error={action.error} />
      <Problems list={meta?.problems} />
      <h1>Tags</h1>
      <p className="lede">
        Tags put groups together, such as “family” or “friends”. A group has at most one tag. A tag can give every group
        under it a storage limit, in place of the server default
        {meta ? ` (${limitText(meta.default_limit_bytes)})` : ""}; a group's own limit still wins over its tag's.
      </p>

      <CreateTagForm
        key={created}
        action={action}
        onCreate={async (name) => {
          const ok = await change(
            () => api.createTag(name),
            `Tag “${name.trim()}” created. Put groups under it from each group's page.`,
          );
          if (ok) setCreated((n) => n + 1);
        }}
      />

      <SmartTable<Tag, TagList>
        id="tags"
        caption="Tags"
        noun="tags"
        endpoint="/admin/api/tags"
        columns={columns}
        rowKey={(t) => t.name}
        defaultSort={defaultSort}
        searchPlaceholder="Search tags"
        onResponse={setMeta}
        controller={table}
        emptyText="No tags yet. Create one above."
      />
      {meta && (
        <p className="hint spaced">
          0 means no per-group limit; the server's total limit ({limitText(meta.total_limit_bytes)}) still applies.
          Unticking keeps the value for later.
          {meta.has_store && (
            <>
              {" "}
              A store limit works the same way for the groups' stores, in place of the server default (
              {limitText(meta.default_store_limit_bytes)}), within the total store limit (
              {limitText(meta.total_store_limit_bytes)}). Whether a group has a store at all is set on its own page.
            </>
          )}
        </p>
      )}
    </>
  );
}

function CreateTagForm({ action, onCreate }: { action: Action; onCreate: (name: string) => void }) {
  const [name, setName] = useState("");
  const id = useId();
  return (
    <form
      className="card"
      onSubmit={(event) => {
        event.preventDefault();
        onCreate(name);
      }}
    >
      <div className="form-row">
        <div className="field">
          <label htmlFor={id}>New tag</label>
          <input
            type="text"
            id={id}
            maxLength={40}
            placeholder="e.g. family"
            required
            value={name}
            onChange={(e) => setName(e.target.value)}
          />
        </div>
        <button type="submit" disabled={action.busy}>
          Create tag
        </button>
      </div>
      <span className="hint">
        Letters, digits, spaces, dots, dashes and underscores. Put groups under it from each group's page.
      </span>
    </form>
  );
}

function TagLimitForm({
  tag,
  stored,
  label,
  placeholder,
  action,
  onSave,
}: {
  tag: Tag;
  stored: Limit;
  label: string;
  placeholder: string;
  action: Action;
  onSave: (limit: LimitChange) => void;
}) {
  const form = useLimitForm(stored);
  return (
    <form
      className="limit-form"
      onSubmit={(event) => {
        event.preventDefault();
        onSave(form.change);
      }}
    >
      <label className="check">
        <input type="checkbox" checked={form.enabled} onChange={(e) => form.setEnabled(e.target.checked)} /> Own limit
      </label>
      <input
        type="text"
        value={form.size}
        aria-label={`${label} for ${tag.name}`}
        placeholder={placeholder}
        onChange={(e) => form.setSize(e.target.value)}
      />
      <button type="submit" className="secondary small" disabled={action.busy}>
        Save
      </button>
    </form>
  );
}

function DeleteTagButton({ tag, action, onDelete }: { tag: Tag; action: Action; onDelete: () => void }) {
  return (
    <form
      className="inline-form"
      onSubmit={(event) => {
        event.preventDefault();
        if (
          window.confirm(
            `Delete the tag “${tag.name}”? Its groups stay, untagged, and go back to their own limit or the default.`,
          )
        ) {
          onDelete();
        }
      }}
    >
      <button type="submit" className="danger small" disabled={action.busy}>
        Delete
      </button>
    </form>
  );
}
