package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"

	"github.com/Mark7888/two-place-paste/server/internal/blob"
	"github.com/Mark7888/two-place-paste/server/internal/config"
	"github.com/Mark7888/two-place-paste/server/internal/entries"
)

// runGC implements `tpp gc --verify` (SPEC §4.5, "manual verification").
//
// Normal blob reclamation is the bucket sweep inside the running server and
// nothing else: it needs no Redis query and no reference count. This command
// is the manual post-incident tool for the cases the sweep cannot describe —
// after Redis data loss, a restore from backup, or a migration — and it
// **never deletes anything**. Whatever it reports, an operator acts on by
// hand, with the reasoning in front of them.
func runGC(args []string) error {
	fs := flag.NewFlagSet("gc", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	verify := fs.Bool("verify", false, "cross-check stored blobs against entry records and report (required; nothing is ever deleted)")
	fs.Usage = func() {
		fmt.Fprint(os.Stderr, "usage: tpp gc --verify\n\nReports orphaned blobs and dangling entries. Deletes nothing.\n\n")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("gc: parse flags: %w", err)
	}
	if !*verify {
		fs.Usage()
		return errors.New("gc: --verify is required (it is the only mode; gc never deletes)")
	}

	cfg, err := config.Load(config.Options{})
	if err != nil {
		return fmt.Errorf("load configuration: %w", err)
	}
	if cfg.Blob.Backend != config.BlobBackendDisk {
		return fmt.Errorf("gc --verify needs a backend that can list what it holds; %q cannot", cfg.Blob.Backend)
	}

	ctx := context.Background()
	rdb, err := openRedis(ctx, cfg.Redis)
	if err != nil {
		return err
	}
	defer func() { _ = rdb.Close() }()

	report, err := blob.Verify(ctx, blob.NewDisk(cfg.Blob.Root, cfg.Entry.MaxBytes), entries.NewRedisStore(rdb))
	if err != nil {
		return fmt.Errorf("cross-check blobs against entries: %w", err)
	}
	if err := report.WriteReport(os.Stdout); err != nil {
		return fmt.Errorf("write verify report: %w", err)
	}
	return nil
}
