import { useCallback, useId, useState } from "react";
import { api, type Expiry, type NewToken, type Token, type TokenList } from "../api";
import { Badge, type BadgeKind } from "../components/Badge";
import { Dash } from "../components/Dash";
import { ErrorNotice } from "../components/Notice";
import { SmartTable, useTableController, type Column, type DefaultSort } from "../components/SmartTable";
import { TokenDialog, type ShownToken } from "../components/TokenDialog";
import { useAction, type Action } from "../hooks/useAction";
import { useCopyFeedback } from "../hooks/useCopyFeedback";
import { useDocumentTitle } from "../hooks/useDocumentTitle";
import { ago, formatDateTime } from "../lib/format";
import { groupPath } from "../lib/paths";
import { Link, useSearchParams } from "../router";

const statuses: Record<Token["status"], { text: string; kind: BadgeKind }> = {
  unused: { text: "unused", kind: "open" },
  used: { text: "used", kind: "used" },
  group_deleted: { text: "group deleted", kind: "used" },
  expired: { text: "expired", kind: "bad" },
};

const spent = (t: Token) => t.status === "used" || t.status === "group_deleted";

const defaultSort: DefaultSort = { key: "created", dir: "desc" };

/** TokensPage mints creation tokens, lists them and revokes the unused ones.
 *  One token creates exactly one group (SPEC §3.1). */
export function TokensPage() {
  useDocumentTitle("Creation tokens");
  const [params, setParams] = useSearchParams();
  const status = params.get("status") ?? "";
  const [meta, setMeta] = useState<TokenList | null>(null);
  const [created, setCreated] = useState(0);
  const action = useAction();
  const table = useTableController();
  const [shown, setShown] = useState<ShownToken | null>(null);
  const close = useCallback(() => setShown(null), []);

  const create = (token: NewToken) =>
    action.run(async () => {
      const t = await api.createToken(token);
      setCreated((n) => n + 1);
      table.refresh();
      // The point of minting a token is to hand it over, and this is the
      // moment: its code opens straight away.
      setShown({ handle: t.handle, name: t.name, url: t.url });
    });

  // A token's link is asked for only when it is wanted. If it has stopped
  // working since the list was loaded, the list is out of date: say why and
  // reload it.
  const link = async (t: Token): Promise<string | null> => {
    let url: string | null = null;
    await action.run(async () => {
      try {
        url = (await api.tokenLink(t.handle)).url;
      } catch (err) {
        table.refresh();
        throw err;
      }
    });
    return url;
  };

  const show = async (t: Token) => {
    const url = await link(t);
    if (url) setShown({ handle: t.handle, name: t.name, url });
  };

  const revoke = (t: Token) =>
    action.run(async () => {
      await api.revokeToken(t.handle);
      table.refresh();
    });

  const columns: Column<Token>[] = [
    {
      key: "name",
      header: "Name",
      className: "name",
      render: (t) => (
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
      ),
    },
    { key: "created", header: "Created", firstDir: "desc", render: (t) => formatDateTime(t.created_at) },
    {
      key: "status",
      header: "Status",
      render: (t) => <Badge kind={statuses[t.status].kind}>{statuses[t.status].text}</Badge>,
    },
    { key: "expires", header: "Expires", firstDir: "desc", render: (t) => <Expires token={t} /> },
    {
      key: "group",
      header: "Group",
      sortable: false,
      render: (t) =>
        t.group_handle ? (
          <Link className="nowrap" to={groupPath(t.group_handle)}>
            Open group
          </Link>
        ) : spent(t) ? (
          <span className="muted">deleted</span>
        ) : (
          <Dash />
        ),
    },
    {
      key: "actions",
      header: "Actions",
      headerHidden: true,
      className: "actions",
      render: (t) => (
        <TokenActions token={t} action={action} onLink={link} onShow={show} onRevoke={() => revoke(t)} />
      ),
    },
  ];

  return (
    <>
      <ErrorNotice error={action.error} />
      <h1>Creation tokens</h1>
      <p className="lede">
        One token creates exactly one group. Generate one, hand its link or QR code to the first device, and it is
        spent.
      </p>

      <CreateTokenForm key={created} tags={meta?.tags ?? []} action={action} onCreate={create} />

      <SmartTable<Token, TokenList>
        id="tokens"
        caption="Tokens"
        noun="tokens"
        endpoint="/admin/api/tokens"
        filters={{ status }}
        columns={columns}
        rowKey={(t) => t.handle}
        defaultSort={defaultSort}
        searchPlaceholder="Search tokens"
        onResponse={setMeta}
        controller={table}
        toolbar={
          <label className="table-filter">
            Status{" "}
            <select value={status} onChange={(e) => setParams({ status: e.target.value || null, page: null })}>
              <option value="">All</option>
              <option value="unused">Unused</option>
              <option value="used">Used</option>
              <option value="expired">Expired</option>
            </select>
          </label>
        }
        emptyText={
          status ? "No tokens with this status." : "No tokens yet. Generate one above and send its link to the first device."
        }
      />

      <TokenDialog token={shown} onClose={close} />
    </>
  );
}

function Expires({ token: t }: { token: Token }) {
  if (spent(t)) return <Dash />;
  if (!t.expires_at) return <span className="muted">never</span>;
  if (t.status === "expired") return <>expired {ago(Date.now(), t.expires_at, "")}</>;
  return <>{formatDateTime(t.expires_at)}</>;
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

function TokenActions({
  token: t,
  action,
  onLink,
  onShow,
  onRevoke,
}: {
  token: Token;
  action: Action;
  onLink: (token: Token) => Promise<string | null>;
  onShow: (token: Token) => void;
  onRevoke: () => void;
}) {
  const feedback = useCopyFeedback();
  if (spent(t)) return <span className="muted">Spent</span>;

  // Revoking and removing are the same request; only the question differs.
  const expired = t.status === "expired";
  const question = expired
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
      <button type="submit" className={expired ? "secondary small" : "danger small"} disabled={action.busy}>
        {expired ? "Remove" : "Revoke"}
      </button>
    </form>
  );
  if (expired) return revokeForm;

  const copyLink = async () => {
    const url = await onLink(t);
    if (url) await feedback.copy(url);
  };
  return (
    <>
      <button type="button" className="secondary small" onClick={() => void copyLink()}>
        <span>{feedback.message ?? "Copy link"}</span>
      </button>{" "}
      <button type="button" className="secondary small" onClick={() => onShow(t)}>
        Show code
      </button>{" "}
      {revokeForm}
    </>
  );
}
