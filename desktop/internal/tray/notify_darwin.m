// The UserNotifications half of notify_darwin.go.

#import <Foundation/Foundation.h>
#import <UserNotifications/UserNotifications.h>

#include "_cgo_export.h"

// The categories are notify_darwin.go's directionCategory and
// sensitiveCategory.
static NSString *const TPPCategoryDirection = @"tpp.sync.direction";
static NSString *const TPPCategorySensitive = @"tpp.sync.sensitive";

// The action identifiers are tray.go's actionUpload, actionDownload and
// actionPushAnyway, which it maps back to what the user chose.
static NSString *const TPPActionUpload = @"upload";
static NSString *const TPPActionDownload = @"download";
static NSString *const TPPActionPushAnyway = @"push-anyway";

@interface TPPNotifyDelegate : NSObject <UNUserNotificationCenterDelegate>
@end

@implementation TPPNotifyDelegate

// An agent app counts as frontmost often enough that macOS would otherwise
// swallow the banner: show it whatever the app's state.
- (void)userNotificationCenter:(UNUserNotificationCenter *)center
       willPresentNotification:(UNNotification *)notification
         withCompletionHandler:(void (^)(UNNotificationPresentationOptions))completionHandler {
	completionHandler(UNNotificationPresentationOptionBanner | UNNotificationPresentationOptionList);
}

- (void)userNotificationCenter:(UNUserNotificationCenter *)center
didReceiveNotificationResponse:(UNNotificationResponse *)response
         withCompletionHandler:(void (^)(void))completionHandler {
	tppNotifyAction((char *)[response.actionIdentifier UTF8String]);
	completionHandler();
}

@end

static TPPNotifyDelegate *tppDelegate;

int tppNotifyAvailable(void) {
	// currentNotificationCenter raises for a process outside a bundle.
	return [[NSBundle mainBundle] bundleIdentifier] != nil;
}

void tppNotifySetup(const char *uploadTitle, const char *downloadTitle, const char *pushTitle) {
	@autoreleasepool {
		UNUserNotificationCenter *center = [UNUserNotificationCenter currentNotificationCenter];
		if (tppDelegate == nil) {
			tppDelegate = [[TPPNotifyDelegate alloc] init];
		}
		// The center holds its delegate weakly; tppDelegate keeps it alive.
		center.delegate = tppDelegate;

		// No foreground option: answering runs a sync in the background and
		// has no window to bring forward.
		UNNotificationAction *upload =
			[UNNotificationAction actionWithIdentifier:TPPActionUpload
			                                     title:[NSString stringWithUTF8String:uploadTitle]
			                                   options:UNNotificationActionOptionNone];
		UNNotificationAction *download =
			[UNNotificationAction actionWithIdentifier:TPPActionDownload
			                                     title:[NSString stringWithUTF8String:downloadTitle]
			                                   options:UNNotificationActionOptionNone];
		UNNotificationCategory *direction =
			[UNNotificationCategory categoryWithIdentifier:TPPCategoryDirection
			                                       actions:@[ upload, download ]
			                             intentIdentifiers:@[]
			                                       options:UNNotificationCategoryOptionNone];
		UNNotificationAction *push =
			[UNNotificationAction actionWithIdentifier:TPPActionPushAnyway
			                                     title:[NSString stringWithUTF8String:pushTitle]
			                                   options:UNNotificationActionOptionNone];
		UNNotificationCategory *sensitive =
			[UNNotificationCategory categoryWithIdentifier:TPPCategorySensitive
			                                       actions:@[ push ]
			                             intentIdentifiers:@[]
			                                       options:UNNotificationCategoryOptionNone];
		[center setNotificationCategories:[NSSet setWithObjects:direction, sensitive, nil]];
	}
}

void tppNotifyPost(const char *ident, const char *title, const char *body, const char *category) {
	@autoreleasepool {
		// Copied now: the C strings are freed as soon as this returns, and
		// the blocks below run later.
		NSString *identifier = [NSString stringWithUTF8String:ident];
		NSString *titleText = [NSString stringWithUTF8String:title];
		NSString *bodyText = [NSString stringWithUTF8String:body];
		NSString *categoryIdentifier = [NSString stringWithUTF8String:category];

		UNUserNotificationCenter *center = [UNUserNotificationCenter currentNotificationCenter];
		// Asks the first time only; afterwards it answers with the user's
		// setting at once.
		[center requestAuthorizationWithOptions:UNAuthorizationOptionAlert
		                      completionHandler:^(BOOL granted, NSError *_Nullable error) {
			if (!granted) {
				NSString *why = error != nil ? error.localizedDescription
				                             : @"notifications are turned off for TwoPlacePaste";
				tppNotifyFailed((char *)[why UTF8String]);
				return;
			}
			UNMutableNotificationContent *content = [[UNMutableNotificationContent alloc] init];
			content.title = titleText;
			content.body = bodyText;
			if (categoryIdentifier.length > 0) {
				content.categoryIdentifier = categoryIdentifier;
			}
			UNNotificationRequest *request = [UNNotificationRequest requestWithIdentifier:identifier
			                                                                      content:content
			                                                                      trigger:nil];
			[center addNotificationRequest:request
			         withCompletionHandler:^(NSError *_Nullable addError) {
				if (addError != nil) {
					tppNotifyFailed((char *)[addError.localizedDescription UTF8String]);
				}
			}];
		}];
	}
}

void tppNotifyRemove(const char *ident) {
	@autoreleasepool {
		NSString *identifier = [NSString stringWithUTF8String:ident];
		[[UNUserNotificationCenter currentNotificationCenter]
			removeDeliveredNotificationsWithIdentifiers:@[ identifier ]];
	}
}
