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
//	tpp gc --rebuild-accounting
//	                   recompute storage accounting from the entries in Redis
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"runtime/debug"
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
	"github.com/Mark7888/two-place-paste/server/internal/quota"
	"github.com/Mark7888/two-place-paste/server/internal/sourcelink"
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
  gc --rebuild-accounting
                     recompute the storage limits' bookkeeping from the
                     entries in Redis; never deletes a blob

Configuration comes from .env and the process environment; see
deploy/.env.example and
https://mark7888.github.io/two-place-paste/self-hosting/configuration/
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
	// (docs/internal/conventions.md §3, §8).
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
	if err := checkBlobBackend(ctx, cfg, blobs); err != nil {
		return err
	}
	persistent := store.NewRedisStore(rdb)
	ephemeral := entries.NewRedisStore(rdb)
	ledger := quota.NewRedis(rdb, quota.Options{Limits: quota.Limits{
		TotalBytes: cfg.Blob.MaxTotalBytes,
		GroupBytes: cfg.Blob.MaxGroupBytes,
	}})

	// The transport is built after the entry service but announces its
	// evictions, so the hook reaches it through this variable. It is set
	// before the server accepts a connection, so no eviction can precede it.
	var wsSrv *ws.Server
	entrySvc := entries.NewService(ephemeral, blobs, entries.Options{
		InlineMaxBytes: cfg.Entry.InlineMaxBytes,
		MaxBytes:       cfg.Entry.MaxBytes,
		TTL:            config.EntryTTL,
		Quota:          ledger,

		SingleFrameMaxBytes: config.SingleFrameMaxBytes,
		ChunkBytes:          cfg.Entry.ChunkBytes,
		UploadTTL:           cfg.Entry.UploadTTL,
		OnEvict: func(ctx context.Context, groupID, entryID string) {
			wsSrv.AnnounceEviction(ctx, groupID, entryID)
		},
		Logger: logger,
	})

	// Chunked uploads live in this process's memory, so any upload still
	// holding room in the ledger now was orphaned by the previous process.
	// Released before the server takes traffic, while nothing can be pending
	// legitimately.
	if n, err := ledger.ReleasePending(ctx); err != nil {
		return fmt.Errorf("release orphaned storage reservations: %w", err)
	} else if n > 0 {
		logger.Info("released storage reserved by uploads a previous run left unfinished", slog.Int("uploads", n))
	}

	// The sweeper is built here so the dashboard can read its status; it is
	// started with the other goroutines below.
	sweeper := blob.NewSweeper(blobs, cfg.Blob.SweepInterval, logger).
		Also("expire storage accounting", func(ctx context.Context, now time.Time) error {
			if _, err := ledger.Expire(ctx, now); err != nil {
				return fmt.Errorf("expire storage accounting: %w", err)
			}
			return nil
		})

	wsSrv = ws.New(persistent, entrySvc, ws.Options{
		Logger:     logger,
		PairingTTL: config.PairingTokenTTL,
	})
	adminSrv, err := admin.New(persistent, admin.Options{
		PublicBaseURL: cfg.PublicBaseURL,
		SessionTTL:    cfg.Admin.SessionTTL,
		Password:      cfg.Admin.Password,
		PasswordHash:  cfg.Admin.PasswordHash,
		Logger:        logger,
		Live:          wsSrv,
		Purger:        entrySvc,
		Limits:        ledger,
		Sweep:         sweeper.Trigger,
		Redis:         rdb,
		Version:       buildVersion(),
		StartedAt:     time.Now().UTC(),
		Dashboard: admin.Dashboard{
			Ledger:         ledger,
			Entries:        ephemeral,
			SweepStatus:    sweeper.Status,
			Uploads:        entrySvc.UploadsInProgress,
			Backend:        string(cfg.Blob.Backend),
			Location:       blobLocation(cfg, blobs),
			TotalLimit:     cfg.Blob.MaxTotalBytes,
			GroupLimit:     cfg.Blob.MaxGroupBytes,
			EntryMaxBytes:  cfg.Entry.MaxBytes,
			InlineMaxBytes: cfg.Entry.InlineMaxBytes,
		},
	})
	if err != nil {
		return fmt.Errorf("build admin server: %w", err)
	}
	// Storage limit overrides live in the store; the ledger checks uploads
	// against its own copy. Bring the copy up to date before any upload.
	if err := adminSrv.SyncStorageLimits(ctx); err != nil {
		return fmt.Errorf("start admin server: %w", err)
	}

	// The pairing hand-off page: three static files behind "GET /pair", which
	// is what a scanned pairing link lands on (SPEC §3.2). It holds no state
	// and never sees a code — the code is in the fragment.
	pairSrv := pairlink.New(logger)

	// The "Source code" link on the admin and pairing pages (AGPL-3.0 §13).
	sourceSrv := sourcelink.New(cfg.SourceURL)

	handler := httpapi.NewWithOptions(httpapi.Options{Logger: logger}, wsSrv, adminSrv, pairSrv, sourceSrv)
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
		// The ledger forgets expired entries on the same schedule, so the
		// storage limits track what the sweep reclaims.
		sweeper.Run(ctx)
	}()
	wg.Add(1)
	go func() {
		defer wg.Done()
		reapUploads(ctx, entrySvc)
	}()

	serveErr := make(chan error, 1)
	go func() {
		logger.Info("server listening",
			slog.String("addr", cfg.HTTPAddr),
			slog.String("public_base_url", cfg.PublicBaseURL),
			slog.String("blob_backend", string(cfg.Blob.Backend)),
			slog.String("blob_store", blobLocation(cfg, blobs)),
			slog.Int64("entry_max_bytes", cfg.Entry.MaxBytes),
			slog.Int64("entry_chunk_bytes", cfg.Entry.ChunkBytes),
			slog.Int64("blob_max_total_bytes", cfg.Blob.MaxTotalBytes),
			slog.Int64("blob_max_group_bytes", cfg.Blob.MaxGroupBytes))
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
	// Uploads cut off by the shutdown release their room now, rather than at
	// the next start.
	entrySvc.AbortAllUploads(shutdownCtx)
	if err != nil {
		return fmt.Errorf("graceful shutdown: %w", err)
	}
	logger.Info("server stopped")
	return <-serveErr
}

