// SmartTable is the admin UI's one table component, for every list it shows
// (docs/internal/plans/admin-react-rewrite/smart-table.md). A screen says
// what to show — its columns and where the rows come from — and the table
// does the rest: search, sorting, pages, and the loading, empty and error
// states.
//
// Given an endpoint, the relay searches, sorts and pages, and the table keeps
// all of that in the URL, so a view can be bookmarked and the back button
// walks through it. Given rows instead (a group's devices, a confirmation
// page's one group) it only sorts them, in memory, and keeps nothing in the
// URL.

import { useEffect, useMemo, useRef, useState, type ReactNode } from "react";
import type { TablePage } from "../../api";
import { useSearchParams } from "../../router";
import type { AttachableController } from "./controller";
import { Pager } from "./Pager";
import { SearchBox } from "./SearchBox";
import {
  isSortable,
  listQuery,
  nextSort,
  pageKeepingRow,
  rangeText,
  readState,
  rememberSize,
  rememberedSize,
  sortRows,
  statePatch,
} from "./state";
import { TableView } from "./TableView";
import type { Column, DefaultSort, SortDir } from "./types";
import { useListData } from "./useListData";

interface CommonProps<T> {
  /** caption names the table to screen readers ("Groups"). */
  caption: string;
  columns: readonly Column<T>[];
  rowKey: (row: T) => string;
  defaultSort: DefaultSort;
  /** emptyText is what an empty list says, when no search narrowed it. */
  emptyText: ReactNode;
  onRowClick?: (row: T) => void;
}

export interface RemoteTableProps<T, R extends TablePage<T>> extends CommonProps<T> {
  /** endpoint is the list's API path, e.g. "/admin/api/groups". */
  endpoint: string;
  /** id names the table, for the page size the browser remembers. */
  id: string;
  /** noun is what the rows are, for messages: "groups". */
  noun: string;
  /** filters are the screen's own query parameters, read from the URL by
   *  the screen and sent along: {tag: "family"}. */
  filters?: Record<string, string>;
  /** toolbar holds the screen's filter controls, next to the search box. */
  toolbar?: ReactNode;
  searchPlaceholder?: string;
  /** onResponse hands the screen everything the endpoint said beyond the
   *  rows: counts, filter options, problems. */
  onResponse?: (page: R) => void;
  controller?: AttachableController;
}

export interface LocalTableProps<T> extends CommonProps<T> {
  rows: readonly T[];
}

export function SmartTable<T, R extends TablePage<T> = TablePage<T>>(
  props: RemoteTableProps<T, R> | LocalTableProps<T>,
) {
  return "endpoint" in props ? <RemoteTable {...props} /> : <LocalTable {...props} />;
}

/** LocalTable sorts the rows it was given, and does nothing else: the small
 *  tables, which need neither search nor pages. */
function LocalTable<T>({ caption, columns, rows, rowKey, defaultSort, emptyText, onRowClick }: LocalTableProps<T>) {
  const [sort, setSort] = useState<{ sort: string; dir: SortDir }>({ sort: defaultSort.key, dir: defaultSort.dir });
  const sorted = useMemo(
    () => sortRows(rows, columns.find((c) => c.key === sort.sort), sort.dir),
    [rows, columns, sort],
  );
  if (rows.length === 0) return <p className="empty">{emptyText}</p>;
  return (
    <TableView
      caption={caption}
      columns={columns}
      rows={sorted}
      rowKey={rowKey}
      local
      sort={sort.sort}
      dir={sort.dir}
      onSort={(c) => setSort((s) => nextSort(s, c))}
      onRowClick={onRowClick}
    />
  );
}

