//go:build darwin && cgo

package update

import "C"

// tppUpdateActivityFired is called by the NSBackgroundActivityScheduler block
// in schedule_darwin.go. It lives in its own file because a file with an
// //export may only declare C, not define it.
//
//export tppUpdateActivityFired
func tppUpdateActivityFired() {
	if fire := activityHook.Load(); fire != nil {
		(*fire)()
	}
}
