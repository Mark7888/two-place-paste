import { useCallback, useId, useState } from "react";
import { api, type Expiry, type NewToken, type Token } from "../api";
import { Badge, type BadgeKind } from "../components/Badge";
import { Empty, SectionHead } from "../components/Card";
import { CopyButton } from "../components/CopyButton";
import { ErrorNotice } from "../components/Notice";
import { Cell, Dash, Table } from "../components/Table";
import { TokenDialog, type ShownToken } from "../components/TokenDialog";
import { useAction, type Action } from "../hooks/useAction";
import { useDocumentTitle } from "../hooks/useDocumentTitle";
import { useResource } from "../hooks/useResource";
import { ago, formatDateTime } from "../lib/format";
import { groupPath } from "../lib/paths";
import { Link } from "../router";

const statuses: Record<Token["status"], { text: string; kind: BadgeKind }> = {
  unused: { text: "unused", kind: "open" },
  used: { text: "used", kind: "used" },
  group_deleted: { text: "group deleted", kind: "used" },
  expired: { text: "expired", kind: "bad" },
};

/** TokensPage mints creation tokens, lists them and revokes the unused ones.
 *  One token creates exactly one group (SPEC §3.1). */
export function TokensPage() {
  useDocumentTitle("Creation tokens");
  const { data, error, version, reload } = useResource((signal) => api.tokens(signal), []);
  const action = useAction();
  const [shown, setShown] = useState<ShownToken | null>(null);
  const close = useCallback(() => setShown(null), []);

  const create = (token: NewToken) =>
    action.run(async () => {
      const t = await api.createToken(token);
      reload();
      // The point of minting a token is to hand it over, and this is the
      // moment: its code opens straight away. The value came back in the
      // response body, never in a URL.
      if (t.url) setShown({ value: t.value, name: t.name, url: t.url });
    });

  const revoke = (t: Token) =>
    action.run(async () => {
      await api.revokeToken(t.value);
      reload();
    });

  return (
    <>
      <ErrorNotice error={action.error ?? error?.message} />
      <h1>Creation tokens</h1>
      <p className="lede">
        One token creates exactly one group. Generate one, hand its link or QR code to the first device, and it is
        spent.
      </p>

      <CreateTokenForm key={version} tags={data?.tags ?? []} action={action} onCreate={create} />

      {data && (
        <>
          <SectionHead title="Tokens">
            {data.tokens.length > 0 && <span className="count">{data.tokens.length} total</span>}
          </SectionHead>
          {data.tokens.length > 0 ? (
            <Table
              columns={[
                { label: "Name" },
                { label: "Created" },
                { label: "Status" },
                { label: "Group" },
                { label: "Actions", srOnly: true },
              ]}
            >
              {data.tokens.map((t) => (
                <TokenRow key={t.value} token={t} action={action} onShow={setShown} onRevoke={() => revoke(t)} />
              ))}
            </Table>
          ) : (
            <Empty>No tokens yet. Generate one above and send its link to the first device.</Empty>
          )}
        </>
      )}

      <TokenDialog token={shown} onClose={close} />
    </>
  );
}

