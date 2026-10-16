package tray

import (
	"bytes"
	"sync"
	"testing"
	"time"
)

// paints records every status a Tray paints.
type paints struct {
	mu  sync.Mutex
	got []Status
}

func (p *paints) paint(s Status) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.got = append(p.got, s)
}

func (p *paints) last() (Status, int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.got) == 0 {
		return -1, 0
	}
	return p.got[len(p.got)-1], len(p.got)
}

func TestStatusSetBeforeTheIconExistsIsPaintedWhenItDoes(t *testing.T) {
	t.Parallel()

	tr := New(nil)
	tr.SetStatus(StatusUnpaired)
	var p paints
	tr.attach(p.paint)
	if got, _ := p.last(); got != StatusUnpaired {
		t.Errorf("painted %v on attach, want %v", got, StatusUnpaired)
	}
}

func TestSyncingIsHeldLongEnoughToSee(t *testing.T) {
	t.Parallel()

	tr := New(nil)
	tr.minSync = 50 * time.Millisecond
	var p paints
	tr.attach(p.paint)

	tr.SetStatus(StatusSyncing)
	tr.SetStatus(StatusConnected)
	if got, _ := p.last(); got != StatusSyncing {
		t.Fatalf("painted %v straight after a short sync, want %v held", got, StatusSyncing)
	}

	deadline := time.Now().Add(2 * time.Second)
	for {
		if got, _ := p.last(); got == StatusConnected {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the syncing glyph was never replaced")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestSyncingAgainCancelsTheHold(t *testing.T) {
	t.Parallel()

	tr := New(nil)
	tr.minSync = 30 * time.Millisecond
	var p paints
	tr.attach(p.paint)

	tr.SetStatus(StatusSyncing)
	tr.SetStatus(StatusConnected)
	tr.SetStatus(StatusSyncing)
	time.Sleep(100 * time.Millisecond)
	if got, _ := p.last(); got != StatusSyncing {
		t.Errorf("painted %v while a second sync is running, want %v", got, StatusSyncing)
	}
}

func TestALongSyncIsReplacedAtOnce(t *testing.T) {
	t.Parallel()

	clock := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	tr := New(nil)
	tr.now = func() time.Time { return clock }
	var p paints
	tr.attach(p.paint)

	tr.SetStatus(StatusSyncing)
	clock = clock.Add(time.Second)
	tr.SetStatus(StatusDisconnected)
	if got, _ := p.last(); got != StatusDisconnected {
		t.Errorf("painted %v after a sync longer than the hold, want %v", got, StatusDisconnected)
	}
}

func TestAnUnchangedStatusIsNotRepainted(t *testing.T) {
	t.Parallel()

	tr := New(nil)
	var p paints
	tr.attach(p.paint)
	tr.SetStatus(StatusConnected)
	_, before := p.last()
	tr.SetStatus(StatusConnected)
	if _, after := p.last(); after != before {
		t.Errorf("SetStatus with the status on screen painted again (%d paints, want %d)", after, before)
	}
}

func TestDirectionActions(t *testing.T) {
	t.Parallel()

	tests := []struct {
		action string
		want   Direction
		ok     bool
	}{
		{action: actionUpload, want: Upload, ok: true},
		{action: actionDownload, want: Download, ok: true},
		{action: "", ok: false},
		{action: "com.apple.UNNotificationDefaultActionIdentifier", ok: false},
	}
	for _, tt := range tests {
		t.Run(tt.action, func(t *testing.T) {
			t.Parallel()
			got, ok := directionForAction(tt.action)
			if got != tt.want || ok != tt.ok {
				t.Errorf("directionForAction(%q) = %v, %v; want %v, %v", tt.action, got, ok, tt.want, tt.ok)
			}
		})
	}
}

func TestChoosingADirectionRunsItAndWithdrawsTheItems(t *testing.T) {
	t.Parallel()

	tr := New(nil)
	chosen := make(chan Direction, 1)
	tr.onDirection = func(d Direction) { chosen <- d }
	var shown []bool
	tr.setChoiceItems(func(show bool) { shown = append(shown, show) })

	tr.AskDirection()
	tr.chooseAction(actionDownload)

	select {
	case d := <-chosen:
		if d != Download {
			t.Errorf("SyncDirection got %v, want %v", d, Download)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("SyncDirection was not called")
	}
	if len(shown) != 2 || !shown[0] || shown[1] {
		t.Errorf("direction items shown = %v, want [true false]", shown)
	}
}

func TestEveryStatusHasItsOwnGlyph(t *testing.T) {
	t.Parallel()

	// The pairs that share a glyph on purpose: what tells them apart is the
	// tooltip and the menu's status line.
	same := map[Status]Status{
		StatusFailed:          StatusDisconnected,
		StatusSkippedDownload: StatusSkippedUpload,
	}
	seen := map[string]Status{}
	for s := StatusConnecting; s <= StatusSkippedDownload; s++ {
		set := icons(inkDark, s)
		if len(set.png) == 0 || len(set.ico) == 0 {
			t.Fatalf("icons(%v) is empty", s)
		}
		if !bytes.HasPrefix(set.png, []byte("\x89PNG")) {
			t.Errorf("icons(%v).png is not a PNG", s)
		}
		if s.label() == "" {
			t.Errorf("status %v has no label", s)
		}
		if twin, ok := same[s]; ok {
			if !bytes.Equal(set.png, icons(inkDark, twin).png) {
				t.Errorf("status %v does not share %v's glyph", s, twin)
			}
			continue
		}
		if other, dup := seen[string(set.png)]; dup {
			t.Errorf("status %v draws the same glyph as %v", s, other)
		}
		seen[string(set.png)] = s
	}
}

func TestOneTransferGlyphReplacesAnotherAtOnce(t *testing.T) {
	t.Parallel()

	tr := New(nil)
	tr.minSync = time.Hour
	var p paints
	tr.attach(p.paint)

	tr.SetStatus(StatusUploading)
	tr.SetStatus(StatusSyncing)
	if got, _ := p.last(); got != StatusSyncing {
		t.Errorf("painted %v when a download joined an upload, want %v", got, StatusSyncing)
	}
}
