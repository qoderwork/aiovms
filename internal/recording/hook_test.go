package recording

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

// --- mocks ---

type mockIngester struct {
	calls []struct {
		path          string
		knownComplete bool
	}
	ingestErr error
}

func (m *mockIngester) IngestFile(path string, knownComplete bool) error {
	m.calls = append(m.calls, struct {
		path          string
		knownComplete bool
	}{path, knownComplete})
	return m.ingestErr
}

func newHookTestRouter(ing *mockIngester) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.POST("/internal/segments/complete", NewHookHandler(ing).HandleSegmentComplete)
	return r
}

// --- tests ---

func TestHookSegmentComplete(t *testing.T) {
	ing := &mockIngester{}
	router := newHookTestRouter(ing)

	body, _ := json.Marshal(SegmentCompleteRequest{
		Path:        "cam-a1b2c3d4",
		SegmentPath: "/recordings/cam-a1b2c3d4/2026-08-13_10-00-00.mp4",
	})
	req := httptest.NewRequest(http.MethodPost, "/internal/segments/complete", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
	if len(ing.calls) != 1 {
		t.Fatalf("expected 1 ingest call, got %d", len(ing.calls))
	}
	call := ing.calls[0]
	if call.path != "/recordings/cam-a1b2c3d4/2026-08-13_10-00-00.mp4" {
		t.Errorf("path = %q", call.path)
	}
	// Hook callers know the segment is finalized → status must be exact.
	if !call.knownComplete {
		t.Error("expected knownComplete=true for hook ingestion")
	}
}

func TestHookSegmentCompleteInvalidBody(t *testing.T) {
	ing := &mockIngester{}
	router := newHookTestRouter(ing)

	req := httptest.NewRequest(http.MethodPost, "/internal/segments/complete",
		bytes.NewReader([]byte("not json")))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
	if len(ing.calls) != 0 {
		t.Errorf("expected no ingest call on invalid body, got %d", len(ing.calls))
	}
}

func TestHookSegmentCompleteMissingSegmentPath(t *testing.T) {
	ing := &mockIngester{}
	router := newHookTestRouter(ing)

	body, _ := json.Marshal(SegmentCompleteRequest{Path: "cam-a1b2c3d4"})
	req := httptest.NewRequest(http.MethodPost, "/internal/segments/complete", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

func TestHookSegmentCompleteIngestError(t *testing.T) {
	ing := &mockIngester{ingestErr: errors.New("no camera for path")}
	router := newHookTestRouter(ing)

	body, _ := json.Marshal(SegmentCompleteRequest{
		Path:        "cam-unknown",
		SegmentPath: "/recordings/cam-unknown/x.mp4",
	})
	req := httptest.NewRequest(http.MethodPost, "/internal/segments/complete", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	// Non-fatal system-wide (scanner reconciles), but surfaced as 500.
	if w.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", w.Code)
	}
}
