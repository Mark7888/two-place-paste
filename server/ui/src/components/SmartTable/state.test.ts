import { assert, describe, test } from "vitest";
import {
  listQuery,
  nextSort,
  pageItems,
  pageKeepingRow,
  rangeText,
  readState,
  sortRows,
  statePatch,
} from "./state";
import type { Column } from "./types";

interface Row {
  name: string;
  size: number;
}

const columns: Column<Row>[] = [
  { key: "name", header: "Name", render: (r) => r.name, sortValue: (r) => r.name },
  { key: "size", header: "Size", firstDir: "desc", render: (r) => r.size, sortValue: (r) => r.size },
  { key: "actions", header: "Actions", headerHidden: true, render: () => null },
];
const defaultSort = { key: "size", dir: "desc" as const };

const read = (query: string, remembered: number | null = null) =>
  readState(new URLSearchParams(query), columns, defaultSort, remembered);

describe("readState", () => {
  test("an empty URL is the default view", () => {
    assert.deepEqual(read(""), { q: "", sort: "size", dir: "desc", page: 1, size: 25 });
  });

  test("reads every parameter", () => {
    assert.deepEqual(read("q=anna&sort=name&dir=desc&page=3&size=50"), {
      q: "anna",
      sort: "name",
      dir: "desc",
      page: 3,
      size: 50,
    });
  });

  test("a column without a direction sorts in its natural one", () => {
    assert.strictEqual(read("sort=name").dir, "asc");
    assert.strictEqual(read("sort=size").dir, "desc");
  });

  test("invalid values fall back to defaults", () => {
    assert.deepEqual(read("sort=bogus&dir=asc&page=-2&size=7"), { q: "", sort: "size", dir: "desc", page: 1, size: 25 });
    assert.strictEqual(read("sort=actions").sort, "size", "a column that does not sort is not a sort key");
    assert.strictEqual(read("page=2.5").page, 1);
    assert.strictEqual(read("sort=name&dir=sideways").dir, "asc");
  });

  test("the remembered page size applies, but the URL wins", () => {
    assert.strictEqual(read("", 100).size, 100);
    assert.strictEqual(read("size=50", 100).size, 50);
    assert.strictEqual(read("", 33).size, 25, "a remembered size that is not offered is ignored");
  });
});

describe("statePatch", () => {
  test("defaults are left out of the URL", () => {
    assert.deepEqual(statePatch({ q: "  ", sort: "size", dir: "desc", page: 1, size: 25 }, defaultSort), {
      q: null,
      sort: null,
      dir: null,
      page: null,
      size: null,
    });
  });

  test("everything else is written", () => {
    assert.deepEqual(statePatch({ q: " anna ", sort: "size", dir: "asc", page: 4, size: 100 }, defaultSort), {
      q: "anna",
      sort: "size",
      dir: "asc",
      page: "4",
      size: "100",
    });
  });

  test("only what changes is touched", () => {
    assert.deepEqual(statePatch({ page: 2 }, defaultSort), { page: "2" });
  });
});

test("nextSort turns the same column around and starts another in its natural direction", () => {
  assert.deepEqual(nextSort({ sort: "size", dir: "desc" }, columns[1]), { sort: "size", dir: "asc" });
  assert.deepEqual(nextSort({ sort: "size", dir: "asc" }, columns[1]), { sort: "size", dir: "desc" });
  assert.deepEqual(nextSort({ sort: "size", dir: "desc" }, columns[0]), { sort: "name", dir: "asc" });
  assert.deepEqual(nextSort({ sort: "name", dir: "asc" }, columns[1]), { sort: "size", dir: "desc" });
});

test("pageItems shows the ends and two pages around the current one", () => {
  assert.deepEqual(pageItems(1, 1), [1]);
  assert.deepEqual(pageItems(3, 7), [1, 2, 3, 4, 5, 6, 7]);
  assert.deepEqual(pageItems(6, 20), [1, "gap", 4, 5, 6, 7, 8, "gap", 20]);
  assert.deepEqual(pageItems(1, 20), [1, 2, 3, "gap", 20]);
  assert.deepEqual(pageItems(20, 20), [1, "gap", 18, 19, 20]);
  assert.deepEqual(pageItems(4, 20), [1, 2, 3, 4, 5, 6, "gap", 20]);
});

test("pageKeepingRow keeps the first visible row on screen", () => {
  assert.strictEqual(pageKeepingRow(2, 25, 50), 1, "row 26 is on page 1 of 50");
  assert.strictEqual(pageKeepingRow(3, 25, 50), 2, "row 51 is on page 2 of 50");
  assert.strictEqual(pageKeepingRow(2, 100, 25), 5, "row 101 is on page 5 of 25");
  assert.strictEqual(pageKeepingRow(1, 50, 25), 1);
});

test("rangeText", () => {
  assert.strictEqual(rangeText(2, 25, 25, 112), "Showing 26–50 of 112");
  assert.strictEqual(rangeText(5, 25, 12, 112), "Showing 101–112 of 112");
  assert.strictEqual(rangeText(1, 25, 0, 0), "Showing 0 of 0");
});

test("sortRows sorts a copy, stably, numbers as numbers and text without case", () => {
  const rows: Row[] = [
    { name: "beta", size: 10 },
    { name: "Alpha", size: 9 },
    { name: "gamma", size: 10 },
  ];
  assert.deepEqual(
    sortRows(rows, columns[0], "asc").map((r) => r.name),
    ["Alpha", "beta", "gamma"],
  );
  assert.deepEqual(
    sortRows(rows, columns[1], "desc").map((r) => r.name),
    ["beta", "gamma", "Alpha"],
  );
  assert.strictEqual(rows[0].name, "beta", "the input is not sorted in place");
});

test("listQuery sends the state and the screen's filters in a fixed order", () => {
  const state = { q: " anna ", sort: "name", dir: "asc" as const, page: 2, size: 50 };
  assert.strictEqual(
    listQuery(state, { tag: "family", stale: "" }),
    "dir=asc&page=2&q=anna&size=50&sort=name&tag=family",
  );
});
