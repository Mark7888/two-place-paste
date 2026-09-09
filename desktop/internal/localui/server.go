package localui

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"syscall"
	"time"
)

// Options configures a Server.
type Options struct {
	// API is the service the UI drives. Required.
	API API

	// Port is the TCP port on 127.0.0.1 to bind. Zero lets the operating
	// system choose, which is what tests want and no product should ship.
	Port int

	// Token is the per-launch token. Generated when empty.
	Token string

	// DevProxy, when set, is the base URL of a running Vite dev server; every
	// request that is not the API is forwarded to it. Development only: it is
	// never set in a shipped binary, and the guards still apply in front of it.
	DevProxy string

	// Logger receives request-level logs. It never sees a token or clipboard
	// content (docs/conventions.md §2).
	Logger *slog.Logger
}

// Server is the localhost HTTP server of SPEC §7.2.
type Server struct {
	api      API
	token    string
	port     int
	origins  []string
	patterns []string
	logger   *slog.Logger
	listener net.Listener
	handler  http.Handler
	assets   http.Handler
}

// ErrPortInUse reports that the configured port was already taken. It is a
// distinct condition because it has a distinct remedy — the port override of
// SPEC §7.2 — and the tray says so rather than showing a generic failure.
var ErrPortInUse = errors.New("localui: the port is already in use")

// Listen binds the server. Nothing is served until Serve is called; ctx bounds
// the bind itself, not the serving.
//
// The address is 127.0.0.1 written out, never ":port" and never "0.0.0.0":
// binding every interface would publish the user's clipboard service to the
// network and, on Windows, raise a firewall prompt for something that has no
// business off the machine (SPEC §7.2).
func Listen(ctx context.Context, opts Options) (*Server, error) {
	if opts.API == nil {
		return nil, errors.New("localui: a server needs an API")
	}
	token := opts.Token
	if token == "" {
		var err error
		if token, err = NewToken(); err != nil {
			return nil, err
		}
	}
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}

	addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(opts.Port))
	var lc net.ListenConfig
	ln, err := lc.Listen(ctx, "tcp4", addr)
	if err != nil {
		if errors.Is(err, syscall.EADDRINUSE) {
			return nil, fmt.Errorf("localui: bind %s: %w", addr, ErrPortInUse)
		}
		return nil, fmt.Errorf("localui: bind %s: %w", addr, err)
	}

	port := ln.Addr().(*net.TCPAddr).Port
	s := &Server{
		api:      opts.API,
		token:    token,
		port:     port,
		origins:  allowedOrigins(port),
		patterns: originPatterns(port),
		logger:   logger,
		listener: ln,
	}
	if opts.DevProxy != "" {
		proxy, err := devProxy(opts.DevProxy)
		if err != nil {
			_ = ln.Close()
			return nil, err
		}
		s.assets = proxy
		logger.Warn("serving the UI from a dev server rather than the embedded build", "target", opts.DevProxy)
	} else {
		s.assets = embeddedAssets(logger)
	}
	s.handler = s.routes()
	return s, nil
}

// Port is the port actually bound, which is not the configured one when the
// configured one was zero.
func (s *Server) Port() int { return s.port }

// Token is the per-launch token.
func (s *Server) Token() string { return s.token }

// URL is what the tray opens: the app, with the token as a query parameter
// because a browser navigation cannot carry a header. The UI captures it on
// load, keeps it in memory and clears it from the address bar, so it does not
// end up in history, in a bookmark or in a screenshot.
func (s *Server) URL() string {
	return fmt.Sprintf("http://127.0.0.1:%d/app?token=%s", s.port, url.QueryEscape(s.token))
}

// Handler exposes the routed handler, guards and all. Tests drive it directly.
func (s *Server) Handler() http.Handler { return s.handler }

// Serve runs until ctx is done, then shuts down gracefully.
func (s *Server) Serve(ctx context.Context) error {
	srv := &http.Server{
		Handler:           s.handler,
		ReadHeaderTimeout: 10 * time.Second,
		// No write timeout: /api/events is a long-lived WebSocket.
		BaseContext: func(net.Listener) context.Context { return ctx },
		ErrorLog:    slog.NewLogLogger(s.logger.Handler(), slog.LevelDebug),
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			s.logger.Warn("shutting down the localhost server", "error", err)
		}
	}()

	s.logger.Info("localhost UI listening", "addr", s.listener.Addr().String())
	err := srv.Serve(s.listener)
	<-done
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return fmt.Errorf("localui: serve: %w", err)
}

// Close releases the listener for a server that was never served.
func (s *Server) Close() error {
	if err := s.listener.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
		return fmt.Errorf("localui: close the listener: %w", err)
	}
	return nil
}

func (s *Server) routes() http.Handler {
	mux := http.NewServeMux()

	api := func(pattern string, h http.HandlerFunc) {
		mux.Handle(pattern, s.guard(originRequired, h))
	}
	api("GET /api/status", s.handleStatus)
	api("POST /api/sync", s.handleSync)
	api("GET /api/history", s.handleHistory)
	api("POST /api/entries/copy", s.handleCopyEntry)
	api("GET /api/devices", s.handleDevices)
	api("POST /api/devices/revoke/prepare", s.handlePrepareRevoke)
	api("POST /api/devices/revoke/confirm", s.handleConfirmRevoke)
	api("POST /api/pairing/start", s.handleStartPairing)
	api("POST /api/pairing/join", s.handleJoinPairing)
	api("POST /api/group/create", s.handleCreateGroup)
	api("GET /api/settings", s.handleGetSettings)
	api("POST /api/settings", s.handleUpdateSettings)

	// The event stream is a WebSocket, and its upgrade goes through exactly
	// the same guard as everything else: an upgrade request from a foreign
	// page is the one a naive server forgets to check.
	mux.Handle("GET /api/events", s.guard(originRequired, http.HandlerFunc(s.handleEvents)))

	mux.Handle("GET /app", s.guard(originMatchIfPresent, http.HandlerFunc(s.handleApp)))
	mux.Handle("GET /app/", s.guard(originMatchIfPresent, http.HandlerFunc(s.handleApp)))
	mux.Handle("/", s.guard(originMatchIfPresent, http.HandlerFunc(s.handleRoot)))
	return mux
}

// handleRoot serves the built assets, and sends a bare "/" to the app.
func (s *Server) handleRoot(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/" {
		http.Redirect(w, r, "/app?token="+url.QueryEscape(r.URL.Query().Get("token")), http.StatusSeeOther)
		return
	}
	s.assets.ServeHTTP(w, r)
}

// handleApp serves the single page. Its CSP is as narrow as a page that talks
// only to its own origin can be: no remote script, no remote style, no frame,
// and connections to nowhere but here.
func (s *Server) handleApp(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Security-Policy",
		"default-src 'self'; img-src 'self' data:; style-src 'self' 'unsafe-inline'; "+
			"connect-src 'self' ws://127.0.0.1:"+strconv.Itoa(s.port)+" ws://localhost:"+strconv.Itoa(s.port)+
			"; frame-ancestors 'none'; base-uri 'none'; form-action 'none'")
	r2 := r.Clone(r.Context())
	r2.URL.Path = "/index.html"
	s.assets.ServeHTTP(w, r2)
}
