export function bytes(n: number): string {
  if (n < 1024) return `${n} B`;
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)} KB`;
  return `${(n / (1024 * 1024)).toFixed(1)} MB`;
}

export function when(iso?: string): string {
  if (!iso) return "—";
  const t = new Date(iso);
  if (Number.isNaN(t.getTime()) || t.getTime() === 0) return "—";
  return t.toLocaleString();
}

export function relative(iso?: string): string {
  if (!iso) return "never";
  const t = new Date(iso).getTime();
  if (!t) return "never";
  const secs = Math.round((Date.now() - t) / 1000);
  if (secs < 60) return "just now";
  if (secs < 3600) return `${Math.round(secs / 60)} min ago`;
  if (secs < 86400) return `${Math.round(secs / 3600)} h ago`;
  return when(iso);
}
