package buildinfo

import "testing"

func TestDefaultsDescribeALocalBuild(t *testing.T) {
	if !Local() {
		t.Fatalf("a test binary is not stamped by CI, but Local() = false (Channel %q)", Channel)
	}
	if got := StampValue(); got != 0 {
		t.Fatalf("StampValue() = %d, want 0 for a local build", got)
	}
}

func TestStampValue(t *testing.T) {
	saved := Stamp
	t.Cleanup(func() { Stamp = saved })

	for _, tc := range []struct {
		stamp string
		want  int64
	}{
		{"55786500", 55786500},
		{"", 0},
		{"soon", 0},
		{"-5", 0},
	} {
		Stamp = tc.stamp
		if got := StampValue(); got != tc.want {
			t.Errorf("StampValue() with Stamp %q = %d, want %d", tc.stamp, got, tc.want)
		}
	}
}
