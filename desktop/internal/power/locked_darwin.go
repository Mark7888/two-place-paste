//go:build darwin && cgo

package power

/*
#cgo LDFLAGS: -framework CoreGraphics -framework CoreFoundation

#include <CoreFoundation/CoreFoundation.h>
#include <CoreGraphics/CoreGraphics.h>

// tppTruth reads a session dictionary value that may be a CFBoolean or a
// CFNumber, depending on the macOS release: -1 when absent, else 0 or 1.
static int tppTruth(CFDictionaryRef d, CFStringRef key) {
	CFTypeRef v = CFDictionaryGetValue(d, key);
	if (v == NULL) {
		return -1;
	}
	if (CFGetTypeID(v) == CFBooleanGetTypeID()) {
		return CFBooleanGetValue((CFBooleanRef)v) ? 1 : 0;
	}
	if (CFGetTypeID(v) == CFNumberGetTypeID()) {
		int n = 0;
		CFNumberGetValue((CFNumberRef)v, kCFNumberIntType, &n);
		return n != 0 ? 1 : 0;
	}
	return -1;
}

static int tppScreenLocked(void) {
	CFDictionaryRef d = CGSessionCopyCurrentDictionary();
	if (d == NULL) {
		// Not in a GUI session at all — run from ssh, say. Nothing to pause
		// for, and nothing to gain from guessing.
		return 0;
	}
	int locked = tppTruth(d, CFSTR("CGSSessionScreenIsLocked")) == 1
		|| tppTruth(d, kCGSessionOnConsoleKey) == 0;
	CFRelease(d);
	return locked;
}
*/
import "C"

// screenLocked asks the window server about this login session: whether its
// screen is locked, and whether it is the one on the console at all. A session
// switched away from with fast user switching is not locked but has nobody at
// it either.
//
// It is one round trip to the window server and no process, which is what
// makes it cheap enough to ask before every clipboard poll.
func screenLocked() bool { return C.tppScreenLocked() != 0 }
