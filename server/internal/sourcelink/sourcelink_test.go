package sourcelink_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Mark7888/two-place-paste/server/internal/httpapi"
	"github.com/Mark7888/two-place-paste/server/internal/sourcelink"
)

func TestRedirectsToSourceURL(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		url  string
	}{
		{"upstream", "https://github.com/Mark7888/two-place-paste"},
		{"fork with a path", "https://git.example.com/someone/tpp-fork/src/branch/main"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			srv := httptest.NewServer(httpapi.New(sourcelink.New(tt.url)))
			t.Cleanup(srv.Close)
			client := srv.Client()
			client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }

			resp, err := client.Get(srv.URL + "/source")
			if err != nil {
				t.Fatalf("GET /source: %v", err)
			}
			t.Cleanup(func() { _ = resp.Body.Close() })

			if resp.StatusCode != http.StatusFound {
				t.Errorf("status = %d, want %d", resp.StatusCode, http.StatusFound)
			}
			if got := resp.Header.Get("Location"); got != tt.url {
				t.Errorf("Location = %q, want %q", got, tt.url)
			}
			if got := resp.Header.Get("Cache-Control"); got != "no-store" {
				t.Errorf("Cache-Control = %q, want %q", got, "no-store")
			}
		})
	}
}

func TestOnlyGet(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(httpapi.New(sourcelink.New("https://example.com/src")))
	t.Cleanup(srv.Close)

	resp, err := srv.Client().Post(srv.URL+"/source", "text/plain", nil)
	if err != nil {
		t.Fatalf("POST /source: %v", err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("POST /source status = %d, want %d", resp.StatusCode, http.StatusMethodNotAllowed)
	}
}
