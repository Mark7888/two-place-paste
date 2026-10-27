// The Apple Event half of reopen_darwin.go.

#import <Cocoa/Cocoa.h>

#include "_cgo_export.h"

@interface TPPReopenHandler : NSObject
- (void)handleReopen:(NSAppleEventDescriptor *)event withReplyEvent:(NSAppleEventDescriptor *)reply;
@end

@implementation TPPReopenHandler

- (void)handleReopen:(NSAppleEventDescriptor *)event withReplyEvent:(NSAppleEventDescriptor *)reply {
	tppReopen();
}

@end

static TPPReopenHandler *tppReopenHandler;

void tppWatchReopen(void) {
	// On the main thread, and after NSApplication has finished launching:
	// it installs its own reopen handler while it launches, which would
	// otherwise replace this one.
	dispatch_async(dispatch_get_main_queue(), ^{
		if (tppReopenHandler == nil) {
			tppReopenHandler = [[TPPReopenHandler alloc] init];
		}
		[[NSAppleEventManager sharedAppleEventManager]
			setEventHandler:tppReopenHandler
			    andSelector:@selector(handleReopen:withReplyEvent:)
			  forEventClass:kCoreEventClass
			     andEventID:kAEReopenApplication];
	});
}
