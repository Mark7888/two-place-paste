package tppclient_test

import (
	"crypto/sha256"
	"strings"
	"testing"

	"github.com/Mark7888/two-place-paste/pkg/tppclient"
)

// TestHeaderV2KeepsLongFilenames: against a relay that accepts version 2
// headers, a writer seals one, and the other device reads the whole filename
// from history without fetching the entry. A version 1 header has room for
// about 115 bytes of it (/spec/crypto.md §5.5, §5.5.1).
func TestHeaderV2KeepsLongFilenames(t *testing.T) {
	relay := startRelay(t)
	ctx := testContext(t)

	desktop, _ := newClient(t, "desktop", tppclient.Options{})
	if err := desktop.CreateGroup(ctx, relay.creationURL(t, "header-v2")); err != nil {
		t.Fatalf("CreateGroup: %v", err)
	}
	phone, _ := newClient(t, "phone", tppclient.Options{})
	pair(ctx, t, desktop, phone)

	limits, err := desktop.Limits(ctx)
	if err != nil {
		t.Fatalf("Limits: %v", err)
	}
	if !limits.HeaderV2 {
		t.Fatalf("Limits.HeaderV2 = false, want true from a current relay")
	}

	name := strings.Repeat("quarterly-report-", 11) + "final.pdf" // 196 bytes
	if _, err := desktop.PutEntry(ctx, tppclient.Item{ContentType: "application/pdf", Filename: name, Body: []byte("%PDF-")}); err != nil {
		t.Fatalf("PutEntry: %v", err)
	}
	history, err := phone.GetHistory(ctx, tppclient.HistoryQuery{})
	if err != nil {
		t.Fatalf("GetHistory: %v", err)
	}
	if len(history.Entries) != 1 {
		t.Fatalf("GetHistory = %d entries, want 1", len(history.Entries))
	}
	want := tppclient.EntryHeader{
		ContentType: "application/pdf", Filename: name, Size: 5,
		SHA256: sha256.Sum256([]byte("%PDF-")), HasSHA256: true,
	}
	if h := history.Entries[0].Header; h == nil || *h != want {
		t.Errorf("Header = %+v, want %+v", h, want)
	}
}
