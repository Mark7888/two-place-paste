package update

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// drip serves body in chunks, gap apart: a slow line that keeps delivering.
// When hangAfter is non-negative it stops after that many bytes and holds the
// connection open until the client gives up: a line that died.
func drip(body []byte, chunk int, gap time.Duration, hangAfter int) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		flusher, _ := w.(http.Flusher)
		for off := 0; off < len(body); off += chunk {
			if hangAfter >= 0 && off >= hangAfter {
				<-r.Context().Done()
				return
			}
			end := min(off+chunk, len(body))
			if _, err := w.Write(body[off:end]); err != nil {
				return
			}
			if flusher != nil {
				flusher.Flush()
			}
			select {
			case <-r.Context().Done():
				return
			case <-time.After(gap):
			}
		}
	}
}

// progressLog records progress reports.
type progressLog struct {
	mu    sync.Mutex
	calls int
	done  int64
	total int64
}

func (p *progressLog) report(done, total int64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls++
	p.done, p.total = done, total
}

func fileFor(body []byte) File {
	sum := sha256.Sum256(body)
	return File{Name: "build.bin", Size: int64(len(body)), SHA256: hex.EncodeToString(sum[:])}
}

// TestASlowDownloadIsNotCutOff is the bug this guards against: a download
// that keeps arriving must finish however long it takes in all, even through
// a client whose per-request timeout is shorter than the whole download.
func TestASlowDownloadIsNotCutOff(t *testing.T) {
	t.Parallel()
	body := bytes.Repeat([]byte("tpp"), 4000)
	srv := httptest.NewServer(drip(body, 1000, 150*time.Millisecond, -1)) // ~1.8 s in all
	t.Cleanup(srv.Close)

	m := &Manager{opts: Options{Client: &http.Client{Timeout: 300 * time.Millisecond}}}
	var log progressLog
	path, err := download(context.Background(), m.downloadClient(), srv.URL, t.TempDir(), fileFor(body), log.report)
	if err != nil {
		t.Fatalf("download() error = %v", err)
	}
	if path == "" {
		t.Fatal("download() returned no file")
	}
	if log.done != int64(len(body)) || log.total != int64(len(body)) || log.calls < 2 {
		t.Errorf("progress = %d of %d in %d reports, want %d of %d in several", log.done, log.total, log.calls, len(body), len(body))
	}
}

func TestAStalledDownloadIsGivenUp(t *testing.T) {
	t.Parallel()
	body := bytes.Repeat([]byte("tpp"), 4000)
	srv := httptest.NewServer(drip(body, 1000, 10*time.Millisecond, 3000))
	t.Cleanup(srv.Close)

	m := &Manager{opts: Options{Client: &http.Client{}}}
	start := time.Now()
	_, err := download(context.Background(), m.downloadClient(), srv.URL, t.TempDir(), fileFor(body), nil)
	if !errors.Is(err, ErrStalled) {
		t.Fatalf("download() error = %v, want %v", err, ErrStalled)
	}
	if took := time.Since(start); took > 10*stallTimeout {
		t.Errorf("giving up took %s, want about %s", took, stallTimeout)
	}
}

// TestASlowArtifactIsNotCutOff is the same for a Nightly build: the API
// client's 30-second timeout used to bound the whole artifact download.
func TestASlowArtifactIsNotCutOff(t *testing.T) {
	t.Parallel()
	body := bytes.Repeat([]byte("zip"), 4000)
	storage := httptest.NewServer(drip(body, 1000, 150*time.Millisecond, -1))
	t.Cleanup(storage.Close)
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, storage.URL+"/blob", http.StatusFound)
	}))
	t.Cleanup(api.Close)

	g := githubAPI{client: &http.Client{Timeout: 300 * time.Millisecond}, base: api.URL, token: "t"}
	var buf bytes.Buffer
	var log progressLog
	if err := g.downloadArtifact(context.Background(), api.URL+"/zip", &buf, int64(len(body)), log.report); err != nil {
		t.Fatalf("downloadArtifact() error = %v", err)
	}
	if !bytes.Equal(buf.Bytes(), body) {
		t.Errorf("downloaded %d bytes, want the %d served", buf.Len(), len(body))
	}
	if log.done != int64(len(body)) || log.total != int64(len(body)) {
		t.Errorf("progress = %d of %d, want %d of %d", log.done, log.total, len(body), len(body))
	}
}

func TestAStalledArtifactIsGivenUp(t *testing.T) {
	t.Parallel()
	body := bytes.Repeat([]byte("zip"), 4000)
	storage := httptest.NewServer(drip(body, 1000, 10*time.Millisecond, 2000))
	t.Cleanup(storage.Close)
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, storage.URL+"/blob", http.StatusFound)
	}))
	t.Cleanup(api.Close)

	g := githubAPI{client: &http.Client{}, base: api.URL, token: "t"}
	err := g.downloadArtifact(context.Background(), api.URL+"/zip", &bytes.Buffer{}, 0, nil)
	if !errors.Is(err, ErrStalled) {
		t.Fatalf("downloadArtifact() error = %v, want %v", err, ErrStalled)
	}
}

func TestProgressIsReportedToTheUI(t *testing.T) {
	t.Parallel()
	m := &Manager{}
	m.state = "downloading"
	if m.progressLocked() != nil {
		t.Error("progress before any byte arrived, want none")
	}
	m.reportProgress(10, 100)
	m.reportProgress(20, 100) // within progressInterval: recorded, not announced
	p := m.progressLocked()
	if p == nil || p.Done != 20 || p.Total != 100 {
		t.Errorf("progress = %+v, want 20 of 100", p)
	}
	m.state = "installing"
	if m.progressLocked() != nil {
		t.Error("progress while installing, want none")
	}
}
