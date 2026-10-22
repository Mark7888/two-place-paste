// How the admin screens write sizes, times and counts. The API sends bytes and
// RFC 3339 UTC times; everything a person reads is made here, so every screen
// says "3.4 MiB" and "12 min ago" the same way.

const MINUTE = 60_000;
const HOUR = 60 * MINUTE;
const DAY = 24 * HOUR;

/** plural picks the word for a count: plural(1, "entry", "entries"). */
export function plural(n: number, one: string, many: string): string {
  return n === 1 ? one : many;
}

/** humanBytes renders a byte count in binary units: "0 B", "512 B", "3.4 MiB". */
export function humanBytes(n: number): string {
  const unit = 1024;
  if (n < unit) return `${n} B`;
  let div = unit;
  let exp = 0;
  for (let m = Math.floor(n / unit); m >= unit && exp < 5; m = Math.floor(m / unit)) {
    div *= unit;
    exp++;
  }
  const v = n / div;
  const prefix = "KMGTPE"[exp];
  return v >= 100 ? `${v.toFixed(0)} ${prefix}iB` : `${v.toFixed(1)} ${prefix}iB`;
}

/** limitText is a storage limit, where zero or less means none. */
export function limitText(n: number): string {
  return n <= 0 ? "no limit" : humanBytes(n);
}

/** sizeInput renders a byte count the way the limit fields accept it back. */
export function sizeInput(n: number): string {
  const units: [number, string][] = [
    [2 ** 40, "TiB"],
    [2 ** 30, "GiB"],
    [2 ** 20, "MiB"],
    [2 ** 10, "KiB"],
  ];
  if (n === 0) return "0";
  for (const [size, name] of units) {
    if (n % size === 0) return `${n / size} ${name}`;
  }
  return String(n);
}

/** percent is used/limit as a whole percentage, capped at 100; 0 when there
 *  is no limit. */
export function percent(used: number, limit: number): number {
  if (limit <= 0 || used <= 0) return 0;
  return Math.min(100, Math.floor((used * 100) / limit));
}

/** ago renders how long before now a time was, coarsely: "just now",
 *  "12 min ago", "3 h ago", "2 days ago". A missing time renders as zero. */
export function ago(now: number, at: string | null | undefined, zero: string): string {
  if (!at) return zero;
  const d = now - Date.parse(at);
  if (d < MINUTE) return "just now";
  if (d < HOUR) return `${Math.floor(d / MINUTE)} min ago`;
  if (d < 2 * DAY) return `${Math.floor(d / HOUR)} h ago`;
  return `${Math.floor(d / DAY)} days ago`;
}

const pad = (n: number) => String(n).padStart(2, "0");

/** formatDate is a time's UTC date: "2026-09-09". */
export function formatDate(at: string): string {
  const d = new Date(at);
  return `${d.getUTCFullYear()}-${pad(d.getUTCMonth() + 1)}-${pad(d.getUTCDate())}`;
}

/** formatDateTime is a time in UTC to the minute: "2026-09-09 12:00 UTC". */
export function formatDateTime(at: string): string {
  const d = new Date(at);
  return `${formatDate(at)} ${pad(d.getUTCHours())}:${pad(d.getUTCMinutes())} UTC`;
}

/** uptime renders how long the server has run, coarsely. */
export function uptime(seconds: number): string {
  const ms = seconds * 1000;
  if (ms < MINUTE) return "less than a minute";
  if (ms < HOUR) {
    const n = Math.floor(ms / MINUTE);
    return n === 1 ? "1 minute" : `${n} minutes`;
  }
  if (ms < 2 * DAY) {
    const n = Math.floor(ms / HOUR);
    return n === 1 ? "1 hour" : `${n} hours`;
  }
  return `${Math.floor(ms / DAY)} days`;
}

/** days renders a whole number of days: "1 day", "90 days". */
export function days(n: number): string {
  return `${n} ${plural(n, "day", "days")}`;
}

/** duration renders a whole number of milliseconds the way Go prints a
 *  time.Duration rounded to the millisecond: "3ms", "1.25s", "2m3.5s". */
export function duration(ms: number): string {
  if (ms === 0) return "0s";
  if (ms < 1000) return `${ms}ms`;
  const h = Math.floor(ms / 3_600_000);
  const m = Math.floor((ms % 3_600_000) / 60_000);
  const rest = ms % 60_000;
  const whole = Math.floor(rest / 1000);
  const frac = String(rest % 1000).padStart(3, "0").replace(/0+$/, "");
  const s = `${whole}${frac ? "." + frac : ""}s`;
  if (h > 0) return `${h}h${m}m${s}`;
  if (m > 0) return `${m}m${s}`;
  return s;
}
