package service

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Mark7888/two-place-paste/desktop/internal/clipboard"
	"github.com/Mark7888/two-place-paste/desktop/internal/config"
	"github.com/Mark7888/two-place-paste/desktop/internal/localui"
)

// lockedBuffer is a log sink the service's goroutines can write to while a
// test reads it.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// captureLogs sends the service's logs to a buffer the test can search.
func captureLogs(f *fixture) *lockedBuffer {
	buf := &lockedBuffer{}
	f.svc.logger = slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	return buf
}

func concealed(s string) clipboard.Content {
	c := textContent(s)
	c.Concealed = clipboard.ReasonConcealed
	return c
}

func sensitiveError(t *testing.T, err error) *localui.SensitiveError {
	t.Helper()
	var se *localui.SensitiveError
	if !errors.As(err, &se) {
		t.Fatalf("error = %v, want a *localui.SensitiveError", err)
	}
	if se.PlanID == "" || se.Reason == "" {
		t.Fatalf("SensitiveError = %+v, want a plan id and a reason", se)
	}
	return se
}

const (
	secretText  = "correct horse battery staple"
	filterValue = "battery"
	filterLabel = "Label-for-my-bank"
)

func filtersPatch() localui.SettingsPatch {
	return localui.SettingsPatch{Filters: &[]localui.FilterView{{
		Kind: "contains", Target: "text", Value: filterValue, Enabled: true, Label: filterLabel,
	}}}
}

// TestSensitiveChangeIsNotAutoUploaded: a password manager's copy and a
// filter match both stay on this device, quietly, and nothing about them
// reaches the log but ids.
func TestSensitiveChangeIsNotAutoUploaded(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	logs := captureLogs(f)
	ctx := context.Background()
	if _, err := f.svc.UpdateSettings(ctx, filtersPatch()); err != nil {
		t.Fatalf("UpdateSettings() error = %v", err)
	}
	events, stop := f.svc.Subscribe()
	defer stop()

	for _, tt := range []struct {
		name   string
		c      clipboard.Content
		reason string
	}{
		{"password manager", concealed("pw-from-a-manager"), clipboard.ReasonConcealed},
		{"filter match", textContent(secretText), filterLabel},
	} {
		f.clip.set(tt.c)
		f.svc.onClipboardChange(ctx, tt.c, f.now())
		if n := f.relay.count(); n != 0 {
			t.Fatalf("%s: a sensitive change was uploaded (%d entries)", tt.name, n)
		}
		if ev := nextEvent(t, events, localui.EventSensitive); ev.Message != tt.reason {
			t.Errorf("%s: event message = %q, want %q", tt.name, ev.Message, tt.reason)
		}
		st := f.svc.Status(ctx)
		if st.SensitiveNote == nil || st.SensitiveNote.Reason != tt.reason {
			t.Errorf("%s: SensitiveNote = %+v, want reason %q", tt.name, st.SensitiveNote, tt.reason)
		}
		if st.Skipped != nil {
			t.Errorf("%s: a sensitive change was reported as skipped: %+v", tt.name, st.Skipped)
		}
		cv := st.Clipboard
		if !cv.Sensitive || cv.Preview != "" || cv.SensitiveReason != tt.reason || cv.Chars != len(tt.c.Body) {
			t.Errorf("%s: clipboard view = %+v, want masked with reason %q and %d chars", tt.name, cv, tt.reason, len(tt.c.Body))
		}
		if a := f.svc.Activity(); a.Skipped != "" {
			t.Errorf("%s: the tray would flag a sensitive change: %+v", tt.name, a)
		}
	}

	// The next ordinary copy clears the note and goes.
	f.svc.onClipboardChange(ctx, textContent("ordinary"), f.now())
	if n := f.relay.count(); n != 1 {
		t.Fatalf("an ordinary change was not uploaded (%d entries)", n)
	}
	if st := f.svc.Status(ctx); st.SensitiveNote != nil {
		t.Errorf("SensitiveNote = %+v after an ordinary upload, want nil", st.SensitiveNote)
	}

	out := logs.String()
	for _, leak := range []string{"pw-from-a-manager", secretText, filterValue, filterLabel} {
		if strings.Contains(out, leak) {
			t.Errorf("the log contains %q:\n%s", leak, out)
		}
	}
	if !strings.Contains(out, "not uploaded automatically: sensitive") {
		t.Errorf("the log does not record the skip:\n%s", out)
	}
}

// A sensitive change over the size limit is reported as sensitive, not as
// too big.
func TestSensitiveWinsOverTheSizeLimit(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	ctx := context.Background()
	if _, err := f.svc.UpdateSettings(ctx, localui.SettingsPatch{AutoUploadMaxBytes: int64Ptr(4)}); err != nil {
		t.Fatal(err)
	}
	f.svc.onClipboardChange(ctx, concealed("longer than four bytes"), f.now())
	st := f.svc.Status(ctx)
	if st.SensitiveNote == nil || st.Skipped != nil {
		t.Errorf("note = %+v, skipped = %+v; want the note only", st.SensitiveNote, st.Skipped)
	}
}

