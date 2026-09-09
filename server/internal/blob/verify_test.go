package blob_test

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Mark7888/two-place-paste/server/internal/blob"
)

type refSource map[string]string

func (r refSource) EntryRefs(context.Context) (map[string]string, error) { return r, nil }

func TestVerifyReportsOrphansAndDanglingEntries(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	root := t.TempDir()
	d := blob.NewDisk(root, 1024)

	expires := time.Date(2026, 9, 8, 14, 0, 0, 0, time.UTC)
	referenced, err := d.Put(ctx, "entry-referenced", expires, stringReader("a"))
	if err != nil {
		t.Fatalf("Put() error = %v", err)
	}
	orphan, err := d.Put(ctx, "entry-orphan", expires, stringReader("b"))
	if err != nil {
		t.Fatalf("Put() error = %v", err)
	}

	// A file that is not a blob at all must not be reported as an orphan.
	if err := os.WriteFile(filepath.Join(root, "2026090814", "README"), []byte("x"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	report, err := blob.Verify(ctx, d, refSource{
		referenced:                  "entry-referenced",
		"2026090814/entry-gone.bin": "entry-gone",
	})
	if err != nil {
		t.Fatalf("Verify() error = %v", err)
	}

	if report.ScannedBlobs != 2 || report.ScannedEntries != 2 {
		t.Errorf("Verify() scanned %d blobs and %d entries, want 2 and 2", report.ScannedBlobs, report.ScannedEntries)
	}
	if len(report.Orphans) != 1 || report.Orphans[0] != orphan {
		t.Errorf("Verify().Orphans = %v, want [%s]", report.Orphans, orphan)
	}
	if len(report.Dangling) != 1 || report.Dangling[0].EntryID != "entry-gone" {
		t.Errorf("Verify().Dangling = %+v, want one entry-gone", report.Dangling)
	}

	// Report only, never delete (SPEC §4.5).
	if _, err := d.Get(ctx, orphan); err != nil {
		t.Errorf("Get(orphan) after Verify() error = %v, want the orphan to still be there", err)
	}
}

func stringReader(s string) io.Reader { return strings.NewReader(s) }

func TestVerifyReportWriteReport(t *testing.T) {
	t.Parallel()

	report := blob.VerifyReport{
		ScannedBlobs:   3,
		ScannedEntries: 2,
		Orphans:        []string{"2026090814/orphan.bin"},
		Dangling:       []blob.DanglingEntry{{EntryID: "e1", Ref: "2026090814/e1.bin"}},
	}

	var out strings.Builder
	if err := report.WriteReport(&out); err != nil {
		t.Fatalf("WriteReport() error = %v", err)
	}

	for _, want := range []string{
		"scanned 3 stored blobs against 2 entry references",
		"2026090814/orphan.bin",
		"missing 2026090814/e1.bin (entry e1)",
		"nothing was deleted",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("WriteReport() output is missing %q; got:\n%s", want, out.String())
		}
	}
}