function CreateTokenForm({
  tags,
  action,
  onCreate,
}: {
  tags: string[];
  action: Action;
  onCreate: (token: NewToken) => void;
}) {
  const [token, setToken] = useState<NewToken>({ name: "", note: "", tag: "", expires: "never" });
  const set = (patch: Partial<NewToken>) => setToken((t) => ({ ...t, ...patch }));
  const id = useId();
  return (
    <form
      className="card"
      onSubmit={(event) => {
        event.preventDefault();
        onCreate(token);
      }}
    >
      <div className="form-row">
        <div className="field">
          <label htmlFor={`${id}-name`}>Name</label>
          <input
            type="text"
            id={`${id}-name`}
            maxLength={120}
            placeholder="e.g. Anna — laptop"
            required
            value={token.name}
            onChange={(e) => set({ name: e.target.value })}
          />
        </div>
        <button type="submit" disabled={action.busy}>
          Generate token
        </button>
      </div>
      {/* The hint sits outside the row on purpose: inside the field it would
          become the field's bottom edge, and the row aligns its button to
          that rather than to the input. */}
      <span className="hint">It becomes the group's name, which its devices show. You can rename the group later.</span>
      <div className="form-grid">
        <div className="field">
          <label htmlFor={`${id}-note`}>
            Note <span className="muted">(optional)</span>
          </label>
          <input
            type="text"
            id={`${id}-note`}
            maxLength={500}
            placeholder="Only you see this"
            value={token.note}
            onChange={(e) => set({ note: e.target.value })}
          />
        </div>
        <div className="field">
          <label htmlFor={`${id}-tag`}>
            Tag <span className="muted">(optional)</span>
          </label>
          <select id={`${id}-tag`} value={token.tag} onChange={(e) => set({ tag: e.target.value })}>
            <option value="">No tag</option>
            {tags.map((t) => (
              <option key={t} value={t}>
                {t}
              </option>
            ))}
          </select>
        </div>
        <div className="field">
          <label htmlFor={`${id}-expires`}>Link expires</label>
          <select
            id={`${id}-expires`}
            value={token.expires}
            onChange={(e) => set({ expires: e.target.value as Expiry })}
          >
            <option value="never">Never</option>
            <option value="1">In 1 day</option>
            <option value="7">In 7 days</option>
            <option value="30">In 30 days</option>
          </select>
        </div>
      </div>
    </form>
  );
}

function TokenRow({
  token: t,
  action,
  onShow,
  onRevoke,
}: {
  token: Token;
  action: Action;
  onShow: (token: ShownToken) => void;
  onRevoke: () => void;
}) {
  const status = statuses[t.status];
  const spent = t.status === "used" || t.status === "group_deleted";
  let expires = "";
  if (!spent && t.expires_at) {
    expires = t.status === "expired" ? `expired ${ago(Date.now(), t.expires_at, "")}` : `expires ${formatDateTime(t.expires_at)}`;
  }

  return (
    <tr>
      <Cell label="Name" className="name">
        <span className="stack">
          <span>
            {t.name}
            {t.tag && (
              <>
                {" "}
                <Badge kind="open">{t.tag}</Badge>
              </>
            )}
          </span>
          {t.note && <span className="muted note">{t.note}</span>}
        </span>
      </Cell>
      <Cell label="Created">{formatDateTime(t.created_at)}</Cell>
      <Cell label="Status">
        <span className="stack">
          <span>
            <Badge kind={status.kind}>{status.text}</Badge>
          </span>
          {expires && <span className="muted small">{expires}</span>}
        </span>
      </Cell>
      <Cell label="Group">
        {t.group_id ? (
          <Link className="nowrap" to={groupPath(t.group_id)}>
            Open group
          </Link>
        ) : spent ? (
          <span className="muted">deleted</span>
        ) : (
          <Dash />
        )}
      </Cell>
      <Cell label="Actions" className="actions">
        <TokenActions token={t} spent={spent} action={action} onShow={onShow} onRevoke={onRevoke} />
      </Cell>
    </tr>
  );
}

function TokenActions({
  token: t,
  spent,
  action,
  onShow,
  onRevoke,
}: {
  token: Token;
  spent: boolean;
  action: Action;
  onShow: (token: ShownToken) => void;
  onRevoke: () => void;
}) {
  if (spent) return <span className="muted">Spent</span>;

  // Revoking and removing are the same request; only the question differs.
  const question =
    t.status === "expired"
      ? `Remove the expired token “${t.name}” from the list?`
      : `Revoke “${t.name}”? Its link and QR code stop working, and this cannot be undone.`;
  const revokeForm = (
    <form
      className="inline-form"
      onSubmit={(event) => {
        event.preventDefault();
        if (window.confirm(question)) onRevoke();
      }}
    >
      <button type="submit" className={t.status === "expired" ? "secondary small" : "danger small"} disabled={action.busy}>
        {t.status === "expired" ? "Remove" : "Revoke"}
      </button>
    </form>
  );

  if (t.status === "expired" || !t.url) return revokeForm;
  const url = t.url;
  return (
    <>
      <CopyButton text={url} label="Copy link" className="secondary small" />{" "}
      <button type="button" className="secondary small" onClick={() => onShow({ value: t.value, name: t.name, url })}>
        Show code
      </button>{" "}
      {revokeForm}
    </>
  );
}
