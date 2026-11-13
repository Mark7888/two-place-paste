import { useCallback, useId, useState } from "react";
import { api, type ApiKey, type ApiKeyList, type ApiScope, type NewApiKey } from "../api";
import { ApiKeyDialog, type ShownApiKey } from "../components/ApiKeyDialog";
import { Badge } from "../components/Badge";
import { Dash } from "../components/Dash";
import { ErrorNotice } from "../components/Notice";
import { SmartTable, useTableController, type Column, type DefaultSort } from "../components/SmartTable";
import { useAction, type Action } from "../hooks/useAction";
import { useDocumentTitle } from "../hooks/useDocumentTitle";
import { ago, formatDateTime } from "../lib/format";
import { apiDocsURL } from "../lib/paths";

const defaultSort: DefaultSort = { key: "created", dir: "desc" };

/** What each scope allows, as the create form explains it. */
const scopeText: Record<ApiScope, string> = {
  read: "Read the dashboard, groups, tags, settings and audit log.",
  tokens: "List, create and revoke creation tokens, and read their links.",
  admin: "Everything, including deleting groups. Includes read and tokens.",
};

const lifetimes: { days: number; label: string }[] = [
  { days: 30, label: "30 days" },
  { days: 90, label: "90 days" },
  { days: 365, label: "1 year" },
  { days: 0, label: "Never" },
];

/** ApiKeysPage creates, lists and revokes the keys of the admin REST API
 *  (/api/v1/). A key is shown once, when it is created; the relay keeps only
 *  a hash. Keys are made here or with `tpp apikey`, never through the API. */
export function ApiKeysPage() {
  useDocumentTitle("API keys");
  const [meta, setMeta] = useState<ApiKeyList | null>(null);
  const [created, setCreated] = useState(0);
  const [shown, setShown] = useState<ShownApiKey | null>(null);
  const close = useCallback(() => setShown(null), []);
  const action = useAction();
  const table = useTableController();

  const create = (key: NewApiKey) =>
    action.run(async () => {
      const k = await api.createApiKey(key);
      setCreated((n) => n + 1);
      table.refresh();
      setShown({ name: k.api_key.name, key: k.key });
    });

  const revoke = (k: ApiKey) =>
    action.run(async () => {
      await api.revokeApiKey(k.id);
      table.refresh();
    });

  const columns: Column<ApiKey>[] = [
    {
      key: "name",
      header: "Name",
      className: "name",
      render: (k) => (
        <span className="stack">
          <span>
            {k.name}{" "}
            {k.scopes.map((s) => (
              <span key={s}>
                <Badge kind={s === "admin" ? "warn" : "open"}>{s}</Badge>{" "}
              </span>
            ))}
          </span>
          <span className="muted note">
            <code>{k.id}</code>
            {k.created_by === "cli" && " · made with tpp apikey"}
          </span>
        </span>
      ),
    },
    { key: "created", header: "Created", firstDir: "desc", render: (k) => formatDateTime(k.created_at) },
    {
      key: "expires",
      header: "Expires",
      firstDir: "desc",
      render: (k) =>
        !k.expires_at ? (
          <span className="muted">never</span>
        ) : k.status === "expired" ? (
          <Badge kind="bad">expired {ago(Date.now(), k.expires_at, "")}</Badge>
        ) : (
          formatDateTime(k.expires_at)
        ),
    },
    {
      key: "last_used",
      header: "Last used",
      firstDir: "desc",
      render: (k) =>
        k.last_used_at ? (
          <span className="stack">
            <span>{ago(Date.now(), k.last_used_at, "")}</span>
            {k.last_used_ip && (
              <span className="muted note">
                from <code>{k.last_used_ip}</code>
              </span>
            )}
          </span>
        ) : (
          <Dash />
        ),
    },
    {
      key: "actions",
      header: "Actions",
      headerHidden: true,
      className: "actions",
      render: (k) => <RevokeKey apiKey={k} action={action} onRevoke={() => revoke(k)} />,
    },
  ];

  const full = meta !== null && meta.count >= meta.max;

  return (
    <>
      <ErrorNotice error={action.error} />
      <h1>API keys</h1>
      <p className="lede">
        Keys for the admin REST API under <code>/api/v1/</code>, for scripts and automation. A key is shown once, when
        it is created; the relay keeps only a hash of it. See the <a href={apiDocsURL}>API docs</a>.
      </p>

      <CreateKeyForm
        key={created}
        defaultDays={meta?.default_expiry_days ?? 90}
        full={full}
        max={meta?.max ?? 50}
        action={action}
        onCreate={create}
      />

      <SmartTable<ApiKey, ApiKeyList>
        id="api-keys"
        caption="API keys"
        noun="keys"
        endpoint="/admin/api/api-keys"
        columns={columns}
        rowKey={(k) => k.id}
        defaultSort={defaultSort}
        searchPlaceholder="Search keys"
        onResponse={setMeta}
        controller={table}
        emptyText="No API keys yet. Create one above, or run tpp apikey create on the relay's host."
      />
      {meta && (
        <p className="hint spaced">
          {meta.count} of at most {meta.max} keys.
        </p>
      )}

      <ApiKeyDialog apiKey={shown} onClose={close} />
    </>
  );
}

