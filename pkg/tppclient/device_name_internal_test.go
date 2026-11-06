package tppclient

import "testing"

func TestSetDeviceName(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		set  string
		want string
	}{
		{name: "renamed", set: "work laptop", want: "work laptop"},
		{name: "empty is ignored", set: "", want: "test"},
		{name: "unchanged", set: "test", want: "test"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			c, err := New(Options{DeviceName: "test"})
			if err != nil {
				t.Fatalf("New() error = %v", err)
			}
			t.Cleanup(func() { _ = c.Close() })

			if err := c.SetDeviceName(tt.set); err != nil {
				t.Fatalf("SetDeviceName() error = %v", err)
			}
			if got := c.State().DeviceName; got != tt.want {
				t.Errorf("DeviceName = %q, want %q", got, tt.want)
			}
		})
	}
}
