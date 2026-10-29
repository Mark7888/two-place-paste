import { builtAt } from '../buildStamp';

describe('builtAt', () => {
  it.each([
    { stamp: 1, want: '2025-01-01T00:00:01.000Z' },
    { stamp: 86_400, want: '2025-01-02T00:00:00.000Z' },
    // 2026-10-29T12:00:00Z, the kind of value a CI build carries.
    { stamp: 57_585_600, want: '2026-10-29T12:00:00.000Z' },
  ])('reads stamp $stamp as $want', ({ stamp, want }) => {
    expect(builtAt(stamp)?.toISOString()).toBe(want);
  });

  it.each([0, -5, 1.5, Number.NaN])('treats %p as a local build', (stamp) => {
    expect(builtAt(stamp)).toBeNull();
  });
});
