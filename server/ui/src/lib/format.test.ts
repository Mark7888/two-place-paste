// Run with `npm test` (node --test). The cases are the ones the Go code these
// helpers replace printed, so a screen reads the same as it did.
import assert from "node:assert/strict";
import { test } from "node:test";
import { ago, days, duration, formatDate, formatDateTime, humanBytes, limitText, percent, sizeInput, uptime } from "./format.ts";

test("humanBytes", () => {
  const cases: [number, string][] = [
    [0, "0 B"],
    [512, "512 B"],
    [1023, "1023 B"],
    [1024, "1.0 KiB"],
    [4096, "4.0 KiB"],
    [50 * 1024, "50.0 KiB"],
    [100 * 1024, "100 KiB"],
    [30 * 1024 * 1024, "30.0 MiB"],
    [5 * 2 ** 30, "5.0 GiB"],
    [3.4 * 2 ** 20, "3.4 MiB"],
  ];
  for (const [n, want] of cases) assert.equal(humanBytes(n), want, `humanBytes(${n})`);
});

test("limitText", () => {
  assert.equal(limitText(0), "no limit");
  assert.equal(limitText(-1), "no limit");
  assert.equal(limitText(40 * 1024), "40.0 KiB");
});

test("sizeInput round-trips what the limit field accepts", () => {
  const cases: [number, string][] = [
    [0, "0"],
    [5 * 2 ** 30, "5 GiB"],
    [2 * 2 ** 40, "2 TiB"],
    [3 * 2 ** 20, "3 MiB"],
    [1024, "1 KiB"],
    [500_000_000, "500000000"],
  ];
  for (const [n, want] of cases) assert.equal(sizeInput(n), want, `sizeInput(${n})`);
});

test("percent", () => {
  assert.equal(percent(0, 100), 0);
  assert.equal(percent(50, 0), 0);
  assert.equal(percent(40, 40), 100);
  assert.equal(percent(80, 40), 100);
  assert.equal(percent(1, 3), 33);
});

test("ago", () => {
  const now = Date.parse("2026-09-09T12:00:00Z");
  assert.equal(ago(now, null, "never"), "never");
  assert.equal(ago(now, "2026-09-09T11:59:30Z", ""), "just now");
  assert.equal(ago(now, "2026-09-09T11:50:00Z", ""), "10 min ago");
  assert.equal(ago(now, "2026-09-08T12:00:00Z", ""), "24 h ago");
  assert.equal(ago(now, "2026-09-01T12:00:00Z", ""), "8 days ago");
});

test("dates are UTC", () => {
  assert.equal(formatDate("2026-09-09T23:30:00-02:00"), "2026-09-10");
  assert.equal(formatDateTime("2026-09-09T12:05:00Z"), "2026-09-09 12:05 UTC");
});

test("uptime", () => {
  assert.equal(uptime(30), "less than a minute");
  assert.equal(uptime(60), "1 minute");
  assert.equal(uptime(600), "10 minutes");
  assert.equal(uptime(3600), "1 hour");
  assert.equal(uptime(47 * 3600), "47 hours");
  assert.equal(uptime(3 * 86400), "3 days");
});

test("days", () => {
  assert.equal(days(1), "1 day");
  assert.equal(days(90), "90 days");
});

test("duration prints like Go's time.Duration", () => {
  assert.equal(duration(0), "0s");
  assert.equal(duration(3), "3ms");
  assert.equal(duration(1000), "1s");
  assert.equal(duration(1250), "1.25s");
  assert.equal(duration(123_500), "2m3.5s");
  assert.equal(duration(3_723_000), "1h2m3s");
});