// TestManualUploadOfSensitiveNeedsConfirmation: Upload asks, a confirmed plan
// uploads exactly the content it was asked about, once.
func TestManualUploadOfSensitiveNeedsConfirmation(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	ctx := context.Background()
	up := localui.SyncRequest{Direction: localui.DirectionUpload}

	f.clip.set(concealed("pw1"))
	_, err := f.svc.Sync(ctx, up)
	se := sensitiveError(t, err)
	if se.Reason != clipboard.ReasonConcealed {
		t.Errorf("reason = %q, want %q", se.Reason, clipboard.ReasonConcealed)
	}
	if n := f.relay.count(); n != 0 {
		t.Fatalf("a sensitive clipboard was uploaded without confirmation (%d entries)", n)
	}

	// A plan id that was never issued is just a new question.
	_, err = f.svc.Sync(ctx, localui.SyncRequest{Direction: localui.DirectionUpload, PlanID: "made-up"})
	_ = sensitiveError(t, err)

	// The clipboard changed since the question: the answer is not for this.
	f.clip.set(concealed("pw2"))
	_, err = f.svc.Sync(ctx, localui.SyncRequest{Direction: localui.DirectionUpload, PlanID: se.PlanID})
	se2 := sensitiveError(t, err)
	if f.relay.count() != 0 {
		t.Fatal("a plan for one copy uploaded another")
	}

	// Confirmed for this copy: it goes.
	res, err := f.svc.Sync(ctx, localui.SyncRequest{Direction: localui.DirectionUpload, PlanID: se2.PlanID})
	if err != nil || !res.Changed {
		t.Fatalf("confirmed Sync = %+v, %v; want an upload", res, err)
	}
	if n := f.relay.count(); n != 1 {
		t.Fatalf("relay holds %d entries, want 1", n)
	}

	// An answer applies once.
	_, err = f.svc.Sync(ctx, localui.SyncRequest{Direction: localui.DirectionUpload, PlanID: se2.PlanID})
	_ = sensitiveError(t, err)
}

func TestExpiredPushPlanIsRefused(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	ctx := context.Background()
	f.clip.set(concealed("pw"))
	_, err := f.svc.Sync(ctx, localui.SyncRequest{Direction: localui.DirectionUpload})
	se := sensitiveError(t, err)

	f.svc.now = func() time.Time { return time.Date(2026, 9, 9, 10, 5, 0, 0, time.UTC) }
	_, err = f.svc.Sync(ctx, localui.SyncRequest{Direction: localui.DirectionUpload, PlanID: se.PlanID})
	_ = sensitiveError(t, err)
	if n := f.relay.count(); n != 0 {
		t.Errorf("an expired plan uploaded (%d entries)", n)
	}
}

// Sync with no direction picks upload for a newer local copy, and that upload
// asks just as an explicit one does.
func TestAutoSyncOfSensitiveAsks(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	ctx := context.Background()
	c := concealed("pw")
	f.clip.set(c)
	f.svc.watcher.MarkLocal(c, f.now())
	_, err := f.svc.Sync(ctx, localui.SyncRequest{Direction: localui.DirectionAuto})
	_ = sensitiveError(t, err)
}

func TestDetectionCanBeTurnedOff(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	ctx := context.Background()
	off := false
	view, err := f.svc.UpdateSettings(ctx, localui.SettingsPatch{DetectSensitive: &off})
	if err != nil {
		t.Fatal(err)
	}
	if view.DetectSensitive {
		t.Error("view.DetectSensitive = true after turning it off")
	}
	f.svc.onClipboardChange(ctx, concealed("pw"), f.now())
	if n := f.relay.count(); n != 1 {
		t.Errorf("with detection off, a marked copy was not uploaded (%d entries)", n)
	}
}

func TestBuiltInDetectorsDefaultAndToggle(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	ctx := context.Background()
	key := textContent("-----BEGIN OPENSSH PRIVATE KEY-----\nAAAA")

	view := f.svc.Settings(ctx)
	if !view.DetectSensitive || len(view.Detectors) != 5 {
		t.Fatalf("default view = %+v, want detection on and five detectors", view)
	}
	for _, d := range view.Detectors {
		if d.Enabled != (d.Name == "private_keys") {
			t.Errorf("detector %s enabled = %v by default", d.Name, d.Enabled)
		}
	}
	f.svc.onClipboardChange(ctx, key, f.now())
	if n := f.relay.count(); n != 0 {
		t.Fatalf("a private key was uploaded with the default detectors (%d entries)", n)
	}

	none := []string{}
	if _, err := f.svc.UpdateSettings(ctx, localui.SettingsPatch{Detectors: &none}); err != nil {
		t.Fatal(err)
	}
	f.svc.onClipboardChange(ctx, key, f.now())
	if n := f.relay.count(); n != 1 {
		t.Errorf("with every detector off, the key was not uploaded (%d entries)", n)
	}

	bad := []string{"no_such_detector"}
	if _, err := f.svc.UpdateSettings(ctx, localui.SettingsPatch{Detectors: &bad}); err == nil {
		t.Error("an unknown detector was accepted")
	}
}

