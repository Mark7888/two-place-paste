//go:build !darwin || !cgo

package update

import "time"

// startSchedule calls fire every fallbackTick until the returned stop is
// called. Each call is cheap: Run only compares the wall clock with the last
// check. interval and tolerance are the macOS scheduler's and unused here.
func startSchedule(_, _ time.Duration, fire func()) (stop func()) {
	done := make(chan struct{})
	go func() {
		t := time.NewTicker(fallbackTick)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-t.C:
				fire()
			}
		}
	}()
	return func() { close(done) }
}