function CreateKeyForm({
  defaultDays,
  full,
  max,
  action,
  onCreate,
}: {
  defaultDays: number;
  full: boolean;
  max: number;
  action: Action;
  onCreate: (key: NewApiKey) => void;
}) {
  const [name, setName] = useState("");
  const [scopes, setScopes] = useState<ApiScope[]>(["read"]);
  const [days, setDays] = useState(defaultDays);
  const id = useId();
  const toggle = (s: ApiScope, on: boolean) =>
    setScopes((cur) => (on ? [...cur.filter((x) => x !== s), s] : cur.filter((x) => x !== s)));

  return (
    <form
      className="card"
      onSubmit={(event) => {
        event.preventDefault();
        onCreate({ name, scopes, expiry_days: days });
      }}
    >
      <div className="form-row">
        <div className="field">
          <label htmlFor={`${id}-name`}>Name</label>
          <input
            type="text"
            id={`${id}-name`}
            maxLength={80}
            placeholder="e.g. Backup script"
            required
            value={name}
            onChange={(e) => setName(e.target.value)}
          />
        </div>
        <button type="submit" disabled={action.busy || full || scopes.length === 0}>
          Create key
        </button>
      </div>
      {full && <span className="hint">There are already {max} keys. Revoke one you no longer use first.</span>}
      <div className="form-grid">
        <fieldset className="field scopes">
          <legend>Scopes</legend>
          {(Object.keys(scopeText) as ApiScope[]).map((s) => (
            <label key={s} className="check">
              <input type="checkbox" checked={scopes.includes(s)} onChange={(e) => toggle(s, e.target.checked)} />
              <span>
                <strong>{s}</strong> <span className="muted">{scopeText[s]}</span>
              </span>
            </label>
          ))}
        </fieldset>
        <div className="field">
          <label htmlFor={`${id}-expires`}>Expires</label>
          <select id={`${id}-expires`} value={days} onChange={(e) => setDays(Number(e.target.value))}>
            {lifetimes.map((l) => (
              <option key={l.days} value={l.days}>
                {l.days === 0 ? l.label : `In ${l.label}`}
              </option>
            ))}
          </select>
        </div>
      </div>
    </form>
  );
}

function RevokeKey({ apiKey: k, action, onRevoke }: { apiKey: ApiKey; action: Action; onRevoke: () => void }) {
  const expired = k.status === "expired";
  const question = expired
    ? `Remove the expired key “${k.name}” from the list?`
    : `Revoke “${k.name}”? Anything using it stops working at once, and this cannot be undone.`;
  return (
    <form
      className="inline-form"
      onSubmit={(event) => {
        event.preventDefault();
        if (window.confirm(question)) onRevoke();
      }}
    >
      <button type="submit" className={expired ? "secondary small" : "danger small"} disabled={action.busy}>
        {expired ? "Remove" : "Revoke"}
      </button>
    </form>
  );
}
