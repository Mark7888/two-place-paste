import type { ReactNode } from "react";
import type { SortDir } from "../../api";

export type { SortDir };

/** Column says how one column of a SmartTable looks and sorts. */
export interface Column<T> {
  /** key is the sort key sent to the relay, and unique per table. */
  key: string;
  header: string;
  /** sortable defaults to true. A table given its rows (rather than an
   *  endpoint) can sort only by columns with a sortValue. */
  sortable?: boolean;
  /** firstDir is the direction a click on this column sorts in first:
   *  ascending for text, descending for sizes, counts and dates. */
  firstDir?: SortDir;
  /** align "end" is for numbers. */
  align?: "start" | "end";
  width?: string;
  /** render is the cell's content: text, badges, meters, links, buttons. */
  render: (row: T) => ReactNode;
  /** mobileLabel labels the cell in the phone layout; it defaults to header. */
  mobileLabel?: string;
  hideOnMobile?: boolean;
  /** headerHidden keeps the header for screen readers only, for an actions
   *  column. Such a column does not sort. */
  headerHidden?: boolean;
  /** className is added to the column's cells, e.g. "name" or "actions". */
  className?: string;
  /** sortValue is what a table given its rows sorts this column by. */
  sortValue?: (row: T) => string | number;
}

/** TableController lets a screen reload its table after an action, keeping
 *  its search, sort and page. */
export interface TableController {
  refresh: () => void;
}

/** TableState is a list's search, sort and page, as the URL holds them. */
export interface TableState {
  q: string;
  sort: string;
  dir: SortDir;
  page: number;
  size: number;
}

export interface DefaultSort {
  key: string;
  dir: SortDir;
}
