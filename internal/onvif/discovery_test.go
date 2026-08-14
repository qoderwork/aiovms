package onvif

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestIsAuthError(t *testing.T) {
	cases := []struct {
		name string
		err  string
		want bool
	}{
		{"status 401", "GetDeviceInformation failed: HTTP request failed with status 401: <?xml", true},
		{"status 403", "HTTP request failed with status 403: <?xml", true},
		{"soap 401", "HTTP Error: 401 Unauthorized", true},
		{"status 400 soap", "HTTP request failed with status 400: <?xml", false},
		{"status 404", "HTTP request failed with status 404: not found", false},
		{"status 500", "HTTP request failed with status 500: <?xml", false},
		{"connection refused", "dial tcp 1.2.3.4:80: connect: connection refused", false},
		{"timeout", "context deadline exceeded", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := isAuthError(errors.New(c.err)); got != c.want {
				t.Errorf("isAuthError(%q) = %v, want %v", c.err, got, c.want)
			}
		})
	}
}

func TestIsAuthErrorNil(t *testing.T) {
	if isAuthError(nil) {
		t.Error("isAuthError(nil) = true, want false")
	}
}

// TestGetDeviceInfo_AuthRequired verifies that a 401 from the device yields a
// friendly "authentication required" error (not a raw SOAP dump).
func TestGetDeviceInfo_AuthRequired(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	s := &discoveryService{}
	// Use host:port form, matching how ProbeDevice builds the address.
	addr := srv.Listener.Addr().String()
	_, err := s.getDeviceInfo(context.Background(), addr, "admin", "wrong")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !isAuthError(err) {
		t.Errorf("expected auth error, got %v", err)
	}
	if !strings.Contains(err.Error(), "authentication required") {
		t.Errorf("expected friendly auth message, got %v", err)
	}
}