// buildVersion describes the running binary for the admin health card: the
// module version and the commit it was built from, when the build recorded
// them.
func buildVersion() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return ""
	}
	version := info.Main.Version
	var rev string
	var dirty bool
	for _, s := range info.Settings {
		switch s.Key {
		case "vcs.revision":
			rev = s.Value
		case "vcs.modified":
			dirty = s.Value == "true"
		}
	}
	if len(rev) > 12 {
		rev = rev[:12]
	}
	if dirty && rev != "" {
		rev += "+dirty"
	}
	switch {
	case version != "" && version != "(devel)" && rev != "":
		return version + " (" + rev + ")"
	case rev != "":
		return rev
	case version == "(devel)":
		// A build from a checkout without VCS data, such as the Docker
		// build, whose context has no .git.
		return "development build"
	default:
		return version
	}
}

// uploadReapInterval is how often idle chunked uploads are looked for. An
// upload also ends the moment its next chunk finds it timed out; this is for
// the ones whose client never comes back.
const uploadReapInterval = time.Minute

// reapUploads abandons idle chunked uploads until ctx is done.
func reapUploads(ctx context.Context, svc *entries.Service) {
	ticker := time.NewTicker(uploadReapInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			svc.ReapUploads(ctx, now.UTC())
		}
	}
}

// newLogger builds the process-wide logger. Libraries never configure logging
// (docs/internal/conventions.md §2); this is the one place that does.
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
