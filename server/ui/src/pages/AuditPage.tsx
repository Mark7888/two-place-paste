import { useState } from "react";
import type { AuditEntry, AuditLog } from "../api";
import { Dash } from "../components/Dash";
import { SmartTable, type Column, type DefaultSort } from "../components/SmartTable";
import { useDocumentTitle } from "../hooks/useDocumentTitle";
import { ago, formatDateTime } from "../lib/format";
import { useSearchParams } from "../router";

const defaultSort: DefaultSort = { key: "when", dir: "desc" };

const columns: Column<AuditEntry>[] = [
  {
    key: "when",
    header: "When",
    firstDir: "desc",
    className: "wide",
    render: (e) => (
      <span className="nowrap">
        {formatDateTime(e.at)} · {ago(Date.now(), e.at, "")}
      </span>
    ),
  },
  { key: "action", header: "Action", className: "name", render: (e) => e.label },
  { key: "target", header: "On", render: (e) => e.target || <Dash /> },
  { key: "detail", header: "Details", sortable: false, className: "wide", render: (e) => e.detail || <Dash /> },
  { key: "ip", header: "From", render: (e) => (e.ip ? <code>{e.ip}</code> : <Dash />) },
];

/** AuditPage lists what was done on these screens, newest first, searchable
 *  and narrowed to one kind of entry (?action=name). It records names and
 *  counts, never a token, password or key. */
export function AuditPage() {
  useDocumentTitle("Audit log");
  const [params, setParams] = useSearchParams();
  const action = params.get("action") ?? "";
  const [meta, setMeta] = useState<AuditLog | null>(null);

  return (
    <>
      <h1>Audit log</h1>
      <p className="lede">
        What was done on these screens, newest first: sign-ins, tokens, groups, tags and settings. The server keeps the
        last {meta?.kept ?? 1000} entries. It records names and counts, never a token, password or key.
      </p>
      <SmartTable<AuditEntry, AuditLog>
        id="audit"
        caption="Audit log"
        noun="entries"
        endpoint="/admin/api/audit"
        filters={{ action }}
        columns={columns}
        rowKey={(e) => `${e.at}|${e.action}|${e.target}|${e.ip}|${e.detail}`}
        defaultSort={defaultSort}
        searchPlaceholder="Search the log"
        onResponse={setMeta}
        toolbar={
          <label className="table-filter">
            Action{" "}
            <select value={action} onChange={(e) => setParams({ action: e.target.value || null, page: null })}>
              <option value="">All</option>
              {meta?.actions.map((a) => (
                <option key={a.action} value={a.action}>
                  {a.label}
                </option>
              ))}
            </select>
          </label>
        }
        emptyText={action ? "Nothing of this kind recorded." : "Nothing recorded yet."}
      />
    </>
  );
}
