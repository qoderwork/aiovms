// Package vault provides a lightweight Vault client for reading secrets
// from HashiCorp Vault at application startup. It uses only the standard
// library — no external SDK dependency — and supports both KV v1 and KV v2
// secret engines, TLS with custom CA, and token-based authentication.
//
// Typical usage:
//
//	client, _ := vault.NewClient(vault.Config{
//	    Addr:      "https://vault:8200",
//	    Token:     os.Getenv("VAULT_TOKEN"),
//	    Path:      "secret/data/vms", // KV v2; use "secret/vms" for v1
//	    KVVersion: 2,
//	})
//	secrets, err := client.ReadWithRetry(ctx, vault.DefaultRetryBackoff)
//	if err != nil {
//	    log.Printf("vault read failed, falling back to config: %v", err)
//	}
package vault

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// maxBodyBytes caps how much of a Vault response is read into memory,
// protecting against a misbehaving server or storage backend returning
// a huge body.
const maxBodyBytes = 1 << 20 // 1 MiB

// DefaultRetryBackoff retries transient Vault failures (container restart,
// unseal window, network blips) over roughly 107 seconds total, covering
// manual unseal workflows. Rough schedule: attempt 1 (immediate), wait 2s,
// attempt 2, wait 5s, attempt 3, wait 10s, attempt 4, wait 30s, attempt 5,
// wait 60s, final attempt 6.
var DefaultRetryBackoff = []time.Duration{
	2 * time.Second, 5 * time.Second, 10 * time.Second, 30 * time.Second, 60 * time.Second,
}

// StatusError represents a Vault response with a non-200 HTTP status code.
type StatusError struct {
	Code int
	Msg  string
}

func (e *StatusError) Error() string { return e.Msg }

// IsRetryable reports whether err is a transient failure worth retrying:
// network-level errors (connection refused, timeouts) and Vault states
// that may resolve on their own (sealed, standby, recovery, 5xx).
// Deterministic failures — bad token (403), bad request (400), not found
// (404) — are not retried: a second attempt cannot succeed.
func IsRetryable(err error) bool {
	if errors.Is(err, context.Canceled) {
		return false
	}
	var se *StatusError
	if errors.As(err, &se) {
		switch se.Code {
		case http.StatusTooManyRequests, 472,
			http.StatusInternalServerError, http.StatusBadGateway,
			http.StatusServiceUnavailable, http.StatusGatewayTimeout:
			return true
		}
		return false
	}
	// Transport-level errors (DNS, connection refused, timeouts) are
	// considered transient.
	return true
}

// SimpleLogger is the minimal logging interface expected by ReadWithRetry.
// Pass nil to disable attempt-level logging. Compatible with aiovms'
// pkg/logger and stdlib log.Logger via adapters.
type SimpleLogger interface {
	Warnf(format string, args ...interface{})
}

// Config holds Vault client connection settings.
type Config struct {
	Enabled    bool   `mapstructure:"enabled"`
	Addr       string `mapstructure:"addr"`        // e.g. "https://vault:8200"
	Token      string `mapstructure:"token"`       // Vault token (root or scoped)
	Path       string `mapstructure:"path"`        // secret path, e.g. "secret/data/vms" (v2) or "secret/vms" (v1)
	KVVersion  int    `mapstructure:"kv_version"`  // 1 or 2; default 2
	CABase64   string `mapstructure:"ca_base64"`   // optional: base64-encoded PEM CA cert for HTTPS
	Insecure   bool   `mapstructure:"insecure"`    // skip TLS verification (dev only)
	TimeoutSec int    `mapstructure:"timeout_sec"` // HTTP timeout in seconds, default 10
}

// Client is a lightweight Vault HTTP API client.
type Client struct {
	cfg  Config
	http *http.Client
}

