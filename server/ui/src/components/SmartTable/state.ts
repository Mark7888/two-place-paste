// What a SmartTable keeps in the URL, and the rules for changing it. Nothing
// here touches React, so it is tested on its own (state.test.ts).

import type { ParamPatch } from "../../router";
import type { Column, DefaultSort, SortDir, TableState } from "./types";

export const PAGE_SIZES = [25, 50, 100] as const;
export const DEFAULT_SIZE = 25;

/** SEARCH_DELAY is how long typing rests before the search runs. */
export const SEARCH_DELAY = 300;

/** isSortable says whether a column sorts, given how the table sorts. */
export function isSortable<T>(column: Column<T>, local: boolean): boolean {
  if (column.headerHidden || column.sortable === false) return false;
  return local ? column.sortValue !== undefined : true;
}

const isDir = (v: string | null): v is SortDir => v === "asc" || v === "desc";

/** readState reads a table's state from the URL. A missing or unknown value
 *  falls back to its default: an unknown sort key to the default sort, an
 *  unknown size to the size remembered for this table or 25, a bad page to 1.
 *  A page past the end is the relay's to clamp. */
export function readState<T>(
  params: URLSearchParams,
  columns: readonly Column<T>[],
  defaultSort: DefaultSort,
  rememberedSize: number | null,
): TableState {
  const sortKey = params.get("sort");
  const column = columns.find((c) => c.key === sortKey && isSortable(c, false));
  let sort = defaultSort.key;
  let dir = defaultSort.dir;
  if (column) {
    sort = column.key;
    const given = params.get("dir");
    dir = isDir(given) ? given : column.key === defaultSort.key ? defaultSort.dir : (column.firstDir ?? "asc");
  }

  const page = Number(params.get("page"));
  const size = Number(params.get("size"));
  const sizes: readonly number[] = PAGE_SIZES;
  return {
    q: params.get("q") ?? "",
    sort,
    dir,
    page: Number.isInteger(page) && page > 1 ? page : 1,
    size: sizes.includes(size) ? size : rememberedSize !== null && sizes.includes(rememberedSize) ? rememberedSize : DEFAULT_SIZE,
  };
}

/** statePatch is the URL change for a new state. Defaults are left out, so
 *  a table in its default state has a plain URL. */
export function statePatch(next: Partial<TableState>, defaultSort: DefaultSort): ParamPatch {
  const patch: ParamPatch = {};
  if (next.q !== undefined) patch.q = next.q.trim() || null;
  if (next.sort !== undefined && next.dir !== undefined) {
    const isDefault = next.sort === defaultSort.key && next.dir === defaultSort.dir;
    patch.sort = isDefault ? null : next.sort;
    patch.dir = isDefault ? null : next.dir;
  }
  if (next.page !== undefined) patch.page = next.page > 1 ? String(next.page) : null;
  if (next.size !== undefined) patch.size = next.size !== DEFAULT_SIZE ? String(next.size) : null;
  return patch;
}

/** nextSort is the sort after a click on a column's header: the same column
 *  turns around, another starts in its natural direction. */
export function nextSort<T>(state: Pick<TableState, "sort" | "dir">, column: Column<T>): { sort: string; dir: SortDir } {
  if (state.sort === column.key) return { sort: column.key, dir: state.dir === "asc" ? "desc" : "asc" };
  return { sort: column.key, dir: column.firstDir ?? "asc" };
}

export function lastPage(total: number, size: number): number {
  return Math.max(1, Math.ceil(total / size));
}

/** pageItems are the page buttons to show: the first and last page, two on
 *  each side of the current one, and a gap for what is left out —
 *  1 … 4 5 6 7 8 … 20. */
export function pageItems(page: number, last: number): (number | "gap")[] {
  if (last <= 7) return Array.from({ length: last }, (_, i) => i + 1);
  const shown = new Set([1, last]);
  for (let p = page - 2; p <= page + 2; p++) if (p >= 1 && p <= last) shown.add(p);
  const sorted = [...shown].sort((a, b) => a - b);
  const out: (number | "gap")[] = [];
  sorted.forEach((p, i) => {
    if (i > 0 && p - sorted[i - 1] > 1) out.push("gap");
    out.push(p);
  });
  return out;
}

/** pageKeepingRow is the page that, at a new page size, shows the row that
 *  was first on the current page. */
export function pageKeepingRow(page: number, oldSize: number, newSize: number): number {
  return Math.floor(((page - 1) * oldSize) / newSize) + 1;
}

/** rangeText says which rows are on screen: "Showing 26–50 of 112". */
export function rangeText(page: number, size: number, shown: number, total: number): string {
  if (total === 0 || shown === 0) return `Showing 0 of ${total}`;
  const start = (page - 1) * size + 1;
  return `Showing ${start}–${start + shown - 1} of ${total}`;
}

/** sortRows sorts rows a table was given, by a column's sortValue. The sort
 *  is stable, so rows the column calls equal keep their order. */
export function sortRows<T>(rows: readonly T[], column: Column<T> | undefined, dir: SortDir): T[] {
  const value = column?.sortValue;
  if (!value) return [...rows];
  const sign = dir === "asc" ? 1 : -1;
  return [...rows].sort((a, b) => {
    const x = value(a);
    const y = value(b);
    if (typeof x === "number" && typeof y === "number") return sign * (x - y);
    return sign * String(x).localeCompare(String(y), undefined, { sensitivity: "base" });
  });
}

const storageKey = (table: string) => `tpp-admin:page-size:${table}`;

/** rememberedSize is the page size last chosen for a table, if the browser
 *  kept it. Storage can be missing or refuse, which is no reason to fail. */
export function rememberedSize(table: string): number | null {
  try {
    const n = Number(window.localStorage.getItem(storageKey(table)));
    return Number.isInteger(n) && n > 0 ? n : null;
  } catch {
    return null;
  }
}

export function rememberSize(table: string, size: number) {
  try {
    window.localStorage.setItem(storageKey(table), String(size));
  } catch {
    // Not remembered; the URL still says it.
  }
}

/** listQuery is the request for one page: the table's state and the screen's
 *  own filters, in a fixed order. */
export function listQuery(state: TableState, filters: Record<string, string>): string {
  const params = new URLSearchParams();
  for (const [name, value] of Object.entries(filters)) if (value !== "") params.set(name, value);
  if (state.q.trim()) params.set("q", state.q.trim());
  params.set("sort", state.sort);
  params.set("dir", state.dir);
  params.set("page", String(state.page));
  params.set("size", String(state.size));
  params.sort();
  return params.toString();
}
