package pairlink_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Mark7888/two-place-paste/server/internal/httpapi"
	"github.com/Mark7888/two-place-paste/server/internal/pairlink"
)

func serve(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(httpapi.New(pairlink.New(nil)))
	t.Cleanup(srv.Close)
	return srv
}

func get(t *testing.T, srv *httptest.Server, path string) (*http.Response, string) {
	t.Helper()
	resp, err := srv.Client().Get(srv.URL + path)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return resp, string(body)
}

// TestPageAndAssets: the page and its files are public, because whoever is scanning a
// code has no account here and may not have the app.
func TestPageAndAssets(t *testing.T) {
	t.Parallel()
	srv := serve(t)

	for _, tc := range []struct{ path, contentType string }{
		{"/pair", "text/html; charset=utf-8"},
		{"/pair/style.css", "text/css; charset=utf-8"},
		{"/pair/app.js", "text/javascript; charset=utf-8"},
		{"/pair/favicon.svg", "image/svg+xml"},
		{"/pair/favicon.ico", "image/x-icon"},
		{"/pair/apple-touch-icon.png", "image/png"},
	} {
		resp, body := get(t, srv, tc.path)
		if resp.StatusCode != http.StatusOK {
			t.Errorf("GET %s = %d, want %d", tc.path, resp.StatusCode, http.StatusOK)
		}
		if got := resp.Header.Get("Content-Type"); got != tc.contentType {
			t.Errorf("GET %s Content-Type = %q, want %q", tc.path, got, tc.contentType)
		}
		if body == "" {
			t.Errorf("GET %s served an empty body", tc.path)
		}
	}
}

// TestPolicy is the page's whole security posture: two same-origin assets, no
// inline script, and no referrer carrying the fragment onward.
func TestPolicy(t *testing.T) {
	t.Parallel()
	resp, body := get(t, serve(t), "/pair")

	csp := resp.Header.Get("Content-Security-Policy")
	for _, want := range []string{"default-src 'none'", "script-src 'self'", "style-src 'self'"} {
		if !strings.Contains(csp, want) {
			t.Errorf("Content-Security-Policy %q does not contain %q", csp, want)
		}
	}
	if strings.Contains(csp, "unsafe-inline") || strings.Contains(csp, "unsafe-eval") {
		t.Errorf("Content-Security-Policy %q relaxes script execution", csp)
	}
	if got := resp.Header.Get("Referrer-Policy"); got != "no-referrer" {
		t.Errorf("Referrer-Policy = %q, want %q", got, "no-referrer")
	}
	// Every line of script is in app.js precisely so the policy does not have
	// to allow an inline one.
	if strings.Contains(body, "<script>") {
		t.Error("the page carries an inline script")
	}
}

// TestPageCarriesNoCode is the property the whole design rests on: the code
// lives in the fragment, which a browser does not send, so this server cannot
// receive it — even when a client puts one in the request line anyway.
func TestPageCarriesNoCode(t *testing.T) {
	t.Parallel()
	srv := serve(t)

	plain, withFragment := "", ""
	_, plain = get(t, srv, "/pair")
	// A fragment cannot be expressed in a request line at all; the closest a
	// caller can get is a query string, and the page must ignore that too.
	_, withFragment = get(t, srv, "/pair?code=SECRETCODE")

	if plain != withFragment {
		t.Error("the page's body depends on the request, so something about the caller reached it")
	}
	if strings.Contains(withFragment, "SECRETCODE") {
		t.Error("the page echoed a value from the request")
	}
}
