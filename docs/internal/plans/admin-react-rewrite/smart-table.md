# Plan — SmartTable, the admin UI's data table

**Status:** proposed. The React rewrite it builds on has landed (`rewrite.md`): the admin
UI is now a React app in `server/ui` over a JSON API, still with the plain tables of the
templates it replaced (`server/ui/src/components/Table.tsx`). Nothing in this plan is
built yet; this is the component that should replace those tables.
**Touches (when built):** `server/ui` and the admin server's JSON endpoints under
`server/internal/admin`.

---

## 1. Purpose

One reusable component for every list in the admin UI — Groups, Tags, Tokens and the
Audit log — with a search bar, sortable column headers, pagination, and loading,
empty and error states. Each page says only *what* to show, never *how*.

## 2. Usage

```tsx
<SmartTable<GroupRow>
  endpoint="/admin/api/groups"
  columns={groupColumns}
  rowKey={(g) => g.id}
  defaultSort={{ key: "storage", dir: "desc" }}
  searchPlaceholder="Search groups, tags or devices"
  filters={<TagFilter />}            // optional, sits next to the search box
  emptyText="No groups yet."
  onRowClick={(g) => navigate(`/admin/groups/${g.id}`)} // optional
/>
```

## 3. Column definition

```ts
interface Column<T> {
  key: string;                    // sort key sent to the server; unique per table
  header: string;                 // header label
  sortable?: boolean;             // default true
  align?: "start" | "end";        // numbers align to the end
  width?: string;                 // optional CSS width
  render: (row: T) => ReactNode;  // cell content: badges, meters, links, buttons
  mobileLabel?: string;           // label in the phone card layout; defaults to header
  hideOnMobile?: boolean;
}
```

## 4. State lives in the URL

- `?q=…&sort=key&dir=asc|desc&page=N&size=25|50|100`, plus any page-specific filters
  (such as `tag=`).
- Pages are bookmarkable and shareable, and back/forward walk through searches and
  pages.
- Parameters are read on mount and written with `replaceState` as the user types, and
  with `pushState` for page, sort and size changes.
- Invalid values fall back to defaults: an unknown sort key uses the default sort, an
  unknown size uses 25, and a page past the end uses the last page.

## 5. Server contract (one endpoint per table)

Request: `GET <endpoint>?q=&sort=&dir=&page=&size=&<filters>`, using the admin session
cookie. Being a GET, it needs no CSRF token.

Response:

```json
{
  "rows": [ ... ],
  "total": 112,
  "page": 2,
  "size": 25,
  "sort": "storage",
  "dir": "desc"
}
```

- `total` counts the rows matching the search and filters, across all pages. `page`,
  `sort` and `dir` are what the server actually applied, after clamping and fallback.
- The server does the search, sort and paging. The client never holds the full list,
  so this scales and keeps the full data out of the browser.
- The server owns the allowed sort keys and searchable fields. An unknown key is
  ignored, never an error.
- Sorting is stable: ties break on a fixed secondary key (name, then ID), so rows do
  not jump between pages.

## 6. Search

- One text box at the top, debounced by about 300 ms. Enter searches at once, and
  Escape or ✕ clears it.
- Case-insensitive substring match. Several words must all match, each in any
  searchable field.
- A new search resets to page 1 and keeps the current sort.
- No matches shows "No groups match "anna" · Clear search". That is a different
  message from an empty table ("No groups yet").

## 7. Sort

- Clicking a header sorts by that column; clicking it again reverses the direction.
  Clicking another column switches to it in that column's natural first direction:
  ascending for text, descending for sizes, counts and dates.
- The active header shows ▲/▼ and carries `aria-sort="ascending|descending"`. Other
  sortable headers show a faint ↕ on hover.
- Headers are real `<button>`s inside `<th>`, so they work from the keyboard.
- On phones the table becomes cards with no header row, so a "Sort by" dropdown plus a
  direction toggle appears in the toolbar instead.

## 8. Pagination

The footer is always shown:

- Left: "Showing 26–50 of 112", or "Showing 0 of 0".
- Middle: ‹ Prev, page numbers with ellipses (1 … 4 5 **6** 7 8 … 20), Next ›. Prev
  and Next are disabled at the ends.
- Right: "Rows per page" with 25 (default), 50 and 100. Changing it keeps the first
  visible row on screen by recalculating the page.
- The footer stays visible even when everything fits on one page, with the controls
  disabled, so the layout never jumps.
- The page size may be remembered per table in `localStorage`, but the URL wins.

## 9. Loading, error and refresh

- The first load shows skeleton rows. Later loads keep the current rows dimmed with a
  thin progress bar, so the table does not flash.
- Stale responses are dropped: a request id or `AbortController` cancels the previous
  request when a newer one starts.
- An error shows inline above the table ("Could not load groups · Retry"), and the last
  good rows stay visible.
- A `refresh()` handle lets a page reload after an action (a delete, a rename) without
  losing search, sort or page. If the current page becomes empty, it moves back one
  page.

## 10. Accessibility

- A real `<table>` with a visually hidden `<caption>` and `<th scope="col">`.
- Result counts and "Loading…" are announced through an `aria-live="polite"` region.
- Focus stays on the search box while typing. After a page change, focus moves to the
  table caption.

## 11. Per-table configuration

| Table | Searchable fields | Sortable columns | Default sort | Extra filters |
|---|---|---|---|---|
| Groups | name, tag, short id, device names | name, tag, devices, last active, entries, oldest, storage | storage ↓ | tag (all / a tag / untagged), stale only |
| Tags | name | name, groups, devices, storage, limit | name ↑ | — |
| Tokens | name, note, tag, status | name, created, status, expires | created ↓ | status (unused / used / expired) |
| Audit log | action, target, details, IP | when, action, target, IP | when ↓ | action type |

Small tables (a group's devices, confirmation pages) use the same component with
`searchable={false}` and `paginated={false}`, keeping only sortable headers.

## 12. Security rules carried over

- Never put a token value, a full group id, a key or an entry id in rows, search
  fields, URLs or logs; use short ids and names (CLAUDE.md, security rules).
- Actions inside rows (Revoke, Delete) keep posting with the CSRF token and their
  confirmation pages or dialogs. The table never offers bulk selection.
- Keep the current Content-Security-Policy: everything served from the relay itself,
  no inline scripts.

## 13. Tests

- **Server, per endpoint:** search with one and several words, each sort key and
  direction, stable tie-breaking, page clamping, unknown parameters, and the session
  requirement.
- **Component:** URL round trip, debounce, a stale response being dropped, footer
  states (one page, many pages, empty), sorting from the keyboard, the phone layout's
  sort control.
