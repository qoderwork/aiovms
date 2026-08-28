package vault

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func newTestClient(t *testing.T, url string) *Client {
	t.Helper()
	c, err := NewClient(Config{
		Enabled:    true,
		Addr:       url,
		Token:      "test-token",
		Path:       "secret/data/test",
		KVVersion:  2,
		Insecure:   true,
		TimeoutSec: 2,
	})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	return c
}

const testKV2Body = `{"data": {"data": {"database.password": "fake-db-password"}, "metadata": {"version": 1}}}`

func TestRead_HTTP200(t *testing.T) {
	var gotToken string
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/secret/data/test" {
			http.NotFound(w, r)
			return
		}
		gotToken = r.Header.Get("X-Vault-Token")
		w.Write([]byte(testKV2Body))
	}))
	defer srv.Close()

	c := newTestClient(t, srv.URL)
	secrets, err := c.Read(context.Background(), "")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if secrets["database.password"] != "fake-db-password" {
		t.Errorf("database.password = %q", secrets["database.password"])
	}
	if gotToken != "test-token" {
		t.Errorf("X-Vault-Token header = %q, want test-token", gotToken)
	}
}

func TestRead_HTTP403_NotRetryable(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		w.Write([]byte(`{"errors":["permission denied"]}`))
	}))
	defer srv.Close()

	c := newTestClient(t, srv.URL)
	_, err := c.Read(context.Background(), "")
	if err == nil {
		t.Fatal("expected error for 403")
	}
	var se *StatusError
	if !errors.As(err, &se) || se.Code != 403 {
		t.Fatalf("expected StatusError{403}, got %v", err)
	}
	if IsRetryable(err) {
		t.Error("403 should not be retryable")
	}
}

func TestRead_HTTP503_Retryable(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	c := newTestClient(t, srv.URL)
	_, err := c.Read(context.Background(), "")
	if err == nil {
		t.Fatal("expected error for 503")
	}
	if !IsRetryable(err) {
		t.Error("503 should be retryable")
	}
}

func TestRead_BodyCapped(t *testing.T) {
	// Response larger than maxBodyBytes must fail to decode rather than
	// being fully buffered into memory.
	big := `{"padding": "` + strings.Repeat("a", maxBodyBytes) + `"}`
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(big))
	}))
	defer srv.Close()

	c := newTestClient(t, srv.URL)
	_, err := c.Read(context.Background(), "")
	if err == nil {
		t.Fatal("expected error for oversized body")
	}
}

func TestRead_ContextCanceled(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(500 * time.Millisecond)
		w.Write([]byte(testKV2Body))
	}))
	defer srv.Close()

	c := newTestClient(t, srv.URL)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := c.Read(ctx, "")
	if err == nil {
		t.Fatal("expected error for canceled context")
	}
}

func TestHealthCheck_StatusCodes(t *testing.T) {
	cases := []struct {
		code      int
		wantErr   bool
		retryable bool
	}{
		{http.StatusOK, false, false},
		{http.StatusTooManyRequests, true, true},    // standby
		{472, true, true},                           // recovery mode
		{http.StatusServiceUnavailable, true, true}, // sealed
		{http.StatusInternalServerError, true, true},
		{http.StatusForbidden, true, false},
	}
	for _, tc := range cases {
		srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(tc.code)
		}))
		c := newTestClient(t, srv.URL)
		err := c.HealthCheck(context.Background())
		if gotErr := err != nil; gotErr != tc.wantErr {
			t.Errorf("code %d: got error=%v, want %v", tc.code, gotErr, tc.wantErr)
		}
		if err != nil && IsRetryable(err) != tc.retryable {
			t.Errorf("code %d: IsRetryable=%v, want %v", tc.code, IsRetryable(err), tc.retryable)
		}
		srv.Close()
	}
}

func TestReadWithRetry_SuccessAfterTransient(t *testing.T) {
	var calls int32
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/sys/health" {
			if atomic.AddInt32(&calls, 1) <= 2 {
				w.WriteHeader(http.StatusServiceUnavailable) // sealed
				return
			}
			w.WriteHeader(http.StatusOK)
			return
		}
		w.Write([]byte(testKV2Body))
	}))
	defer srv.Close()

	c := newTestClient(t, srv.URL)
	backoff := []time.Duration{time.Millisecond, time.Millisecond}
	secrets, err := c.ReadWithRetry(context.Background(), backoff)
	if err != nil {
		t.Fatalf("ReadWithRetry: %v", err)
	}
	if secrets["database.password"] != "fake-db-password" {
		t.Errorf("database.password = %q", secrets["database.password"])
	}
	if got := atomic.LoadInt32(&calls); got != 3 {
		t.Errorf("health calls = %d, want 3 (2 sealed + 1 ok)", got)
	}
}

func TestReadWithRetry_NonRetryableFailsFast(t *testing.T) {
	var calls int32
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusForbidden) // bad token: must not be retried
	}))
	defer srv.Close()

	c := newTestClient(t, srv.URL)
	backoff := []time.Duration{time.Millisecond, time.Millisecond, time.Millisecond}
	_, err := c.ReadWithRetry(context.Background(), backoff)
	if err == nil {
		t.Fatal("expected error")
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Errorf("calls = %d, want 1 (non-retryable must fail fast)", got)
	}
}

