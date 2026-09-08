package blob

import (
	"context"
	"fmt"
	"io"
	"sort"
	"strings"
)

// Lister is implemented by a backend that can enumerate what it holds. Only
// the disk backend does; the S3 backend has no reason to, because object
// lifecycle rules reclaim its objects without anyone listing them.
type Lister interface {
	// ListRefs returns every reference the backend currently stores.
	ListRefs(ctx context.Context) ([]string, error)
}

// EntryRefSource yields the blob references Redis currently points at, keyed
// by reference and valued by entry id.
type EntryRefSource interface {
	EntryRefs(ctx context.Context) (map[string]string, error)
}

// VerifyReport is the result of a mark-and-sweep cross-check.
type VerifyReport struct {
	// ScannedBlobs and ScannedEntries are the two sides of the comparison.
	ScannedBlobs   int
	ScannedEntries int

	// Orphans are stored blobs no entry references. Some orphans are normal:
	// an entry whose Redis record expired within the last hour still has its
	// blob on disk until the bucket sweep reaches it (SPEC §4.5).
	Orphans []string

	// Dangling are entries whose blob is missing. These are the ones that
	// matter: the entry is visible and unreadable.
	Dangling []DanglingEntry
}

// DanglingEntry is an entry whose referenced blob is not stored.
type DanglingEntry struct {
	EntryID string
	Ref     string
}

// Verify cross-checks stored blobs against the entries that reference them and
// reports what it finds. It **never deletes anything** (SPEC §4.5, "manual
// verification").
//
// This is a diagnostic for use by hand after Redis data loss or a migration,
// exposed as `tpp gc --verify`. It is not scheduled and is not part of normal
// reclamation, which is the bucket sweep and nothing else.
func Verify(ctx context.Context, l Lister, src EntryRefSource) (VerifyReport, error) {
	refs, err := l.ListRefs(ctx)
	if err != nil {
		return VerifyReport{}, fmt.Errorf("verify: list stored blobs: %w", err)
	}
	entryRefs, err := src.EntryRefs(ctx)
	if err != nil {
		return VerifyReport{}, fmt.Errorf("verify: list entry references: %w", err)
	}

	stored := make(map[string]struct{}, len(refs))
	for _, ref := range refs {
		stored[ref] = struct{}{}
	}

	report := VerifyReport{ScannedBlobs: len(refs), ScannedEntries: len(entryRefs)}
	for ref := range stored {
		if _, ok := entryRefs[ref]; !ok {
			report.Orphans = append(report.Orphans, ref)
		}
	}
	for ref, entryID := range entryRefs {
		if _, ok := stored[ref]; !ok {
			report.Dangling = append(report.Dangling, DanglingEntry{EntryID: entryID, Ref: ref})
		}
	}

	sort.Strings(report.Orphans)
	sort.Slice(report.Dangling, func(i, j int) bool { return report.Dangling[i].Ref < report.Dangling[j].Ref })
	return report, nil
}

// WriteReport renders the report for an operator. It is the output of
// `tpp gc --verify` and says plainly that nothing was deleted, because the
// command exists to be run in the minutes after a Redis incident, when the
// temptation to "clean up" is highest.
func (r VerifyReport) WriteReport(w io.Writer) error {
	var b strings.Builder
	fmt.Fprintf(&b, "scanned %d stored blobs against %d entry references\n", r.ScannedBlobs, r.ScannedEntries)
	fmt.Fprintf(&b, "orphaned blobs (no entry references them): %d\n", len(r.Orphans))
	for _, ref := range r.Orphans {
		fmt.Fprintf(&b, "  orphan  %s\n", ref)
	}
	fmt.Fprintf(&b, "dangling entries (referenced blob is missing): %d\n", len(r.Dangling))
	for _, d := range r.Dangling {
		fmt.Fprintf(&b, "  missing %s (entry %s)\n", d.Ref, d.EntryID)
	}
	b.WriteString("nothing was deleted: this command reports only (SPEC §4.5).\n")

	if _, err := io.WriteString(w, b.String()); err != nil {
		return fmt.Errorf("write verify report: %w", err)
	}
	return nil
}