// TestExactFilterKeepsOnlyAHash: the secret is hashed on save, never written
// to the settings file, never returned, and kept across later saves that do
// not retype it.
func TestExactFilterKeepsOnlyAHash(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	ctx := context.Background()
	const secret = "my-exact-secret-42"

	view, err := f.svc.UpdateSettings(ctx, localui.SettingsPatch{Filters: &[]localui.FilterView{
		{ID: "exact1", Kind: "exact", Target: "text", Value: secret, Enabled: true},
	}})
	if err != nil {
		t.Fatalf("UpdateSettings() error = %v", err)
	}
	if len(view.Filters) != 1 || view.Filters[0].Value != "" || view.Filters[0].Kind != "exact" {
		t.Fatalf("view filters = %+v, want one exact filter without its value", view.Filters)
	}
	path, err := config.Path(f.svc.configDir)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte(secret)) {
		t.Fatalf("the settings file contains the secret:\n%s", raw)
	}

	f.svc.onClipboardChange(ctx, textContent(" "+secret+"\n"), f.now())
	if n := f.relay.count(); n != 0 {
		t.Fatalf("the exact secret was uploaded (%d entries)", n)
	}

	// Saved again without retyping, as the editor does: the hash stays.
	view.Filters[0].Label = "renamed"
	if _, err := f.svc.UpdateSettings(ctx, localui.SettingsPatch{Filters: &view.Filters}); err != nil {
		t.Fatalf("re-saving the exact filter: %v", err)
	}
	f.svc.onClipboardChange(ctx, textContent("noise"), f.now())
	f.svc.onClipboardChange(ctx, textContent(secret), f.now())
	if n := f.relay.count(); n != 1 {
		t.Errorf("relay holds %d entries, want only the noise", n)
	}

	// An exact filter nobody typed a secret for is refused.
	if _, err := f.svc.UpdateSettings(ctx, localui.SettingsPatch{Filters: &[]localui.FilterView{
		{ID: "new", Kind: "exact", Target: "text", Enabled: true},
	}}); err == nil {
		t.Error("an exact filter without a secret was accepted")
	}
}

func TestInvalidFilterIsRefusedWithoutItsValue(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	ctx := context.Background()
	const pattern = "(unclosed-secret"
	_, err := f.svc.UpdateSettings(ctx, localui.SettingsPatch{Filters: &[]localui.FilterView{
		{Kind: "regex", Target: "text", Value: pattern, Enabled: true},
	}})
	var se *localui.StatusError
	if !errors.As(err, &se) || se.Code != 400 {
		t.Fatalf("error = %v, want a 400", err)
	}
	if strings.Contains(err.Error(), "unclosed-secret") {
		t.Errorf("the error repeats the pattern: %v", err)
	}
	if got := f.svc.Settings(ctx).Filters; len(got) != 0 {
		t.Errorf("a refused list was saved: %+v", got)
	}
}

// TestFilterTestUsesTheDraft: the editor's test box answers for the unsaved
// draft and saves nothing.
func TestFilterTestUsesTheDraft(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	ctx := context.Background()
	jwt := []string{"jwt"}
	res, err := f.svc.TestFilters(ctx, localui.FilterTest{
		Text:      "token eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxIn0.sig and pw:hunter2",
		Filename:  "server.pem",
		Detectors: &jwt,
		Filters: &[]localui.FilterView{
			{ID: "a", Kind: "contains", Target: "text", Value: "PW:", CaseInsensitive: true, Enabled: true},
			{ID: "b", Kind: "suffix", Target: "filename", Value: ".pem", Enabled: true},
			{ID: "c", Kind: "exact", Target: "text", Value: "nope", Enabled: true},
			{ID: "d", Kind: "contains", Target: "text", Value: "token", Enabled: false},
		},
	})
	if err != nil {
		t.Fatalf("TestFilters() error = %v", err)
	}
	var got []string
	for _, m := range res.Matches {
		got = append(got, m.Source+":"+m.Rule)
	}
	if want := "detector:jwt,filter:a,filter:b"; strings.Join(got, ",") != want {
		t.Errorf("matches = %v, want %s", got, want)
	}
	if view := f.svc.Settings(ctx); len(view.Filters) != 0 || view.Detectors[1].Enabled {
		t.Errorf("a test changed the settings: %+v", view)
	}

	if _, err := f.svc.TestFilters(ctx, localui.FilterTest{Text: strings.Repeat("x", maxFilterSampleBytes+1)}); err == nil {
		t.Error("an oversized sample was accepted")
	}
}

func TestRevealClipboard(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	ctx := context.Background()
	f.clip.set(concealed("hunter2"))
	if st := f.svc.Status(ctx); st.Clipboard.Preview != "" {
		t.Fatalf("status preview = %q, want masked", st.Clipboard.Preview)
	}
	got, err := f.svc.RevealClipboard(ctx)
	if err != nil || got.Preview != "hunter2" {
		t.Errorf("RevealClipboard = %+v, %v; want hunter2", got, err)
	}
}
