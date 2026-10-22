import { useId, useState } from "react";
import { api, type LimitChange, type Tag } from "../api";
import { Empty, SectionHead } from "../components/Card";
import { useLimitForm } from "../components/LimitFields";
import { ErrorNotice, Problems } from "../components/Notice";
import { Cell, Table } from "../components/Table";
import { useAction, type Action } from "../hooks/useAction";
import { useDocumentTitle } from "../hooks/useDocumentTitle";
import { useResource } from "../hooks/useResource";
import { humanBytes, limitText } from "../lib/format";
import { groupsByTag } from "../lib/paths";
import { Link, useRouter } from "../router";

/** TagsPage creates tags, gives a tag a storage limit for every group under
 *  it, and deletes tags. Groups are put under a tag from their own page. */
export function TagsPage() {
  useDocumentTitle("Tags");
  const { data, error, version, reload } = useResource((signal) => api.tags(signal), []);
  const { notify } = useRouter();
  const action = useAction();

  const change = (save: () => Promise<unknown>, done: string) =>
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
          <h1>Tags</h1>
          <p className="lede">
            Tags put groups together, such as “family” or “friends”. A group has at most one tag. A tag can give every
            group under it a storage limit, in place of the server default ({limitText(data.default_limit_bytes)}); a
            group's own limit still wins over its tag's.
          </p>

          <CreateTagForm
            key={version}
            action={action}
            onCreate={(name) =>
              change(
                () => api.createTag(name),
                `Tag “${name.trim()}” created. Put groups under it from each group's page.`,
              )
            }
          />

          <SectionHead title="All tags">
            {data.tags.length > 0 && <span className="count">{data.tags.length} total</span>}
          </SectionHead>

          {data.tags.length > 0 ? (
            <>
              <Table
                columns={[
                  { label: "Tag" },
                  { label: "Groups" },
                  { label: "Devices" },
                  { label: "Blob storage" },
                  { label: "Storage limit" },
                  { label: "Delete", srOnly: true },
                ]}
              >
                {data.tags.map((t) => (
                  <TagRow
                    key={`${t.name}-${version}`}
                    tag={t}
                    action={action}
                    onLimit={(limit) => change(() => api.setTagLimit(t.name, limit), `Storage limit of “${t.name}” saved.`)}
                    onDelete={() => change(() => api.deleteTag(t.name), `Tag “${t.name}” deleted. Its groups are untagged now.`)}
                  />
                ))}
              </Table>
              <p className="hint spaced">
                0 means no per-group limit; the server's total limit ({limitText(data.total_limit_bytes)}) still
                applies. Unticking keeps the value for later.
              </p>
            </>
          ) : (
            <Empty>No tags yet. Create one above.</Empty>
          )}
        </>
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

function TagRow({
  tag,
  action,
  onLimit,
  onDelete,
}: {
  tag: Tag;
  action: Action;
  onLimit: (limit: LimitChange) => void;
  onDelete: () => void;
}) {
  const form = useLimitForm(tag.limit);
  return (
    <tr>
      <Cell label="Tag" className="name">
        <Link to={groupsByTag(tag.name)}>{tag.name}</Link>
      </Cell>
      <Cell label="Groups">{tag.groups}</Cell>
      <Cell label="Devices">{tag.devices}</Cell>
      <Cell label="Blob storage">{humanBytes(tag.used_bytes)}</Cell>
      <Cell label="Storage limit">
        <form
          className="limit-form"
          onSubmit={(event) => {
            event.preventDefault();
            onLimit(form.change);
          }}
        >
          <label className="check">
            <input type="checkbox" checked={form.enabled} onChange={(e) => form.setEnabled(e.target.checked)} /> Own
            limit
          </label>
          <input
            type="text"
            value={form.size}
            aria-label={`Storage limit for ${tag.name}`}
            placeholder="e.g. 5 GiB"
            onChange={(e) => form.setSize(e.target.value)}
          />
          <button type="submit" className="secondary small" disabled={action.busy}>
            Save
          </button>
        </form>
      </Cell>
      <Cell label="Delete" className="actions">
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
      </Cell>
    </tr>
  );
}
