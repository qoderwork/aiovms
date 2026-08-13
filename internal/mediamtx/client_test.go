package mediamtx

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestAddPathUpsert verifies the upsert semantics: when the path already
// exists (MediaMTX answers 400 to add), AddPath falls back to patching the
// full desired config instead of failing. This makes Create/reconcile retries
// safe — a plain add would leave an orphaned DB camera record on retry.
func TestAddPathUpsert(t *testing.T) {
	var added, patched bool
	mux := http.NewServeMux()
	mux.HandleFunc("/v3/config/paths/add/cam-1", func(w http.ResponseWriter, r *http.Request) {
		added = true
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte("path already exists"))
	})
	mux.HandleFunc("/v3/config/paths/patch/cam-1", func(w http.ResponseWriter, r *http.Request) {
		patched = true
		w.WriteHeader(http.StatusOK)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := NewClient(srv.URL)
	err := c.AddPath("cam-1", PathConfig{
		Source:                "rtsp://192.0.2.10/stream",
		SourceOnDemand:        true,
		RecordPath:            "/recordings/%path/%Y-%m-%d_%H-%M-%S",
		RecordSegmentDuration: "1m",
	})
	if err != nil {
		t.Fatalf("AddPath upsert: %v", err)
	}
	if !added {
		t.Error("expected add to be attempted first")
	}
	if !patched {
		t.Error("expected patch fallback after 400")
	}
}

// TestAddPathSuccess verifies the happy path: add succeeds, no fallback.
func TestAddPathSuccess(t *testing.T) {
	var patched bool
	mux := http.NewServeMux()
	mux.HandleFunc("/v3/config/paths/add/cam-1", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("/v3/config/paths/patch/cam-1", func(w http.ResponseWriter, r *http.Request) {
		patched = true
		w.WriteHeader(http.StatusOK)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := NewClient(srv.URL)
	if err := c.AddPath("cam-1", PathConfig{Source: "rtsp://192.0.2.10/stream"}); err != nil {
		t.Fatalf("AddPath: %v", err)
	}
	if patched {
		t.Error("patch fallback must not run when add succeeds")
	}
}

// TestAddPathServerError verifies that non-400 errors propagate unchanged and
// do NOT trigger the patch fallback (the fallback is exclusively for the
// "already exists" convergence case).
func TestAddPathServerError(t *testing.T) {
	var patched bool
	mux := http.NewServeMux()
	mux.HandleFunc("/v3/config/paths/add/cam-1", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("internal error"))
	})
	mux.HandleFunc("/v3/config/paths/patch/cam-1", func(w http.ResponseWriter, r *http.Request) {
		patched = true
		w.WriteHeader(http.StatusOK)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := NewClient(srv.URL)
	err := c.AddPath("cam-1", PathConfig{Source: "rtsp://192.0.2.10/stream"})
	if err == nil {
		t.Fatal("expected error for 500 response")
	}
	if patched {
		t.Error("patch fallback must not run on non-400 errors")
	}
}

// TestAddPathFallbackFails verifies that when both add (400) and the patch
// fallback fail, a combined error is returned.
func TestAddPathFallbackFails(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/v3/config/paths/add/cam-1", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte("path already exists"))
	})
	mux.HandleFunc("/v3/config/paths/patch/cam-1", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("boom"))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := NewClient(srv.URL)
	err := c.AddPath("cam-1", PathConfig{Source: "rtsp://192.0.2.10/stream"})
	if err == nil {
		t.Fatal("expected error when both add and patch fail")
	}
}
