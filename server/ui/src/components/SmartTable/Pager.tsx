import { useId } from "react";
import { PAGE_SIZES, lastPage, pageItems, rangeText } from "./state";

/** Pager is a table's footer: which rows are shown, the pages, and the page
 *  size. It is always there, with its buttons disabled when there is one
 *  page, so the layout does not jump as a search narrows the list. */
export function Pager({
  label,
  page,
  size,
  shown,
  total,
  onPage,
  onSize,
}: {
  label: string;
  page: number;
  size: number;
  /** shown is how many rows are on this page. */
  shown: number;
  total: number;
  onPage: (page: number) => void;
  onSize: (size: number) => void;
}) {
  const last = lastPage(total, size);
  const sizeId = useId();
  return (
    <div className="table-foot">
      <span className="table-range">{rangeText(page, size, shown, total)}</span>
      <nav className="pager" aria-label={`${label} pages`}>
        <button type="button" className="secondary small" disabled={page <= 1} onClick={() => onPage(page - 1)}>
          ‹ Prev
        </button>
        {pageItems(page, last).map((p, i) =>
          p === "gap" ? (
            <span key={`gap-${i}`} className="gap" aria-hidden="true">
              …
            </span>
          ) : (
            <button
              key={p}
              type="button"
              className={p === page ? "small current" : "secondary small"}
              aria-current={p === page ? "page" : undefined}
              aria-label={`Page ${p}`}
              disabled={last === 1}
              onClick={() => p !== page && onPage(p)}
            >
              {p}
            </button>
          ),
        )}
        <button type="button" className="secondary small" disabled={page >= last} onClick={() => onPage(page + 1)}>
          Next ›
        </button>
      </nav>
      <span className="page-size">
        <label htmlFor={sizeId}>Rows per page</label>
        <select id={sizeId} value={size} onChange={(e) => onSize(Number(e.target.value))}>
          {PAGE_SIZES.map((s) => (
            <option key={s} value={s}>
              {s}
            </option>
          ))}
        </select>
      </span>
    </div>
  );
}
