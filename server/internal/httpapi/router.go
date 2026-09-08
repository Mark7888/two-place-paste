// Package httpapi assembles the server's HTTP routing.
//
// SHARED TOUCHPOINT (ROADMAP §4). This file exists so that Phase 3c
// (WebSocket transport) and Phase 3d (admin UI) can both attach handlers
// without ever editing the same file:
//
//   - P3c adds internal/ws/register.go     implementing Registrar
//   - P3d adds internal/admin/register.go  implementing Registrar
//   - P3e wires them together in cmd/tpp/main.go
//
// Neither phase edits this file or main.go. If a phase believes it needs a
// change here, it stops and opens a `contract-change` issue (ROADMAP §3).
package httpapi

import (
	"log/slog"
	"net/http"
)

// Registrar is implemented by any component that contributes routes to the
// server. Implementations must only call mux.Handle / mux.HandleFunc; they
// must not depend on registration order or on routes owned by another
// Registrar.
type Registrar interface {
	// Register attaches the component's routes to mux.
	Register(mux *http.ServeMux)
}

// RegistrarFunc adapts an ordinary function to the Registrar interface.
type RegistrarFunc func(mux *http.ServeMux)

// Register implements Registrar.
func (f RegistrarFunc) Register(mux *http.ServeMux) { f(mux) }

// Options tunes the assembled handler. The zero value is usable.
type Options struct {
	// Logger receives request-scoped logs. Defaults to slog.Default().
	Logger *slog.Logger

	// HealthPath is the liveness endpoint path. Defaults to "/healthz".
	// Phase 3e's deployment healthcheck targets it.
	HealthPath string
}

// New assembles an http.Handler from the given registrars.
//
// Registrars are applied in argument order. A nil registrar is ignored so that
// callers can conditionally omit a component without branching. Duplicate
// patterns panic — that is net/http's behaviour and it is deliberately not
// suppressed: a collision between two Registrars is a wiring bug that must
// surface at startup, not at request time.
func New(regs ...Registrar) http.Handler {
	return NewWithOptions(Options{}, regs...)
}

// NewWithOptions is New with explicit configuration.
func NewWithOptions(opts Options, regs ...Registrar) http.Handler {
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	healthPath := opts.HealthPath
	if healthPath == "" {
		healthPath = "/healthz"
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET "+healthPath, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok\n"))
	})

	for _, reg := range regs {
		if reg == nil {
			continue
		}
		reg.Register(mux)
	}

	return recoverer(logger, mux)
}

// recoverer converts a panic in a handler into a 500 rather than tearing down
// the process. Handlers themselves must not panic (docs/conventions.md); this
// is a backstop, not a licence.
func recoverer(logger *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if v := recover(); v != nil {
				logger.ErrorContext(r.Context(), "panic in http handler",
					slog.String("method", r.Method),
					slog.String("path", r.URL.Path),
					slog.Any("panic", v),
				)
				http.Error(w, "internal server error", http.StatusInternalServerError)
			}
		}()
		next.ServeHTTP(w, r)
	})
}
