package httpapi

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestNewRoutesHealthAndRegistrars(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		opts     Options
		regs     []Registrar
		method   string
		path     string
		wantCode int
		wantBody string
	}{
		{
			name:     "default health path",
			method:   http.MethodGet,
			path:     "/healthz",
			wantCode: http.StatusOK,
			wantBody: "ok\n",
		},
		{
			name:     "custom health path",
			opts:     Options{HealthPath: "/live"},
			method:   http.MethodGet,
			path:     "/live",
			wantCode: http.StatusOK,
			wantBody: "ok\n",
		},
		{
			name:     "health rejects non-GET",
			method:   http.MethodPost,
			path:     "/healthz",
			wantCode: http.StatusMethodNotAllowed,
		},
		{
			name: "registrar route is served",
			regs: []Registrar{RegistrarFunc(func(mux *http.ServeMux) {
				mux.HandleFunc("GET /ws", func(w http.ResponseWriter, _ *http.Request) {
					_, _ = io.WriteString(w, "ws")
				})
			})},
			method:   http.MethodGet,
			path:     "/ws",
			wantCode: http.StatusOK,
			wantBody: "ws",
		},
		{
			name: "nil registrar is skipped",
			regs: []Registrar{nil, RegistrarFunc(func(mux *http.ServeMux) {
				mux.HandleFunc("GET /admin", func(w http.ResponseWriter, _ *http.Request) {
					_, _ = io.WriteString(w, "admin")
				})
			})},
			method:   http.MethodGet,
			path:     "/admin",
			wantCode: http.StatusOK,
			wantBody: "admin",
		},
		{
			name:     "unknown path is 404",
			method:   http.MethodGet,
			path:     "/nope",
			wantCode: http.StatusNotFound,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			h := NewWithOptions(tt.opts, tt.regs...)
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest(tt.method, tt.path, nil))

			if rec.Code != tt.wantCode {
				t.Errorf("status = %d, want %d", rec.Code, tt.wantCode)
			}
			if tt.wantBody != "" && rec.Body.String() != tt.wantBody {
				t.Errorf("body = %q, want %q", rec.Body.String(), tt.wantBody)
			}
		})
	}
}

// Two registrars must be able to coexist without knowing about each other.
// This is the property ROADMAP §4 relies on to let P3c and P3d run in parallel.
func TestNewCombinesIndependentRegistrars(t *testing.T) {
	t.Parallel()

	ws := RegistrarFunc(func(mux *http.ServeMux) {
		mux.HandleFunc("GET /ws", func(w http.ResponseWriter, _ *http.Request) {
			_, _ = io.WriteString(w, "ws")
		})
	})
	admin := RegistrarFunc(func(mux *http.ServeMux) {
		mux.HandleFunc("GET /admin/", func(w http.ResponseWriter, _ *http.Request) {
			_, _ = io.WriteString(w, "admin")
		})
	})

	h := New(ws, admin)

	for path, want := range map[string]string{"/ws": "ws", "/admin/": "admin"} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusOK || rec.Body.String() != want {
			t.Errorf("GET %s = %d %q, want 200 %q", path, rec.Code, rec.Body.String(), want)
		}
	}
}

func TestRecovererTurnsPanicInto500(t *testing.T) {
	t.Parallel()

	h := New(RegistrarFunc(func(mux *http.ServeMux) {
		mux.HandleFunc("GET /boom", func(http.ResponseWriter, *http.Request) {
			panic("boom")
		})
	}))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/boom", nil))

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusInternalServerError)
	}
}
