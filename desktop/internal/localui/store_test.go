package localui

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"slices"
	"testing"
)

// The stub's store: it records what it was asked and answers with fixed data.

func (s *stubAPI) Store(_ context.Context, q StoreQuery) (StoreView, error) {
	s.record(fmt.Sprintf("store q=%q tags=%v sort=%q", q.Text, q.Tags, q.Sort))
	return StoreView{Enabled: true, Items: []StoreItemView{}, Tags: []StoreTagView{}, Unreadable: []StoreUnreadableView{}}, nil
}

func (s *stubAPI) SaveToStore(_ context.Context, req StoreSaveRequest) (StoreItemView, error) {
	s.record("store-save " + req.Source)
	if !req.AllowDuplicate && req.Name == "dup" {
		return StoreItemView{}, &DuplicateError{Duplicates: []StoreItemView{{ID: "old", Name: "Already here"}}}
	}
	return StoreItemView{ID: "new", Name: req.Name}, nil
}

func (s *stubAPI) UploadToStore(_ context.Context, up StoreUpload) (StoreItemView, error) {
	s.record(fmt.Sprintf("store-upload %s %s %d %v %s", up.Filename, up.ContentType, len(up.Body), up.AllowDuplicate, up.Details.Name))
	return StoreItemView{ID: "file", Name: up.Details.Name}, nil
}

func (s *stubAPI) EditStoreItem(_ context.Context, itemID string, d StoreDetails) (StoreItemView, error) {
	s.record("store-edit " + itemID)
	return StoreItemView{ID: itemID, Name: d.Name}, nil
}

func (s *stubAPI) DeleteStoreItem(_ context.Context, itemID string) error {
	s.record("store-delete " + itemID)
	return nil
}

func (s *stubAPI) CopyStoreItem(_ context.Context, itemID string) (SyncResult, error) {
	s.record("store-copy " + itemID)
	return SyncResult{Changed: true}, nil
}

func (s *stubAPI) StoreItemContent(_ context.Context, itemID string) (EntryContent, error) {
	s.record("store-content " + itemID)
	return EntryContent{ContentType: "text/html", Body: []byte("<script>alert(1)</script>")}, nil
}

func appHeaders(srv *Server) map[string]string {
	return map[string]string{
		"Origin":    fmt.Sprintf("http://127.0.0.1:%d", srv.Port()),
		tokenHeader: "test-token",
	}
}

// TestStoreRoutes: the listing passes its query through, a duplicate comes
// back as 409 with the items it duplicates, an item's bytes are an
// attachment no browser renders, and a build without a store says so.
func TestStoreRoutes(t *testing.T) {
	t.Parallel()
	srv, api := newTestServer(t)
	h := appHeaders(srv)

	if rec := do(t, srv, "GET", "/api/store?q=wifi&tag=home&tag=net&sort=name", "", h); rec.Code != http.StatusOK {
		t.Fatalf("GET /api/store = %d", rec.Code)
	}
	if !api.called(`store q="wifi" tags=[home net] sort="name"`) {
		t.Errorf("the query did not reach the service: %v", api.calls)
	}
	if rec := do(t, srv, "GET", "/api/store?sort=size", "", h); rec.Code != http.StatusBadRequest {
		t.Errorf("GET /api/store?sort=size = %d, want 400", rec.Code)
	}

	rec := do(t, srv, "POST", "/api/store/save", `{"source":"clipboard","name":"dup","description":"","tags":[]}`, h)
	var dup struct {
		Error      string          `json:"error"`
		Duplicates []StoreItemView `json:"duplicates"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &dup); err != nil || rec.Code != http.StatusConflict ||
		len(dup.Duplicates) != 1 || dup.Duplicates[0].Name != "Already here" {
		t.Errorf("a duplicate save = %d %s, want 409 naming the duplicate", rec.Code, rec.Body)
	}
	for _, body := range []string{`{"source":"entry"}`, `{"source":"clipboard","entry_id":"e1"}`, `{"source":"disk"}`} {
		if rec := do(t, srv, "POST", "/api/store/save", body, h); rec.Code != http.StatusBadRequest {
			t.Errorf("POST /api/store/save %s = %d, want 400", body, rec.Code)
		}
	}

	rec = do(t, srv, "POST", "/api/store/content", `{"item_id":"i1"}`, h)
	if rec.Code != http.StatusOK || rec.Header().Get("Content-Disposition") != "attachment" ||
		rec.Header().Get("Content-Security-Policy") != "sandbox; default-src 'none'" {
		t.Errorf("POST /api/store/content = %d %v, want an attachment in a sandbox", rec.Code, rec.Header())
	}

	bare, err := Listen(context.Background(), Options{API: newStubAPI(), Port: 0, Token: "test-token"})
	if err != nil {
		t.Fatalf("Listen() error = %v", err)
	}
	t.Cleanup(func() { _ = bare.Close() })
	if rec := do(t, bare, "GET", "/api/store", "", appHeaders(bare)); rec.Code != http.StatusNotImplemented {
		t.Errorf("GET /api/store on a build without a store = %d, want 501", rec.Code)
	}
}

// TestStoreUpload: a file arrives as multipart form data with its details,
// keeps the browser's type, and is refused without its details.
func TestStoreUpload(t *testing.T) {
	t.Parallel()
	srv, api := newTestServer(t)

	form := func(withDetails bool) (string, string) {
		var buf bytes.Buffer
		mw := multipart.NewWriter(&buf)
		if withDetails {
			_ = mw.WriteField("details", `{"name":"Manual","description":"","tags":["home"]}`)
			_ = mw.WriteField("allow_duplicate", "true")
		}
		hdr := textproto.MIMEHeader{}
		hdr.Set("Content-Disposition", `form-data; name="file"; filename="manual.pdf"`)
		hdr.Set("Content-Type", "application/pdf")
		part, _ := mw.CreatePart(hdr)
		_, _ = part.Write([]byte("%PDF-1.7"))
		_ = mw.Close()
		return buf.String(), mw.FormDataContentType()
	}

	body, ct := form(true)
	h := appHeaders(srv)
	h["Content-Type"] = ct
	if rec := do(t, srv, "POST", "/api/store/upload", body, h); rec.Code != http.StatusOK {
		t.Fatalf("POST /api/store/upload = %d %s", rec.Code, rec.Body)
	}
	if !slices.Contains(api.calls, "store-upload manual.pdf application/pdf 8 true Manual") {
		t.Errorf("the upload reached the service as %v", api.calls)
	}

	body, ct = form(false)
	h["Content-Type"] = ct
	if rec := do(t, srv, "POST", "/api/store/upload", body, h); rec.Code != http.StatusBadRequest {
		t.Errorf("an upload without details = %d, want 400", rec.Code)
	}
}