// NewClient creates a Vault client. Returns (nil, nil) when disabled,
// allowing callers to skip nil checks in optional-vault deployments.
func NewClient(cfg Config) (*Client, error) {
	if !cfg.Enabled {
		return nil, nil
	}
	if cfg.Addr == "" {
		return nil, fmt.Errorf("vault addr is required")
	}
	if cfg.Token == "" {
		return nil, fmt.Errorf("vault token is required")
	}
	if cfg.KVVersion == 0 {
		cfg.KVVersion = 2 // default to KV v2
	}
	if cfg.TimeoutSec <= 0 {
		cfg.TimeoutSec = 10
	}

	tlsConfig := &tls.Config{
		MinVersion: tls.VersionTLS12,
	}
	if cfg.Insecure {
		tlsConfig.InsecureSkipVerify = true
	}
	if cfg.CABase64 != "" {
		caPEM, err := base64.StdEncoding.DecodeString(cfg.CABase64)
		if err != nil {
			return nil, fmt.Errorf("decode CA cert: %w", err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(caPEM) {
			return nil, fmt.Errorf("failed to parse CA certificate")
		}
		tlsConfig.RootCAs = pool
	}

	return &Client{
		cfg: cfg,
		http: &http.Client{
			Timeout:   time.Duration(cfg.TimeoutSec) * time.Second,
			Transport: &http.Transport{TLSClientConfig: tlsConfig},
		},
	}, nil
}

// Read fetches secrets at the given path. If path is empty, uses cfg.Path.
// Uses cfg.KVVersion to determine how to parse the response.
func (c *Client) Read(ctx context.Context, path string) (map[string]string, error) {
	if path == "" {
		path = c.cfg.Path
	}
	if path == "" {
		return nil, fmt.Errorf("vault path is required")
	}

	url := strings.TrimRight(c.cfg.Addr, "/") + "/v1/" + strings.TrimLeft(path, "/")
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("X-Vault-Token", c.cfg.Token)
	req.Header.Set("X-Vault-Request", "true")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("vault request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, &StatusError{
			Code: resp.StatusCode,
			Msg:  fmt.Sprintf("vault returned %d: %s", resp.StatusCode, truncate(body, 200)),
		}
	}

	return parseSecretData(body, c.cfg.KVVersion)
}

// ReadField reads a single field from the given path. Returns empty string
// with no error if the field exists but is empty. Returns an error if the
// field does not exist.
func (c *Client) ReadField(ctx context.Context, path, field string) (string, error) {
	secrets, err := c.Read(ctx, path)
	if err != nil {
		return "", err
	}
	val, ok := secrets[field]
	if !ok {
		return "", fmt.Errorf("field %q not found in vault path %q", field, path)
	}
	return val, nil
}

// ReadWithRetry performs HealthCheck + Read, retrying transient failures
// (vault restarting, sealed, standby, network blips) with the given backoff
// schedule. It returns as soon as both steps succeed; non-retryable errors
// fail fast; an exhausted schedule returns the last error wrapped with the
// attempt count. Pass a nil/empty schedule for a single attempt.
//
// If log is non-nil each transient failure (and the subsequent wait) is
// logged at Warn level so operators can observe the unseal / restart window
// instead of seeing a silent pause.
func (c *Client) ReadWithRetry(ctx context.Context, backoff []time.Duration, log SimpleLogger) (map[string]string, error) {
	var lastErr error
	for attempt := 0; ; attempt++ {
		if lastErr = c.HealthCheck(ctx); lastErr == nil {
			secrets, err := c.Read(ctx, c.cfg.Path)
			if err == nil {
				return secrets, nil
			}
			lastErr = fmt.Errorf("vault read %s: %w", c.cfg.Path, err)
		} else {
			lastErr = fmt.Errorf("vault health check: %w", lastErr)
		}

		if !IsRetryable(lastErr) {
			return nil, lastErr
		}
		if attempt >= len(backoff) {
			return nil, fmt.Errorf("after %d attempts: %w", attempt+1, lastErr)
		}
		sleep := backoff[attempt]
		if log != nil {
			log.Warnf("vault attempt %d/%d: %v — retrying in %v",
				attempt+1, len(backoff)+1, lastErr, sleep)
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(sleep):
		}
	}
}

// HealthCheck verifies Vault is reachable and unsealed.
// Does NOT send the token: sys/health is unauthenticated.
func (c *Client) HealthCheck(ctx context.Context) error {
	url := strings.TrimRight(c.cfg.Addr, "/") + "/v1/sys/health"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("vault unreachable: %w", err)
	}
	defer resp.Body.Close()
	// Drain and discard up to 64 KiB of the response body (sys/health
	// responses are tiny) so the TCP connection is reusable for the
	// subsequent Read call on keep-alive transports.
	_, _ = io.CopyN(io.Discard, resp.Body, 64<<10)

	// 200 = unsealed, 429 = standby, 472 = recovery mode, 503 = sealed
	switch resp.StatusCode {
	case http.StatusOK:
		return nil
	case http.StatusTooManyRequests:
		return &StatusError{Code: resp.StatusCode, Msg: "vault is in standby mode"}
	case 472:
		return &StatusError{Code: resp.StatusCode, Msg: "vault is in recovery mode"}
	case http.StatusServiceUnavailable:
		return &StatusError{Code: resp.StatusCode, Msg: "vault is sealed"}
	default:
		return &StatusError{Code: resp.StatusCode, Msg: fmt.Sprintf("vault health check returned %d", resp.StatusCode)}
	}
}

// Path returns the configured secret path.
func (c *Client) Path() string {
	return c.cfg.Path
}

// parseSecretData extracts the secret key-values from a Vault API response.
// kvVersion determines which response format to expect:
//
//   - KV v1: {"data": {"key": "value"}}
//   - KV v2: {"data": {"data": {"key": "value"}, "metadata": {...}}}
//
// When the secret data is empty (v2 secret written with no fields, or v1
// path deleted), returns an empty map rather than falling through to
// metadata or erroring out.
func parseSecretData(body []byte, kvVersion int) (map[string]string, error) {
	if kvVersion == 1 {
		var v1 struct {
			Data map[string]interface{} `json:"data"`
		}
		if err := json.Unmarshal(body, &v1); err != nil {
			return nil, fmt.Errorf("decode KV v1 response: %w", err)
		}
		if v1.Data == nil {
			return nil, fmt.Errorf("no data field in vault response")
		}
		return toStringMap(v1.Data), nil
	}

	// KV v2
	var v2 struct {
		Data struct {
			Data     map[string]interface{} `json:"data"`
			Metadata map[string]interface{} `json:"metadata"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &v2); err != nil {
		return nil, fmt.Errorf("decode KV v2 response: %w", err)
	}
	if v2.Data.Data == nil {
		return nil, fmt.Errorf("no secret data at this path (may be deleted or empty)")
	}
	return toStringMap(v2.Data.Data), nil
}

// toStringMap converts map[string]interface{} to map[string]string.
func toStringMap(m map[string]interface{}) map[string]string {
	result := make(map[string]string, len(m))
	for k, v := range m {
		switch val := v.(type) {
		case string:
			result[k] = val
		case nil:
			result[k] = ""
		default:
			result[k] = fmt.Sprintf("%v", val)
		}
	}
	return result
}

func truncate(b []byte, n int) string {
	if len(b) <= n {
		return string(b)
	}
	return string(b[:n]) + "..."
}
