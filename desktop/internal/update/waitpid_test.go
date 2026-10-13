package update

import (
	"os"
	"os/exec"
	"runtime"
	"testing"
	"time"
)

func TestParseWaitPID(t *testing.T) {
	for _, tc := range []struct {
		args []string
		want int
	}{
		{nil, 0},
		{[]string{"--wait-pid", "123"}, 123},
		{[]string{"--wait-pid=456"}, 456},
		{[]string{"-psn_0_123", "--wait-pid", "789"}, 789},
		{[]string{"--wait-pid"}, 0},
		{[]string{"--wait-pid", "abc"}, 0},
		{[]string{"--wait-pid", "-4"}, 0},
	} {
		if got := ParseWaitPID(tc.args); got != tc.want {
			t.Errorf("ParseWaitPID(%q) = %d, want %d", tc.args, got, tc.want)
		}
	}
}

func TestWaitForExit(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses sleep(1)")
	}
	if WaitForExit(os.Getpid(), 200*time.Millisecond) {
		t.Fatal("WaitForExit reported this running process as gone")
	}
	cmd := exec.Command("sleep", "0.3")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { _ = cmd.Wait(); close(done) }()
	if !WaitForExit(cmd.Process.Pid, 5*time.Second) {
		<-done
		t.Fatal("WaitForExit did not see the child exit")
	}
}
