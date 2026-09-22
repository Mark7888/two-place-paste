// Command tpp is the TwoPlacePaste relay server (SPEC §4).
//
// It is the only place where the components meet: config, the Redis persistent
// and ephemeral zones, the blob backend and its sweeper, the WebSocket
// transport and the admin surface. Every one of those is written to be
// constructed here and nowhere else, which is why this file is the phase that
// makes the server runnable.
//
// Usage:
//
//	tpp [serve]        run the relay and admin server (default)
//	tpp gc --verify    cross-check blobs against entries; report only
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/Mark7888/two-place-paste/server/internal/admin"
	"github.com/Mark7888/two-place-paste/server/internal/blob"
	"github.com/Mark7888/two-place-paste/server/internal/config"
	"github.com/Mark7888/two-place-paste/server/internal/entries"
	"github.com/Mark7888/two-place-paste/server/internal/httpapi"
	"github.com/Mark7888/two-place-paste/server/internal/pairlink"
	"github.com/Mark7888/two-place-paste/server/internal/store"
	"github.com/Mark7888/two-place-paste/server/internal/ws"
)

// shutdownTimeout bounds how long in-flight work has once a signal arrives.
// WebSocket connections are closed rather than drained: a client reconnects
// with backoff, and nothing on a socket is state the server must finish.
const shutdownTimeout = 15 * time.Second

// readHeaderTimeout bounds how long a client may take to send its headers. No
// read or write deadline is set on the connection itself: a WebSocket is
// long-lived by design and a deadline would sever an idle one.
const readHeaderTimeout = 10 * time.Second

func main() {
	if err := run(os.Args[1:]); err != nil {
		// The logger may not exist yet — a config failure happens before it.
		fmt.Fprintf(os.Stderr, "tpp: %v\n", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	cmd := "serve"
	if len(args) > 0 && !isFlag(args[0]) {
		cmd, args = args[0], args[1:]
	}

	switch cmd {
	case "serve":
		return serve(args)
	case "gc":
		return runGC(args)
	case "help", "-h", "--help":
		usage(os.Stdout)
		return nil
	default:
		usage(os.Stderr)
		return fmt.Errorf("unknown command %q", cmd)
	}
}

func isFlag(arg string) bool { return len(arg) > 0 && arg[0] == '-' }

func usage(w *os.File) {
	fmt.Fprint(w, `tpp — TwoPlacePaste relay server

Commands:
  serve              run the relay and admin server (default)
  gc --verify        cross-check stored blobs against entries and report;
                     never deletes anything

Configuration comes from .env and the process environment; see
deploy/.env.example and docs/deployment.md.
`)
}

// serve wires every component together and runs until a signal arrives.
func serve(args []string) error {
	if len(args) > 0 {
		return fmt.Errorf("serve takes no arguments, got %q", args[0])
	}

	cfg, err := config.Load(config.Options{})
	if err != nil {
		return fmt.Errorf("load configuration: %w", err)
	}
	logger := newLogger(cfg.LogLevel)
	slog.SetDefault(logger)

	// The signal context is the parent of everything: cancelling it stops the
	// sweeper and unblocks every handler holding a request context
	// (docs/conventions.md §3, §8).
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	rdb, err := openRedis(ctx, cfg.Redis)
	if err != nil {
		return err
	}
	defer func() { _ = rdb.Close() }()

	blobs, err := newBlobBackend(cfg)
	if err != nil {
		return err
	}
	persistent := store.NewRedisStore(rdb)
	ephemeral := entries.NewRedisStore(rdb)
	entrySvc := entries.NewService(ephemeral, blobs, entries.Options{
		InlineMaxBytes: cfg.Entry.InlineMaxBytes,
		MaxBytes:       cfg.Entry.MaxBytes,
		TTL:            config.EntryTTL,
	})

	wsSrv := ws.New(persistent, entrySvc, ws.Options{
		Logger:     logger,
		PairingTTL: config.PairingTokenTTL,
	})
	adminSrv, err := admin.New(persistent, admin.Options{
		PublicBaseURL: cfg.PublicBaseURL,
		SessionTTL:    cfg.Admin.SessionTTL,
		Password:      cfg.Admin.Password,
		PasswordHash:  cfg.Admin.PasswordHash,
		Logger:        logger,
	})
	if err != nil {
		return fmt.Errorf("build admin server: %w", err)
	}

	// The pairing hand-off page: three static files behind "GET /pair", which
	// is what a scanned pairing link lands on (SPEC §3.2). It holds no state
	// and never sees a code — the code is in the fragment.
	pairSrv := pairlink.New(logger)

	handler := httpapi.NewWithOptions(httpapi.Options{Logger: logger}, wsSrv, adminSrv, pairSrv)
	srv := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           handler,
		ReadHeaderTimeout: readHeaderTimeout,
		ErrorLog:          slog.NewLogLogger(logger.Handler(), slog.LevelWarn),
	}

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		// The sweeper runs once immediately, which is what makes downtime
		// longer than a bucket harmless (SPEC §4.5).
		blob.NewSweeper(blobs, cfg.Blob.SweepInterval, logger).Run(ctx)
	}()

	serveErr := make(chan error, 1)
	go func() {
		logger.Info("server listening",
			slog.String("addr", cfg.HTTPAddr),
			slog.String("public_base_url", cfg.PublicBaseURL),
			slog.String("blob_backend", string(cfg.Blob.Backend)))
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serveErr <- fmt.Errorf("http server: %w", err)
			return
		}
		serveErr <- nil
	}()

	select {
	case err := <-serveErr:
		stop()
		wg.Wait()
		return err
	case <-ctx.Done():
		logger.Info("shutdown signalled", slog.Duration("grace", shutdownTimeout))
	}

	// Shutdown gets its own context: the signal context is already cancelled.
	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	err = srv.Shutdown(shutdownCtx)
	wg.Wait()
	if err != nil {
		return fmt.Errorf("graceful shutdown: %w", err)
	}
	logger.Info("server stopped")
	return <-serveErr
}

// newLogger builds the process-wide logger. Libraries never configure logging
// (docs/conventions.md §2); this is the one place that does.
func newLogger(level slog.Level) *slog.Logger {
	return slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: level}))
}

// openRedis connects and verifies the connection before anything depends on
// it, so a misconfigured address fails at startup rather than on first use.
func openRedis(ctx context.Context, cfg config.RedisConfig) (*redis.Client, error) {
	rdb := redis.NewClient(&redis.Options{
		Addr:     cfg.Addr,
		Password: cfg.Password,
		DB:       cfg.DB,
	})
	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := rdb.Ping(pingCtx).Err(); err != nil {
		_ = rdb.Close()
		return nil, fmt.Errorf("connect to redis at %s: %w", cfg.Addr, err)
	}
	return rdb, nil
}

// newBlobBackend builds the configured blob backend (SPEC §4.3).
func newBlobBackend(cfg *config.Config) (blob.Backend, error) {
	switch cfg.Blob.Backend {
	case config.BlobBackendDisk:
		return blob.NewDisk(cfg.Blob.Root, cfg.Entry.MaxBytes), nil
	case config.BlobBackendS3:
		// Deferred (SPEC §4.3): the type exists so the interface has a second
		// implementation, but nothing behind it is written yet.
		return nil, errors.New("the s3 blob backend is not implemented; set TPP_BLOB_BACKEND=disk")
	default:
		return nil, fmt.Errorf("unknown blob backend %q", cfg.Blob.Backend)
	}
}
