package power

import (
	"testing"
	"time"
)

func TestSlept(t *testing.T) {
	t.Parallel()

	base := time.Date(2026, 10, 2, 9, 0, 0, 0, time.UTC)
	cases := []struct {
		name    string
		wall    time.Duration
		elapsed time.Duration
		want    time.Duration
	}{
		{"awake", 5 * time.Second, 5 * time.Second, 0},
		{"a late tick is not sleep", 9 * time.Second, 9 * time.Second, 0},
		{"a small clock step is not sleep", 10 * time.Second, 5 * time.Second, 0},
		{"an hour asleep", time.Hour + 5*time.Second, 5 * time.Second, time.Hour},
		{"the clock set back", 0, 5 * time.Second, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := slept(base, base.Add(tc.wall), tc.elapsed); got != tc.want {
				t.Errorf("slept() = %v, want %v", got, tc.want)
			}
		})
	}
}
