import { act, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, test, vi } from "vitest";
import type { TablePage } from "../../api";
import { RouterProvider } from "../../router";
import { SmartTable } from "./SmartTable";
import type { Column } from "./types";

interface Fruit {
  id: string;
  name: string;
  weight: number;
}

const columns: Column<Fruit>[] = [
  { key: "name", header: "Name", render: (f) => f.name },
  { key: "weight", header: "Weight", firstDir: "desc", render: (f) => f.weight },
  { key: "actions", header: "Actions", headerHidden: true, render: () => <button type="button">Do</button> },
];

const fruits = (n: number, from = 1): Fruit[] =>
  Array.from({ length: n }, (_, i) => ({ id: String(from + i), name: `Fruit ${from + i}`, weight: from + i }));

/** pageOf answers a list request the way the relay does, from its query. */
function pageOf(url: string, total: number): TablePage<Fruit> {
  const q = new URL(url, "http://relay").searchParams;
  const size = Number(q.get("size"));
  const last = Math.max(1, Math.ceil(total / size));
  const page = Math.min(Number(q.get("page")), last);
  const start = (page - 1) * size;
  const count = Math.max(0, Math.min(size, total - start));
  return {
    rows: fruits(count, start + 1),
    total,
    page,
    size,
    sort: q.get("sort") ?? "weight",
    dir: (q.get("dir") as "asc" | "desc") ?? "desc",
  };
}

const json = (body: unknown, status = 200) =>
  new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });

/** stubList answers every list request with a page of a list of total rows,
 *  and records the requests. */
function stubList(total: number) {
  const fetch = vi.fn(async (url: string) => json(pageOf(url, total)));
  vi.stubGlobal("fetch", fetch);
  return fetch;
}

function lastQuery(fetch: ReturnType<typeof vi.fn>): URLSearchParams {
  const url = fetch.mock.calls[fetch.mock.calls.length - 1][0] as string;
  return new URL(url, "http://relay").searchParams;
}

function renderTable(url = "/admin/fruits", props: { filters?: Record<string, string> } = {}) {
  window.history.replaceState(null, "", url);
  return render(
    <RouterProvider>
      <SmartTable<Fruit>
        id="fruits"
        caption="Fruits"
        noun="fruits"
        endpoint="/admin/api/fruits"
        columns={columns}
        rowKey={(f) => f.id}
        defaultSort={{ key: "weight", dir: "desc" }}
        emptyText="No fruits yet."
        filters={props.filters}
      />
    </RouterProvider>,
  );
}

/** range is the footer's "Showing …" line. A live region says the same for
 *  screen readers, so the text alone is not one element. */
const range = () => document.querySelector(".table-range")?.textContent;
const waitForRange = (text: string) => waitFor(() => expect(range()).toBe(text));

const rowNames = () =>
  screen
    .getAllByRole("row")
    .slice(1)
    .map((r) => within(r).getAllByRole("cell")[0].textContent);

