import type { ReactNode } from "react";

export interface Column {
  label: string;
  /** srOnly hides the header but still announces it, for an actions column. */
  srOnly?: boolean;
}

/** Table is a listing. On a narrow screen the stylesheet turns each row into
 *  a card and labels each cell with its column, which is why every Cell
 *  carries its label. */
export function Table({ columns, children }: { columns: readonly (Column | false)[]; children: ReactNode }) {
  return (
    <div className="table-wrap">
      <table>
        <thead>
          <tr>
            {columns.filter((c): c is Column => c !== false).map((c) => (
              <th key={c.label}>{c.srOnly ? <span className="sr-only">{c.label}</span> : c.label}</th>
            ))}
          </tr>
        </thead>
        <tbody>{children}</tbody>
      </table>
    </div>
  );
}

export function Cell({ label, className, children }: { label: string; className?: string; children?: ReactNode }) {
  return (
    <td data-label={label} className={className}>
      {children}
    </td>
  );
}

/** Dash is the muted "—" of a cell with nothing in it. */
export function Dash() {
  return <span className="muted">—</span>;
}