func TestReadWithRetry_ScheduleExhausted(t *testing.T) {
	var calls int32
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusServiceUnavailable) // always sealed
	}))
	defer srv.Close()

	c := newTestClient(t, srv.URL)
	backoff := []time.Duration{time.Millisecond, time.Millisecond}
	_, err := c.ReadWithRetry(context.Background(), backoff)
	if err == nil {
		t.Fatal("expected error after exhausting schedule")
	}
	if !strings.Contains(err.Error(), "after 3 attempts") {
		t.Errorf("error should mention attempt count, got: %v", err)
	}
}

func TestReadWithRetry_EmptyScheduleSingleAttempt(t *testing.T) {
	var calls int32
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	c := newTestClient(t, srv.URL)
	_, err := c.ReadWithRetry(context.Background(), nil)
	if err == nil {
		t.Fatal("expected error")
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Errorf("calls = %d, want 1 (nil schedule = single attempt)", got)
	}
}

func TestIsRetryable(t *testing.T) {
	// Closed server => connection refused => transport error => retryable.
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	c := newTestClient(t, srv.URL)
	srv.Close()
	err := c.HealthCheck(context.Background())
	if err == nil {
		t.Fatal("expected connection error")
	}
	if !IsRetryable(err) {
		t.Errorf("transport error should be retryable: %v", err)
	}
	if IsRetryable(context.Canceled) {
		t.Error("context.Canceled should not be retryable")
	}
	if !IsRetryable(fmt.Errorf("some unknown error")) {
		t.Error("unknown errors are treated as transient by design")
	}
}

func TestParseSecretData_KV2(t *testing.T) {
	body := []byte(`{
		"request_id": "abc",
		"data": {
			"data": {
				"database.password": "fake-db-password-for-test",
				"encryption.key": "fake-encryption-key-32-bytes-aaaa"
			},
			"metadata": {"version": 1, "created_time": "2026-01-01T00:00:00Z"}
		}
	}`)

	result, err := parseSecretData(body, 2)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if result["database.password"] != "fake-db-password-for-test" {
		t.Errorf("database.password = %q, want %q", result["database.password"], "fake-db-password-for-test")
	}
	if result["encryption.key"] != "fake-encryption-key-32-bytes-aaaa" {
		t.Errorf("encryption.key = %q, want %q", result["encryption.key"], "fake-encryption-key-32-bytes-aaaa")
	}
	// metadata must NOT leak into result
	if _, ok := result["version"]; ok {
		t.Error("metadata fields leaked into secret data")
	}
}

func TestParseSecretData_KV1(t *testing.T) {
	body := []byte(`{
		"request_id": "abc",
		"data": {
			"salt": "test-salt-value",
			"emailSecret": "fake-email-secret-for-unit-test"
		}
	}`)

	result, err := parseSecretData(body, 1)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if result["salt"] != "test-salt-value" {
		t.Errorf("salt = %q, want %q", result["salt"], "test-salt-value")
	}
	if result["emailSecret"] != "fake-email-secret-for-unit-test" {
		t.Errorf("emailSecret = %q, want %q", result["emailSecret"], "fake-email-secret-for-unit-test")
	}
}

func TestParseSecretData_KV2_EmptySecret(t *testing.T) {
	// v2 path that was written with no fields, or soft-deleted:
	// data.data is null, data.metadata still present
	body := []byte(`{
		"data": {
			"data": null,
			"metadata": {"version": 0, "destroyed": true}
		}
	}`)

	_, err := parseSecretData(body, 2)
	if err == nil {
		t.Fatal("expected error for empty/deleted v2 secret, got nil")
	}
}

func TestParseSecretData_KV2_NoDataField(t *testing.T) {
	// Response with no data at all (e.g. wrong path)
	body := []byte(`{"errors": ["no handler for route"]}`)

	_, err := parseSecretData(body, 2)
	if err == nil {
		t.Fatal("expected error for response without data, got nil")
	}
}

func TestParseSecretData_KV1_NoDataField(t *testing.T) {
	body := []byte(`{"errors": ["no handler for route"]}`)

	_, err := parseSecretData(body, 1)
	if err == nil {
		t.Fatal("expected error for response without data, got nil")
	}
}

func TestParseSecretData_InvalidJSON(t *testing.T) {
	body := []byte(`{not valid json`)

	_, err := parseSecretData(body, 2)
	if err == nil {
		t.Fatal("expected error for invalid JSON, got nil")
	}
}

func TestToStringMap(t *testing.T) {
	m := map[string]interface{}{
		"str":   "hello",
		"empty": nil,
		"num":   float64(42),
		"bool":  true,
	}
	result := toStringMap(m)
	if result["str"] != "hello" {
		t.Errorf("str = %q, want %q", result["str"], "hello")
	}
	if result["empty"] != "" {
		t.Errorf("empty = %q, want %q", result["empty"], "")
	}
	if result["num"] != "42" {
		t.Errorf("num = %q, want %q", result["num"], "42")
	}
	if result["bool"] != "true" {
		t.Errorf("bool = %q, want %q", result["bool"], "true")
	}
}
