import { percent } from "../lib/format";

/** UsageMeter is a usage bar. It is a <meter>, so no inline style sizes it,
 *  which is what the page's style-src 'self' policy requires; past nine
 *  tenths of the limit it turns red. */
export function UsageMeter({ used, limit, label }: { used: number; limit: number; label: string }) {
  if (limit <= 0) return null;
  return (
    <meter min={0} max={limit} value={used} high={Math.floor((limit * 9) / 10)} optimum={0} aria-label={label}>
      {percent(used, limit)}%
    </meter>
  );
}
