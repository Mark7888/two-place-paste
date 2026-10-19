/**
 * Splitting the third-party notices into the pieces the licenses sheet
 * renders.
 *
 * The file is several hundred kilobytes — one license text per library — and a
 * single <Text> that long is slow to lay out and scroll. scripts/notices
 * writes each component under a header fenced by lines of 80 "=", so the text
 * splits on those fences into the overview and one piece per component, and
 * the sheet renders them as a list.
 */

const FENCE = '='.repeat(80);

/** splitNotices returns the overview first, then one string per component (its header and its text). */
export function splitNotices(text: string): string[] {
  const parts = text.replace(/\r\n/g, '\n').split(`\n${FENCE}\n`);
  const pieces = [parts[0].trim()];
  // After the overview, parts alternate: a component's header, then its text.
  for (let i = 1; i < parts.length; i += 2) {
    const header = parts[i].trim();
    const body = (parts[i + 1] ?? '').trim();
    pieces.push(body === '' ? header : `${header}\n\n${body}`);
  }
  return pieces.filter((p) => p !== '');
}
