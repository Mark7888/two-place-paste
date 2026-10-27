package localui

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

// instanceServer is a served Listen with the instance route on, and a channel
// that receives each URL it asks to open.
func instanceServer(t *testing.T, key string) (*Server, chan string) {
	t.Helper()
	shown := make(chan string, 4)
	api := newStubAPI()
	srv, err := Listen(context.Background(), Options{
		API: api, Port: 0, Token: "test-token",
		InstanceKey: key,
		OnShow:      func(url string) { shown <- url },
	})
	if err != nil {
		t.Fatalf("Listen() error = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = srv.Serve(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})
	return srv, shown
}

func TestShowRunningOpensTheRunningCopy(t *testing.T) {
	t.Parallel()
	key, err := NewToken()
	if err != nil {
		t.Fatal(err)
	}
	srv, shown := instanceServer(t, key)
	path := filepath.Join(t.TempDir(), InstanceKeyFile)
	remove, err := WriteInstanceKey(path, key)
	if err != nil {
		t.Fatalf("WriteInstanceKey() error = %v", err)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if got, want := info.Mode().Perm(), os.FileMode(0o600); got != want {
			t.Errorf("instance key mode = %v, want %v", got, want)
		}
	}

	if err := ShowRunning(context.Background(), path, srv.Port()); err != nil {
		t.Fatalf("ShowRunning() error = %v, want nil", err)
	}
	select {
	case got := <-shown:
		if got != srv.URL() {
			t.Errorf("opened a URL other than the app's")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the running copy was never asked to open the app")
	}

	remove()
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("after remove, stat error = %v, want not exist", err)
	}
}

func TestShowRunningWithoutARunningCopy(t *testing.T) {
	t.Parallel()
	key := "a-key"
	srv, _ := instanceServer(t, key)
	dir := t.TempDir()

	missing := filepath.Join(dir, "missing.key")
	wrong := filepath.Join(dir, "wrong.key")
	if _, err := WriteInstanceKey(wrong, "another-key"); err != nil {
		t.Fatal(err)
	}
	right := filepath.Join(dir, "right.key")
	if _, err := WriteInstanceKey(right, key); err != nil {
		t.Fatal(err)
	}
	// A port nothing listens on: bound, then released.
	idle, err := Listen(context.Background(), Options{API: newStubAPI(), Port: 0})
	if err != nil {
		t.Fatal(err)
	}
	idlePort := idle.Port()
	_ = idle.Close()

	for _, tc := range []struct {
		name string
		path string
		port int
	}{
		{"no key file", missing, srv.Port()},
		{"a key from another launch", wrong, srv.Port()},
		{"nothing on the port", right, idlePort},
	} {
		err := ShowRunning(context.Background(), tc.path, tc.port)
		if !errors.Is(err, ErrNotRunning) {
			t.Errorf("%s: ShowRunning() error = %v, want ErrNotRunning", tc.name, err)
		}
	}
}

// ask sends sh one request with headers and returns its status.
func ask(sh *shower, headers map[string]string) int {
	req := httptest.NewRequest(http.MethodPost, instancePath, nil)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	sh.ServeHTTP(rec, req)
	return rec.Code
}

func TestTheInstanceRouteRefusesBrowsers(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		headers map[string]string
		want    int
	}{
		{"no key", nil, http.StatusUnauthorized},
		{"wrong key", map[string]string{instanceHeader: "x"}, http.StatusUnauthorized},
		{"right key from a page", map[string]string{instanceHeader: "k", "Origin": "https://example.com"}, http.StatusForbidden},
		{"right key, same-origin page", map[string]string{instanceHeader: "k", "Sec-Fetch-Site": "same-origin"}, http.StatusForbidden},
		{"right key", map[string]string{instanceHeader: "k"}, http.StatusNoContent},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			opened := 0
			sh := &shower{key: "k", show: func() { opened++ }, now: time.Now}
			if got := ask(sh, tc.headers); got != tc.want {
				t.Errorf("status = %d, want %d", got, tc.want)
			}
			wantOpened := 0
			if tc.want == http.StatusNoContent {
				wantOpened = 1
			}
			if opened != wantOpened {
				t.Errorf("opened %d times, want %d", opened, wantOpened)
			}
		})
	}
}

func TestTheInstanceRouteOpensAtMostOncePerInterval(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)
	opened := 0
	sh := &shower{key: "k", show: func() { opened++ }, now: func() time.Time { return now }}
	key := map[string]string{instanceHeader: "k"}
	for _, step := range []struct {
		advance time.Duration
		want    int
	}{
		{0, 1},
		{time.Second, 1},
		{showInterval, 2},
	} {
		now = now.Add(step.advance)
		if got := ask(sh, key); got != http.StatusNoContent {
			t.Fatalf("status = %d, want %d", got, http.StatusNoContent)
		}
		if opened != step.want {
			t.Fatalf("after +%v: opened %d times, want %d", step.advance, opened, step.want)
		}
	}
}

func TestTheInstanceRouteIsOffWithoutAKey(t *testing.T) {
	t.Parallel()
	srv, _ := newTestServer(t)
	req := httptest.NewRequest(http.MethodPost, instancePath, nil)
	req.Header.Set(instanceHeader, "")
	rec := httptest.NewRecorder()
	srv.handler.ServeHTTP(rec, req)
	if rec.Code == http.StatusNoContent {
		t.Fatalf("status = %d with no instance key configured", rec.Code)
	}
}
