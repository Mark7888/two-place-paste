import type { MouseEvent, ReactNode, Ref } from "react";
import { isSortable } from "./state";
import type { Column, SortDir } from "./types";

/** SKELETON_ROWS is how many placeholder rows a first load shows. */
const SKELETON_ROWS = 5;

function cellClass<T>(column: Column<T>): string | undefined {
  const names = [column.className, column.align === "end" && "num", column.hideOnMobile && "hide-mobile"];
  return names.filter(Boolean).join(" ") || undefined;
}

/** A click on a row opens it, unless it was on something in the row that
 *  does a thing of its own. */
function fromControl(event: MouseEvent): boolean {
  return (event.target as Element).closest("a, button, input, select, textarea, label") !== null;
}

/** TableView draws a SmartTable's <table>: a real table with a hidden
 *  caption and column headers, the sortable ones as buttons. */
export function TableView<T>({
  caption,
  captionRef,
  columns,
  rows,
  rowKey,
  local,
  sort,
  dir,
  onSort,
  onRowClick,
  skeleton,
  busy,
}: {
  caption: string;
  captionRef?: Ref<HTMLTableCaptionElement>;
  columns: readonly Column<T>[];
  rows: readonly T[];
  rowKey: (row: T) => string;
  /** local says the table sorts rows it was given, by sortValue. */
  local: boolean;
  sort: string;
  dir: SortDir;
  onSort: (column: Column<T>) => void;
  onRowClick?: (row: T) => void;
  /** skeleton shows placeholder rows: the first load, with nothing to show. */
  skeleton?: boolean;
  /** busy dims the rows while a newer page loads. */
  busy?: boolean;
}) {
  return (
    <div className={busy ? "table-wrap is-loading" : "table-wrap"}>
      {busy && <div className="progress" aria-hidden="true" />}
      <table aria-busy={busy || skeleton || undefined}>
        <caption className="sr-only" ref={captionRef} tabIndex={-1}>
          {caption}
        </caption>
        <thead>
          <tr>
            {columns.map((c) => (
              <HeaderCell key={c.key} column={c} local={local} sort={sort} dir={dir} onSort={onSort} />
            ))}
          </tr>
        </thead>
        <tbody>
          {skeleton
            ? Array.from({ length: SKELETON_ROWS }, (_, i) => (
                <tr key={`skeleton-${i}`} className="skeleton-row">
                  {columns.map((c) => (
                    <td key={c.key} data-label={c.mobileLabel ?? c.header} className={cellClass(c)}>
                      <span className="skeleton" />
                    </td>
                  ))}
                </tr>
              ))
            : rows.map((row) => (
                <tr
                  key={rowKey(row)}
                  className={onRowClick ? "clickable" : undefined}
                  onClick={onRowClick ? (e) => !fromControl(e) && onRowClick(row) : undefined}
                >
                  {columns.map((c) => (
                    <td key={c.key} data-label={c.mobileLabel ?? c.header} className={cellClass(c)}>
                      {c.render(row)}
                    </td>
                  ))}
                </tr>
              ))}
        </tbody>
      </table>
    </div>
  );
}

function HeaderCell<T>({
  column: c,
  local,
  sort,
  dir,
  onSort,
}: {
  column: Column<T>;
  local: boolean;
  sort: string;
  dir: SortDir;
  onSort: (column: Column<T>) => void;
}) {
  const active = sort === c.key;
  let content: ReactNode = c.header;
  if (c.headerHidden) {
    content = <span className="sr-only">{c.header}</span>;
  } else if (isSortable(c, local)) {
    content = (
      <button type="button" className="sort-button" onClick={() => onSort(c)}>
        {c.header}
        <span className="sort-mark" aria-hidden="true">
          {active ? (dir === "asc" ? "▲" : "▼") : "↕"}
        </span>
      </button>
    );
  }
  return (
    <th
      scope="col"
      className={cellClass(c)}
      style={c.width ? { width: c.width } : undefined}
      aria-sort={active && isSortable(c, local) ? (dir === "asc" ? "ascending" : "descending") : undefined}
    >
      {content}
    </th>
  );
}
