package tray

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/xml"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync/atomic"
	"time"

	"git.sr.ht/~jackmordaunt/go-toast/v2/wintoast"
)

// Toasts on Windows.
//
// A toast with buttons that call back into a running desktop program needs
// three things, and wintoast does the COM half of them: an AppUserModelID
// registered under HKCU with a display name and a CustomActivator, a COM class
// registered under that CLSID while the process runs, and the toast XML. No
// installer step and no Start-menu shortcut is involved; the registry values
// are per-user and written on first use.
//
// Nothing is registered to relaunch the service from a toast. A button
// pressed after the service has quit does nothing, which is right: the sync it
// asked for has nothing to run in.

// toastAppID is the AppUserModelID, and also what Windows shows as the toast's
// sender.
const toastAppID = "TwoPlacePaste"

// toastGUID is the CLSID of this program's activation callback. It is this
// program's own: wintoast's default is shared by every program built on it,
// and Windows would hand one program's button presses to another.
const toastGUID = "{4FEA233F-D985-4B81-9F9F-E137B7C3F68D}"

// toastTimeout bounds one toast's hand-over to Windows.
const toastTimeout = 10 * time.Second

//go:embed notify_icon.png
var toastIcon []byte

// toastJob is one toast for the notifier thread to show.
type toastJob struct {
	xml  string
	done chan error
}

var (
	toastJobs  = make(chan toastJob)
	toastReady atomic.Bool
)

var errToastsNotReady = errors.New("tray: the notifier has not started")

// setupNotifications registers the app for toasts and starts the thread that
// shows them. onAction receives the arguments of whichever button the user
// presses, on a thread of Windows' choosing. Every failure is returned where
// it happens, so nothing reaches the second callback here.
func setupNotifications(ctx context.Context, onAction func(string), _ func(error)) error {
	icon, err := writeToastIcon()
	if err != nil {
		// A toast without the logo is still a toast.
		icon = ""
	}
	if err := wintoast.SetAppData(wintoast.AppData{
		AppID:    toastAppID,
		GUID:     toastGUID,
		IconPath: icon,
	}); err != nil {
		return fmt.Errorf("register for toast notifications: %w", err)
	}
	wintoast.SetActivationCallback(func(_, args string, _ []wintoast.UserData) {
		onAction(args)
	})
	go toastLoop(ctx)
	return nil
}

// toastLoop shows toasts from one OS thread, for the life of the service.
//
// The thread matters: the Windows Runtime is initialised per thread, and the
// activation class registered with each toast lives in that thread's
// apartment. A goroutine the scheduler moves between threads would initialise
// one and register in another.
func toastLoop(ctx context.Context) {
	runtime.LockOSThread()
	toastReady.Store(true)
	defer toastReady.Store(false)
	for {
		select {
		case <-ctx.Done():
			return
		case job := <-toastJobs:
			job.done <- wintoast.Push(toastAppID, job.xml)
		}
	}
}

// pushToast hands one toast to the notifier thread and waits for Windows to
// take it.
func pushToast(doc string) error {
	if !toastReady.Load() {
		return errToastsNotReady
	}
	job := toastJob{xml: doc, done: make(chan error, 1)}
	timeout := time.NewTimer(toastTimeout)
	defer timeout.Stop()
	select {
	case toastJobs <- job:
	case <-timeout.C:
		return errors.New("tray: the notifier is not answering")
	}
	select {
	case err := <-job.done:
		if err != nil {
			return fmt.Errorf("show a toast: %w", err)
		}
		return nil
	case <-timeout.C:
		return errors.New("tray: windows did not take the toast")
	}
}

// postPrompt shows the direction question with a button for each answer.
//
// It is short and silent on purpose: it answers a click the user has just
// made, so it needs no sound to be noticed, and it moves to the notification
// centre on its own rather than waiting to be dismissed.
func postPrompt() error {
	return pushToast(toastXML(promptTitle, promptBody,
		[2]string{uploadLabel, actionUpload},
		[2]string{downloadLabel, actionDownload},
	))
}

func postText(title, body string) error {
	return pushToast(toastXML(title, body))
}

// postSensitive asks whether to upload a protected entry, with one button.
func postSensitive(body string) error {
	return pushToast(toastXML(sensitiveTitle, body, [2]string{pushAnywayLabel, actionPushAnyway}))
}

// withdrawSensitivePrompt has nothing to do on Windows, as withdrawPrompt.
func withdrawSensitivePrompt() {}

// withdrawPrompt has nothing to do on Windows: wintoast cannot take a toast
// back, and one that was answered in the menu instead leaves the screen on
// its own a few seconds later.
func withdrawPrompt() {}

// toastXML builds a toast with a title, a body, and buttons given as label
// and arguments. Every value is escaped: the body of a failure toast is an
// error message, which can hold anything.
func toastXML(title, body string, actions ...[2]string) string {
	esc := func(s string) string {
		var b bytes.Buffer
		_ = xml.EscapeText(&b, []byte(s))
		return b.String()
	}
	var b bytes.Buffer
	b.WriteString(`<toast activationType="foreground" duration="short">`)
	b.WriteString(`<visual><binding template="ToastGeneric">`)
	fmt.Fprintf(&b, `<text hint-maxLines="1">%s</text>`, esc(title))
	if body != "" {
		fmt.Fprintf(&b, `<text>%s</text>`, esc(body))
	}
	b.WriteString(`</binding></visual>`)
	b.WriteString(`<audio silent="true"/>`)
	if len(actions) > 0 {
		b.WriteString(`<actions>`)
		for _, a := range actions {
			fmt.Fprintf(&b, `<action activationType="foreground" content="%s" arguments="%s"/>`,
				esc(a[0]), esc(a[1]))
		}
		b.WriteString(`</actions>`)
	}
	b.WriteString(`</toast>`)
	return b.String()
}

// writeToastIcon puts the toast logo where Windows can read it: toasts take
// an image path, not a resource in the exe.
func writeToastIcon() (string, error) {
	dir, err := os.UserCacheDir()
	if err != nil {
		return "", fmt.Errorf("locate the cache directory: %w", err)
	}
	dir = filepath.Join(dir, "TwoPlacePaste")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create the cache directory: %w", err)
	}
	path := filepath.Join(dir, "notify-icon.png")
	if err := os.WriteFile(path, toastIcon, 0o600); err != nil {
		return "", fmt.Errorf("write the notification icon: %w", err)
	}
	return path, nil
}
