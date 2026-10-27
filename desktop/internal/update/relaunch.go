package update

import (
	"strconv"
	"strings"
)

// showUIFlag tells a relaunched build to open the app in the browser once it
// is up, and why. Only an update or rollback a person started from the UI
// passes it: they were looking at the page the old build served, which stops
// working when it exits, so the new build hands them a fresh one. An
// automatic update passes nothing — it waits until nobody is using the
// machine, and a browser tab appearing out of nowhere is not welcome then.
//
// A build from before this flag ignores it, since the only flag it reads is
// --wait-pid; rolling back to one simply opens nothing.
const showUIFlag = "--show-ui"

// Why the relaunched build opens the app. Each one is also the fragment of
// the URL it opens, where the page reads it to say what happened.
const (
	ShowUpdated    = "updated"
	ShowRolledBack = "rolled-back"
)

// relaunchArgs are the arguments a relaunched build is started with: the PID
// to wait for, and, when show is not empty, why it should open the app.
func relaunchArgs(pid int, show string) []string {
	args := []string{waitPIDFlag, strconv.Itoa(pid)}
	if show != "" {
		args = append(args, showUIFlag+"="+show)
	}
	return args
}

// ParseShowUI returns the reason given with --show-ui=REASON or --show-ui
// REASON, or "" when there is none or it is not one this build knows.
func ParseShowUI(args []string) string {
	for i, a := range args {
		var v string
		switch {
		case a == showUIFlag && i+1 < len(args):
			v = args[i+1]
		case strings.HasPrefix(a, showUIFlag+"="):
			v = strings.TrimPrefix(a, showUIFlag+"=")
		default:
			continue
		}
		switch v {
		case ShowUpdated, ShowRolledBack:
			return v
		}
	}
	return ""
}