function RemoteTable<T, R extends TablePage<T>>({
  endpoint,
  id,
  noun,
  caption,
  columns,
  rowKey,
  defaultSort,
  emptyText,
  onRowClick,
  filters,
  toolbar,
  searchPlaceholder,
  onResponse,
  controller,
}: RemoteTableProps<T, R>) {
  const [params, setParams] = useSearchParams();
  const [remembered] = useState(() => rememberedSize(id));
  const state = readState(params, columns, defaultSort, remembered);
  const filterKey = JSON.stringify(filters ?? {});
  // filterKey stands for filters, which a screen builds afresh every render.
  const query = useMemo(() => listQuery(state, filters ?? {}), [state.q, state.sort, state.dir, state.page, state.size, filterKey]);
  const { data, dataQuery, loading, error, reload } = useListData<R>(endpoint, query);
  const current = data !== null && dataQuery === query;

  const caption$ = useRef<HTMLTableCaptionElement>(null);
  const focusCaption = useRef(false);

  useEffect(() => controller?.attach(reload), [controller, reload]);

  useEffect(() => {
    if (data) onResponse?.(data);
    // A screen's callback is a new function every render; the data is what
    // changed.
  }, [data]);

  // The relay says what it applied. A page past the end comes back as the
  // last page — after a delete emptied the page, say — and an unknown sort
  // as the default: the URL follows, without a history entry of its own.
  useEffect(() => {
    if (!data || !current) return;
    const patch: Record<string, string | null> = {};
    if (data.page !== state.page) Object.assign(patch, statePatch({ page: data.page }, defaultSort));
    if (data.sort !== state.sort || data.dir !== state.dir) {
      Object.assign(patch, statePatch({ sort: data.sort, dir: data.dir }, defaultSort));
    }
    if (Object.keys(patch).length > 0) setParams(patch, { replace: true });
  }, [data, current, state.page, state.sort, state.dir, defaultSort, setParams]);

  // After a page change the caption takes the focus, so a screen reader starts
  // the new page from its top and a keyboard user is not left on a button.
  useEffect(() => {
    if (focusCaption.current && current && !loading) {
      focusCaption.current = false;
      caption$.current?.focus();
    }
  }, [current, loading]);

  const search = (text: string) => {
    if (text.trim() === state.q.trim()) return;
    setParams(statePatch({ q: text, page: 1 }, defaultSort), { replace: true });
  };
  const sortBy = (sort: string, dir: SortDir) =>
    setParams(statePatch({ sort, dir, page: 1 }, defaultSort));
  const goTo = (page: number) => {
    focusCaption.current = true;
    setParams(statePatch({ page }, defaultSort));
  };
  const resize = (size: number) => {
    rememberSize(id, size);
    setParams(statePatch({ size, page: pageKeepingRow(state.page, state.size, size) }, defaultSort));
  };

  const rows = data?.rows ?? [];
  const total = data?.total ?? 0;
  const shownPage = data?.page ?? state.page;
  const sortable = columns.filter((c) => isSortable(c, false));
  const first = data === null;

  let body: ReactNode;
  if (!first && rows.length === 0) {
    body = state.q.trim() ? (
      <p className="empty">
        No {noun} match “{state.q.trim()}” ·{" "}
        <button type="button" className="link-button" onClick={() => search("")}>
          Clear search
        </button>
      </p>
    ) : (
      <p className="empty">{emptyText}</p>
    );
  } else {
    body = (
      <TableView
        caption={caption}
        captionRef={caption$}
        columns={columns}
        rows={rows}
        rowKey={rowKey}
        local={false}
        sort={state.sort}
        dir={state.dir}
        onSort={(c) => {
          const next = nextSort(state, c);
          sortBy(next.sort, next.dir);
        }}
        onRowClick={onRowClick}
        skeleton={first && !error}
        busy={!first && loading}
      />
    );
  }

  return (
    <div className="smart-table">
      <div className="table-toolbar">
        <SearchBox value={state.q} label={`Search ${noun}`} placeholder={searchPlaceholder} onSearch={search} />
        {toolbar}
        {sortable.length > 0 && (
          <SortControl columns={sortable} sort={state.sort} dir={state.dir} onSort={sortBy} />
        )}
      </div>
      {error && (
        <p className="notice error table-error" role="alert">
          Could not load {noun} ·{" "}
          <button type="button" className="link-button" onClick={reload}>
            Retry
          </button>
        </p>
      )}
      {body}
      <Pager
        label={caption}
        page={shownPage}
        size={data?.size ?? state.size}
        shown={rows.length}
        total={total}
        onPage={goTo}
        onSize={resize}
      />
      <p className="sr-only" aria-live="polite">
        {loading ? "Loading…" : rangeText(shownPage, data?.size ?? state.size, rows.length, total)}
      </p>
    </div>
  );
}

/** SortControl is the phone layout's sort: there the table becomes cards with
 *  no header row, so the toolbar offers the column and the direction. The
 *  stylesheet shows it only on narrow screens. */
function SortControl<T>({
  columns,
  sort,
  dir,
  onSort,
}: {
  columns: readonly Column<T>[];
  sort: string;
  dir: SortDir;
  onSort: (sort: string, dir: SortDir) => void;
}) {
  return (
    <div className="sort-control">
      <label>
        Sort by{" "}
        <select
          value={sort}
          onChange={(e) => {
            const column = columns.find((c) => c.key === e.target.value);
            if (column) onSort(column.key, column.firstDir ?? "asc");
          }}
        >
          {columns.map((c) => (
            <option key={c.key} value={c.key}>
              {c.header}
            </option>
          ))}
        </select>
      </label>
      <button
        type="button"
        className="secondary small"
        aria-label={dir === "asc" ? "Ascending; sort descending" : "Descending; sort ascending"}
        onClick={() => onSort(sort, dir === "asc" ? "desc" : "asc")}
      >
        {dir === "asc" ? "▲" : "▼"}
      </button>
    </div>
  );
}
