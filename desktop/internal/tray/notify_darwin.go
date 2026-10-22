package tray

/*
#cgo CFLAGS: -fobjc-arc
#cgo LDFLAGS: -framework Foundation -framework UserNotifications

#include <stdlib.h>

int tppNotifyAvailable(void);
void tppNotifySetup(const char *uploadTitle, const char *downloadTitle, const char *pushTitle);
void tppNotifyPost(const char *ident, const char *title, const char *body, const char *category);
void tppNotifyRemove(const char *ident);
*/
import "C"

import (
	"context"
	"errors"
	"sync"
	"unsafe"
)

// Notifications on macOS.
//
// UNUserNotificationCenter carries the buttons: the direction prompt is posted
// in a category with an Upload and a Download action, and the protected entry
// question in one with a Push anyway action, which macOS shows on the banner;
// the delegate in notify_darwin.m hands the one pressed back to notifyAction. The first notification asks the user for permission; one
// they refuse is logged, and the tray menu still offers the choice.
//
// The framework only works for a process inside an app bundle — it throws
// for a bare binary, which is how a developer runs this from a checkout — so
// a build without a bundle identifier has no notifications at all.

// The identifiers of the two kinds of notification this program posts. A new
// one replaces whatever is on screen under the same identifier, so a second
// "Sync now" does not stack a second question under the first.
const (
	promptIdent    = "tpp.sync.direction"
	sensitiveIdent = "tpp.sync.sensitive"
	textIdent      = "tpp.sync.result"
)

// The notification categories, which decide the buttons. They match
// notify_darwin.m.
const (
	directionCategory = "tpp.sync.direction"
	sensitiveCategory = "tpp.sync.sensitive"
)

var errNoBundle = errors.New("tray: notifications need the app bundle")

// What the Objective-C side calls back into, set once by setupNotifications.
var (
	notifyMu        sync.Mutex
	notifyOnAction  func(string)
	notifyOnFailure func(error)
)

func setupNotifications(_ context.Context, onAction func(string), onFailure func(error)) error {
	if C.tppNotifyAvailable() == 0 {
		return errNoBundle
	}
	notifyMu.Lock()
	notifyOnAction, notifyOnFailure = onAction, onFailure
	notifyMu.Unlock()

	up, down, push := C.CString(uploadLabel), C.CString(downloadLabel), C.CString(pushAnywayLabel)
	defer C.free(unsafe.Pointer(up))
	defer C.free(unsafe.Pointer(down))
	defer C.free(unsafe.Pointer(push))
	C.tppNotifySetup(up, down, push)
	return nil
}

func postPrompt() error { return post(promptIdent, promptTitle, promptBody, directionCategory) }

func postSensitive(body string) error {
	return post(sensitiveIdent, sensitiveTitle, body, sensitiveCategory)
}

func postText(title, body string) error { return post(textIdent, title, body, "") }

func withdrawPrompt() { remove(promptIdent) }

func withdrawSensitivePrompt() { remove(sensitiveIdent) }

func remove(ident string) {
	if C.tppNotifyAvailable() == 0 {
		return
	}
	id := C.CString(ident)
	defer C.free(unsafe.Pointer(id))
	C.tppNotifyRemove(id)
}

// post hands a notification to the notification centre. It returns before
// macOS has shown it — permission is asked for asynchronously — so a refusal
// arrives at tppNotifyFailed rather than here.
//
// category names the buttons; empty means none.
func post(ident, title, body, category string) error {
	if C.tppNotifyAvailable() == 0 {
		return errNoBundle
	}
	cIdent, cTitle, cBody, cCategory := C.CString(ident), C.CString(title), C.CString(body), C.CString(category)
	defer C.free(unsafe.Pointer(cIdent))
	defer C.free(unsafe.Pointer(cTitle))
	defer C.free(unsafe.Pointer(cBody))
	defer C.free(unsafe.Pointer(cCategory))
	C.tppNotifyPost(cIdent, cTitle, cBody, cCategory)
	return nil
}

//export tppNotifyAction
func tppNotifyAction(action *C.char) {
	a := C.GoString(action)
	notifyMu.Lock()
	fn := notifyOnAction
	notifyMu.Unlock()
	if fn != nil {
		// Off the notification centre's queue: the choice starts a sync.
		go fn(a)
	}
}

//export tppNotifyFailed
func tppNotifyFailed(reason *C.char) {
	err := errors.New(C.GoString(reason))
	notifyMu.Lock()
	fn := notifyOnFailure
	notifyMu.Unlock()
	if fn != nil {
		fn(err)
	}
}