describe("SmartTable", () => {
  test("reads its state from the URL and sends it with the screen's filters", async () => {
    const fetch = stubList(112);
    renderTable("/admin/fruits?dir=asc&page=2&q=fruit&size=50&sort=name", { filters: { colour: "red" } });
    await screen.findByText("Fruit 51");

    const q = lastQuery(fetch);
    expect(Object.fromEntries(q)).toEqual({ colour: "red", dir: "asc", page: "2", q: "fruit", size: "50", sort: "name" });
    expect(screen.getByRole("searchbox", { name: "Search fruits" })).toHaveProperty("value", "fruit");
    expect(range()).toBe("Showing 51–100 of 112");
  });

  test("a page change is a history step; the URL and the request follow", async () => {
    const fetch = stubList(112);
    const push = vi.spyOn(window.history, "pushState");
    renderTable();
    await screen.findByText("Fruit 1");

    fireEvent.click(screen.getByRole("button", { name: "Next ›" }));
    await screen.findByText("Fruit 26");
    expect(window.location.search).toBe("?page=2");
    expect(push).toHaveBeenCalledTimes(1);
    expect(lastQuery(fetch).get("page")).toBe("2");

    // Back walks to the first page again, without remounting anything.
    act(() => {
      window.history.replaceState(null, "", "/admin/fruits");
      window.dispatchEvent(new PopStateEvent("popstate"));
    });
    await screen.findByText("Fruit 1");
  });

  test("typing searches once it rests, without a history step; Enter at once; Escape clears", async () => {
    vi.useFakeTimers();
    try {
      const fetch = stubList(3);
      const push = vi.spyOn(window.history, "pushState");
      renderTable();
      await act(() => vi.runAllTimersAsync());
      const calls = fetch.mock.calls.length;
      const box = screen.getByRole("searchbox");

      fireEvent.change(box, { target: { value: "ap" } });
      fireEvent.change(box, { target: { value: "app" } });
      await act(() => vi.advanceTimersByTimeAsync(299));
      expect(fetch.mock.calls.length).toBe(calls);
      await act(() => vi.advanceTimersByTimeAsync(1));
      expect(fetch.mock.calls.length).toBe(calls + 1);
      expect(lastQuery(fetch).get("q")).toBe("app");
      expect(window.location.search).toBe("?q=app");
      expect(push).not.toHaveBeenCalled();

      fireEvent.change(box, { target: { value: "apple" } });
      fireEvent.keyDown(box, { key: "Enter" });
      await act(() => vi.advanceTimersByTimeAsync(0));
      expect(lastQuery(fetch).get("q")).toBe("apple");

      fireEvent.keyDown(box, { key: "Escape" });
      await act(() => vi.advanceTimersByTimeAsync(0));
      expect(box).toHaveProperty("value", "");
      expect(window.location.search).toBe("");
    } finally {
      vi.useRealTimers();
    }
  });

  test("a new search starts from the first page and keeps the sort", async () => {
    stubList(112);
    renderTable("/admin/fruits?page=3&sort=name");
    await screen.findByText("Fruit 51");
    fireEvent.change(screen.getByRole("searchbox"), { target: { value: "fruit" } });
    fireEvent.keyDown(screen.getByRole("searchbox"), { key: "Enter" });
    await waitFor(() => expect(window.location.search).toBe("?q=fruit&sort=name"));
  });

  test("an answer overtaken by a newer request is dropped", async () => {
    const pending: ((r: Response) => void)[] = [];
    vi.stubGlobal(
      "fetch",
      vi.fn(
        (url: string) =>
          new Promise<Response>((resolve) => {
            const page = new URL(url, "http://relay").searchParams.get("page");
            pending.push(() => resolve(json({ ...pageOf(url, 112), rows: [{ id: page, name: `page ${page}`, weight: 0 }] })));
          }),
      ),
    );
    renderTable();
    await waitFor(() => expect(pending.length).toBe(1));
    pending[0](new Response());
    await screen.findByText("page 1");

    fireEvent.click(screen.getByRole("button", { name: "Page 2" }));
    fireEvent.click(screen.getByRole("button", { name: "Page 3" }));
    await waitFor(() => expect(pending.length).toBe(3));
    // The newer answer arrives first, then the stale one.
    await act(async () => pending[2](new Response()));
    await act(async () => pending[1](new Response()));
    expect(screen.queryByText("page 2")).toBeNull();
    expect(screen.getByText("page 3")).toBeTruthy();
  });

  test("sorting from the keyboard: a header is a button, and the active one says so", async () => {
    const fetch = stubList(3);
    const user = userEvent.setup();
    renderTable();
    await screen.findByText("Fruit 1");

    const header = screen.getByRole("columnheader", { name: /Weight/ });
    expect(header.getAttribute("aria-sort")).toBe("descending");
    const name = screen.getByRole("button", { name: /^Name/ });
    name.focus();
    await user.keyboard("{Enter}");
    await waitFor(() => expect(lastQuery(fetch).get("sort")).toBe("name"));
    expect(lastQuery(fetch).get("dir")).toBe("asc");
    expect(window.location.search).toBe("?dir=asc&sort=name");
    await waitFor(() =>
      expect(screen.getByRole("columnheader", { name: /^Name/ }).getAttribute("aria-sort")).toBe("ascending"),
    );

    await user.keyboard("{Enter}");
    await waitFor(() => expect(window.location.search).toBe("?dir=desc&sort=name"));
    // The actions column has a header for screen readers and no sort.
    expect(within(screen.getByRole("columnheader", { name: "Actions" })).queryByRole("button")).toBeNull();
  });

  test("the phone layout's sort control sorts by column and direction", async () => {
    stubList(3);
    renderTable();
    await screen.findByText("Fruit 1");

    fireEvent.change(screen.getByRole("combobox", { name: "Sort by" }), { target: { value: "name" } });
    await waitFor(() => expect(window.location.search).toBe("?dir=asc&sort=name"));
    fireEvent.click(screen.getByRole("button", { name: /^Ascending/ }));
    await waitFor(() => expect(window.location.search).toBe("?dir=desc&sort=name"));
  });

  test("footer: one page keeps its controls, disabled", async () => {
    stubList(3);
    renderTable();
    await waitForRange("Showing 1–3 of 3");
    expect(screen.getByRole("button", { name: "‹ Prev" })).toHaveProperty("disabled", true);
    expect(screen.getByRole("button", { name: "Next ›" })).toHaveProperty("disabled", true);
    expect(screen.getByRole("button", { name: "Page 1" })).toHaveProperty("disabled", true);
  });

  test("footer: many pages show the ends, the neighbours and gaps", async () => {
    stubList(500);
    renderTable("/admin/fruits?page=6");
    await waitForRange("Showing 126–150 of 500");
    const pager = screen.getByRole("navigation", { name: "Fruits pages" });
    expect(pager.textContent).toBe("‹ Prev1…45678…20Next ›");
    expect(within(pager).getByRole("button", { name: "Page 6" }).getAttribute("aria-current")).toBe("page");
  });

  test("an empty list and a search with no match say different things", async () => {
    stubList(0);
    renderTable();
    await screen.findByText("No fruits yet.");
    expect(range()).toBe("Showing 0 of 0");

    window.history.replaceState(null, "", "/admin/fruits?q=kiwi");
    act(() => window.dispatchEvent(new PopStateEvent("popstate")));
    await screen.findByText(/No fruits match “kiwi”/);
    fireEvent.click(screen.getByText("Clear search", { selector: "button.link-button" }));
    await waitFor(() => expect(window.location.search).toBe(""));
  });

  test("a page past the end follows the relay to the last page, without a history step", async () => {
    stubList(30);
    const push = vi.spyOn(window.history, "pushState");
    renderTable("/admin/fruits?page=5");
    await screen.findByText("Fruit 26");
    await waitFor(() => expect(window.location.search).toBe("?page=2"));
    expect(push).not.toHaveBeenCalled();
  });

  test("a new page size keeps the first visible row and is remembered", async () => {
    const fetch = stubList(500);
    renderTable("/admin/fruits?page=3");
    await screen.findByText("Fruit 51");
    fireEvent.change(screen.getByRole("combobox", { name: "Rows per page" }), { target: { value: "50" } });
    await waitForRange("Showing 51–100 of 500");
    expect(lastQuery(fetch).get("page")).toBe("2");
    expect(window.localStorage.getItem("tpp-admin:page-size:fruits")).toBe("50");
  });

  test("a failed load says so, keeps the rows, and retries", async () => {
    let fail = false;
    vi.stubGlobal(
      "fetch",
      vi.fn(async (url: string) => (fail ? json({ error: "down" }, 500) : json(pageOf(url, 60)))),
    );
    renderTable();
    await screen.findByText("Fruit 1");
    fail = true;
    fireEvent.click(screen.getByRole("button", { name: "Next ›" }));
    await screen.findByText(/Could not load fruits/);
    expect(screen.getByText("Fruit 1")).toBeTruthy();

    fail = false;
    fireEvent.click(screen.getByRole("button", { name: "Retry" }));
    await screen.findByText("Fruit 26");
    expect(screen.queryByText(/Could not load fruits/)).toBeNull();
  });

  test("the first load shows placeholder rows", async () => {
    vi.stubGlobal("fetch", vi.fn(() => new Promise<Response>(() => undefined)));
    renderTable();
    expect(document.querySelectorAll("tr.skeleton-row").length).toBeGreaterThan(0);
    expect(screen.getByText("Loading…")).toBeTruthy();
  });

  test("rows it was given are sorted in memory, with no search and no pages", () => {
    window.history.replaceState(null, "", "/admin/x");
    render(
      <RouterProvider>
        <SmartTable<Fruit>
          caption="Fruits"
          rows={[
            { id: "1", name: "banana", weight: 2 },
            { id: "2", name: "Apple", weight: 3 },
          ]}
          columns={[
            { key: "name", header: "Name", render: (f) => f.name, sortValue: (f) => f.name },
            { key: "weight", header: "Weight", render: (f) => f.weight },
          ]}
          rowKey={(f) => f.id}
          defaultSort={{ key: "name", dir: "asc" }}
          emptyText="none"
        />
      </RouterProvider>,
    );
    expect(rowNames()).toEqual(["Apple", "banana"]);
    expect(screen.queryByRole("searchbox")).toBeNull();
    expect(screen.queryByRole("navigation")).toBeNull();
    // Weight has no sortValue, so it is a plain header.
    expect(screen.queryByRole("button", { name: /Weight/ })).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: /Name/ }));
    expect(rowNames()).toEqual(["banana", "Apple"]);
    expect(window.location.search).toBe("");
  });
});
