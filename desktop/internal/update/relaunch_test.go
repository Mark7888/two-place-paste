package update

import (
	"slices"
	"testing"
)

func TestRelaunchArgs(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		show string
		want []string
	}{
		{"", []string{"--wait-pid", "42"}},
		{ShowUpdated, []string{"--wait-pid", "42", "--show-ui=updated"}},
		{ShowRolledBack, []string{"--wait-pid", "42", "--show-ui=rolled-back"}},
	} {
		if got := relaunchArgs(42, tc.show); !slices.Equal(got, tc.want) {
			t.Errorf("relaunchArgs(42, %q) = %q, want %q", tc.show, got, tc.want)
		}
	}
}

func TestParseShowUI(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		args []string
		want string
	}{
		{nil, ""},
		{[]string{"--wait-pid", "1"}, ""},
		{[]string{"--wait-pid", "1", "--show-ui=updated"}, ShowUpdated},
		{[]string{"--show-ui", "rolled-back"}, ShowRolledBack},
		{[]string{"-psn_0_123", "--show-ui=updated"}, ShowUpdated},
		{[]string{"--show-ui"}, ""},
		// Anything else would end up in the URL the browser opens.
		{[]string{"--show-ui=javascript:alert(1)"}, ""},
		{[]string{"--show-ui", "other"}, ""},
	} {
		if got := ParseShowUI(tc.args); got != tc.want {
			t.Errorf("ParseShowUI(%q) = %q, want %q", tc.args, got, tc.want)
		}
	}
}
