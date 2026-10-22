import { api, type AuditEntry } from "../api";
import { Empty } from "../components/Card";
import { ErrorNotice } from "../components/Notice";
import { Cell, Dash, Table } from "../components/Table";
import { useDocumentTitle } from "../hooks/useDocumentTitle";
import { useResource } from "../hooks/useResource";
import { ago, formatDateTime } from "../lib/format";

/** actionLabels names each audit action in plain words. An action this build
 *  does not know, from a newer relay, is shown as it is stored. */
const actionLabels: Record<string, string> = {
  login: "Signed in",
  "login.failed": "Sign-in failed",
  "login.limited": "Sign-in rate limited",
  logout: "Signed out",
  "token.create": "Token created",
  "token.revoke": "Token revoked",
  "group.rename": "Group renamed",
  "group.tag": "Group tag changed",
  "group.limit": "Group storage limit changed",
  "group.purge": "Group entries purged",
  "group.delete": "Group deleted",
  "tag.create": "Tag created",
  "tag.limit": "Tag storage limit changed",
  "tag.delete": "Tag deleted",
  "settings.update": "Settings changed",
  sweep: "Sweep requested",
};

/** AuditPage lists what was done on these screens, newest first. It records
 *  names and counts, never a token, password or key. */
export function AuditPage() {
  useDocumentTitle("Audit log");
  const { data, error } = useResource((signal) => api.audit(signal), []);

  return (
    <>
      <ErrorNotice error={error?.message} />
      {data && (
        <>
          <h1>Audit log</h1>
          <p className="lede">
            What was done on these screens, newest first: sign-ins, tokens, groups, tags and settings. The server keeps
            the last {data.kept} entries. It records names and counts, never a token, password or key.
          </p>
          {data.entries.length > 0 ? (
            <AuditTable entries={data.entries} />
          ) : (
            <Empty>Nothing recorded yet.</Empty>
          )}
        </>
      )}
    </>
  );
}

function AuditTable({ entries }: { entries: AuditEntry[] }) {
  const now = Date.now();
  return (
    <Table columns={[{ label: "When" }, { label: "Action" }, { label: "On" }, { label: "Details" }, { label: "From" }]}>
      {entries.map((e, i) => (
        <tr key={`${e.at}-${i}`}>
          <Cell label="When">
            <span className="nowrap">
              {formatDateTime(e.at)} · {ago(now, e.at, "")}
            </span>
          </Cell>
          <Cell label="Action" className="name">
            {actionLabels[e.action] ?? e.action}
          </Cell>
          <Cell label="On">{e.target || <Dash />}</Cell>
          <Cell label="Details">{e.detail || <Dash />}</Cell>
          <Cell label="From">{e.ip ? <code>{e.ip}</code> : <Dash />}</Cell>
        </tr>
      ))}
    </Table>
  );
}
