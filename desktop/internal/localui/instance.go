package localui

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// One copy of the service runs at a time: the second one cannot bind the
// port. Starting the app again is how people look for it, though, so a second
// copy asks the running one to open the app in the browser, as the tray's
// "Open TwoPlacePaste" would, and exits.
//
// The request needs a secret like every other one here, but not the launch
// token: that never touches the disk, and a second process has no other way
// to learn it. The running copy writes a separate per-launch instance key to
// a file only this user can read, and that key does one thing — open the app
// in this user's own browser. Whoever sends it learns nothing; the URL with
// the launch token goes to the browser, not back to them.

// InstanceKeyFile is the name of the instance key's file, beside the settings.
const InstanceKeyFile = "instance.key"

// instanceHeader carries the instance key.
const instanceHeader = "X-TPP-Instance-Key"

// instancePath is the one route the instance key opens.
const instancePath = "/instance/show"

// showInterval is the least time between two opens a second copy asks for,
// so a launcher that starts the app over and over cannot pile up browser tabs.
const showInterval = 2 * time.Second

// showTimeout bounds the second copy's request: the running one is on this
// machine and answers at once, or not at all.
const showTimeout = 3 * time.Second

// ErrNotRunning reports that no running copy answered the request to show
// itself: none is running, or something else holds the port.
var ErrNotRunning = errors.New("localui: no running copy answered")

// WriteInstanceKey writes key, from NewToken, to path, readable only by this
// user, and returns a function that removes the file again. Write it only
// once this copy holds the port: a copy that failed to bind would otherwise
// replace the running one's key with a key nothing answers to.
func WriteInstanceKey(path, key string) (func(), error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("localui: create the instance key's folder: %w", err)
	}
	// Removed first: WriteFile keeps the mode of a file that already exists.
	_ = os.Remove(path)
	if err := os.WriteFile(path, []byte(key), 0o600); err != nil {
		return nil, fmt.Errorf("localui: write the instance key: %w", err)
	}
	return func() { removeInstanceKey(path, key) }, nil
}

// removeInstanceKey removes the file only if it still holds key: a copy that
// started later may have written its own.
func removeInstanceKey(path, key string) {
	b, err := os.ReadFile(path)
	if err == nil && string(b) == key {
		_ = os.Remove(path)
	}
}

// ShowRunning asks the copy running on port to open the app in the browser,
// with the instance key in the file at keyPath. It returns ErrNotRunning when
// there is no key or nothing answered as a running copy would.
func ShowRunning(ctx context.Context, keyPath string, port int) error {
	b, err := os.ReadFile(keyPath)
	if err != nil {
		return fmt.Errorf("%w: read the instance key: %w", ErrNotRunning, err)
	}
	key := strings.TrimSpace(string(b))
	if key == "" {
		return fmt.Errorf("%w: the instance key is empty", ErrNotRunning)
	}

	ctx, cancel := context.WithTimeout(ctx, showTimeout)
	defer cancel()
	u := "http://" + net.JoinHostPort("127.0.0.1", strconv.Itoa(port)) + instancePath
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, nil)
	if err != nil {
		return fmt.Errorf("localui: build the request to show the running copy: %w", err)
	}
	req.Header.Set(instanceHeader, key)
	// No proxy: this is a loopback request, and a proxy from the environment
	// would carry the key off the machine.
	client := &http.Client{Transport: &http.Transport{Proxy: nil}}
	res, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrNotRunning, err)
	}
	defer func() { _ = res.Body.Close() }()
	_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, 4<<10))
	if res.StatusCode != http.StatusNoContent {
		return fmt.Errorf("%w: it answered %d", ErrNotRunning, res.StatusCode)
	}
	return nil
}

// shower opens the app when a second copy asks, at most once per showInterval.
type shower struct {
	key string
	// show opens the app. It must not block: it runs in the request.
	show func()
	now  func() time.Time

	mu   sync.Mutex
	last time.Time
}

// ServeHTTP answers a second copy's request to show the app.
//
// Only another program can make it. A page in the browser cannot read the key,
// and is refused before the key is looked at: a browser sends Origin or
// Sec-Fetch-Site on a cross-site request, and the custom header alone makes
// it ask permission first with a preflight, which is never answered.
func (sh *shower) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if r.Header.Get("Origin") != "" || r.Header.Get("Sec-Fetch-Site") != "" {
		writeError(w, http.StatusForbidden, "forbidden origin")
		return
	}
	got := r.Header.Get(instanceHeader)
	if sh.key == "" || subtle.ConstantTimeCompare([]byte(got), []byte(sh.key)) != 1 {
		writeError(w, http.StatusUnauthorized, "missing or invalid instance key")
		return
	}
	sh.mu.Lock()
	now := sh.now()
	due := sh.last.IsZero() || now.Sub(sh.last) >= showInterval
	if due {
		sh.last = now
	}
	sh.mu.Unlock()
	if due {
		sh.show()
	}
	w.WriteHeader(http.StatusNoContent)
}
