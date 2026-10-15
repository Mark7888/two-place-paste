package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/Mark7888/two-place-paste/server/internal/blob"
	"github.com/Mark7888/two-place-paste/server/internal/config"
	"github.com/Mark7888/two-place-paste/server/internal/entries"
	"github.com/Mark7888/two-place-paste/server/internal/quota"
)

// runGC implements `tpp gc --verify` and `tpp gc --rebuild-accounting`
// (SPEC §4.5, "manual verification").
//
// Normal blob reclamation is the bucket sweep inside the running server and
// nothing else: it needs no Redis query and no reference count. This command
// is the manual post-incident tool for the cases the sweep cannot describe —
// after Redis data loss, a restore from backup, or a migration — and it
// **never deletes a blob**. Whatever it reports, an operator acts on by hand,
// with the reasoning in front of them.
//
// --rebuild-accounting is the one mode that writes: it replaces the storage
// limits' bookkeeping with what the entry records say is stored. The ledger
// heals itself within one entry lifetime regardless; this is for an operator
// who does not want to wait a day after restoring Redis.
func runGC(args []string) error {
	fs := flag.NewFlagSet("gc", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	verify := fs.Bool("verify", false, "cross-check stored blobs and storage accounting against entry records and report")
	rebuild := fs.Bool("rebuild-accounting", false, "recompute storage accounting from the entry records (deletes no blob)")
	fs.Usage = func() {
		fmt.Fprint(os.Stderr, "usage: tpp gc --verify | --rebuild-accounting\n\n"+
			"--verify reports orphaned blobs, dangling entries and accounting drift. Deletes nothing.\n"+
			"--rebuild-accounting recomputes the storage limits' bookkeeping. Deletes no blob.\n\n")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("gc: parse flags: %w", err)
	}
	if *verify == *rebuild {
		fs.Usage()
		return errors.New("gc: choose exactly one of --verify and --rebuild-accounting")
	}

	cfg, err := config.Load(config.Options{})
	if err != nil {
		return fmt.Errorf("load configuration: %w", err)
	}

	ctx := context.Background()
	rdb, err := openRedis(ctx, cfg.Redis)
	if err != nil {
		return err
	}
	defer func() { _ = rdb.Close() }()

	ephemeral := entries.NewRedisStore(rdb)
	ledger := quota.NewRedis(rdb, quota.Options{})

	if *rebuild {
		return rebuildAccounting(ctx, os.Stdout, ephemeral, ledger)
	}

	if cfg.Blob.Backend != config.BlobBackendDisk {
		return fmt.Errorf("gc --verify needs a backend that can list what it holds; %q cannot", cfg.Blob.Backend)
	}
	report, err := blob.Verify(ctx, blob.NewDisk(cfg.Blob.Root, cfg.Entry.MaxBytes), ephemeral)
	if err != nil {
		return fmt.Errorf("cross-check blobs against entries: %w", err)
	}
	if err := report.WriteReport(os.Stdout); err != nil {
		return fmt.Errorf("write verify report: %w", err)
	}
	return reportAccounting(ctx, os.Stdout, ephemeral, ledger)
}

// reportAccounting compares the ledger's totals with what the entry records
// say is stored. A difference is expected for a moment after a crash and
// resolves itself within one entry lifetime; a large or lasting one is what
// --rebuild-accounting is for.
func reportAccounting(ctx context.Context, w io.Writer, src *entries.RedisStore, ledger quota.Ledger) error {
	metas, err := src.BlobEntries(ctx)
	if err != nil {
		return fmt.Errorf("list blob entries: %w", err)
	}
	var want int64
	for _, m := range metas {
		want += m.Size
	}
	got, err := ledger.Usage(ctx)
	if err != nil {
		return fmt.Errorf("read storage accounting: %w", err)
	}

	_, err = fmt.Fprintf(w, "storage accounting: %d bytes in %d entries; entry records hold %d bytes in %d entries\n",
		got.Bytes, got.Entries, want, len(metas))
	if err == nil && (got.Bytes != want || got.Entries != int64(len(metas))) {
		_, err = fmt.Fprint(w, "accounting differs from the entry records; "+
			"uploads in progress account for a small difference. "+
			"Run `tpp gc --rebuild-accounting` to recompute it.\n")
	}
	if err != nil {
		return fmt.Errorf("write accounting report: %w", err)
	}
	return nil
}

func rebuildAccounting(ctx context.Context, w io.Writer, src *entries.RedisStore, ledger quota.Ledger) error {
	before, err := ledger.Usage(ctx)
	if err != nil {
		return fmt.Errorf("read storage accounting: %w", err)
	}
	metas, err := src.BlobEntries(ctx)
	if err != nil {
		return fmt.Errorf("list blob entries: %w", err)
	}
	reservations := make([]quota.Reservation, 0, len(metas))
	for _, m := range metas {
		reservations = append(reservations, quota.Reservation{
			EntryID: m.ID, GroupID: m.GroupID, Size: m.Size, ExpiresAt: m.ExpiresAt,
		})
	}
	if err := ledger.Rebuild(ctx, reservations); err != nil {
		return fmt.Errorf("rebuild storage accounting: %w", err)
	}
	after, err := ledger.Usage(ctx)
	if err != nil {
		return fmt.Errorf("read storage accounting: %w", err)
	}
	if _, err := fmt.Fprintf(w, "storage accounting rebuilt: %d bytes in %d entries (was %d bytes in %d entries)\n"+
		"no blob was deleted.\n", after.Bytes, after.Entries, before.Bytes, before.Entries); err != nil {
		return fmt.Errorf("write rebuild report: %w", err)
	}
	return nil
}
