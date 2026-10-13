//go:build darwin && cgo

package update

/*
#cgo CFLAGS: -x objective-c -fobjc-arc
#cgo LDFLAGS: -framework Foundation

#import <Foundation/Foundation.h>

// Defined in schedule_darwin_export.go.
extern void tppUpdateActivityFired(void);

static NSBackgroundActivityScheduler *tppScheduler;

// tppScheduleUpdateChecks asks macOS to run the update check about every
// interval seconds, give or take tolerance. The system picks the moment: it
// coalesces the work with other wake-ups and defers it on battery, under
// thermal pressure, and while the user is busy. Low Power Mode defers it too.
static void tppScheduleUpdateChecks(double interval, double tolerance) {
	@autoreleasepool {
		[tppScheduler invalidate];
		NSBackgroundActivityScheduler *s = [[NSBackgroundActivityScheduler alloc]
			initWithIdentifier:@"com.twoplacepaste.desktop.update-check"];
		s.repeats = YES;
		s.interval = interval;
		s.tolerance = tolerance;
		s.qualityOfService = NSQualityOfServiceUtility;
		[s scheduleWithBlock:^(NSBackgroundActivityCompletionHandler completion) {
			if ([[NSProcessInfo processInfo] isLowPowerModeEnabled]) {
				completion(NSBackgroundActivityResultDeferred);
				return;
			}
			tppUpdateActivityFired();
			completion(NSBackgroundActivityResultFinished);
		}];
		tppScheduler = s;
	}
}

static void tppStopUpdateChecks(void) {
	@autoreleasepool {
		[tppScheduler invalidate];
		tppScheduler = nil;
	}
}
*/
import "C"

import (
	"sync/atomic"
	"time"
)

// activityHook is what the scheduler's block calls into, through
// tppUpdateActivityFired. It must not block: the block runs on a system queue.
var activityHook atomic.Pointer[func()]

func startSchedule(interval, tolerance time.Duration, fire func()) (stop func()) {
	activityHook.Store(&fire)
	C.tppScheduleUpdateChecks(C.double(interval.Seconds()), C.double(tolerance.Seconds()))
	return func() {
		C.tppStopUpdateChecks()
		activityHook.Store(nil)
	}
}
