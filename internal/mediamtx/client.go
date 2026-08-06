package mediamtx

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"aiovms/pkg/logger"
)

// Client provides a Go client for MediaMTX v3 HTTP API (v1.19.3).
type Client struct {
	baseURL    string
	httpClient *http.Client
}

// NewClient creates a MediaMTX API client.
func NewClient(baseURL string) *Client {
	return &Client{
		baseURL: baseURL,
		httpClient: &http.Client{
			Timeout: 10 * time.Second,
		},
	}
}

// AddPath registers a new RTSP source path in MediaMTX.
func (c *Client) AddPath(name string, cfg PathConfig) error {
	body, _ := json.Marshal(cfg)
	url := fmt.Sprintf("%s/v3/config/paths/%s", c.baseURL, name)
	req, _ := http.NewRequest("POST", url, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	return c.do(req)
}

// DeletePath removes a path from MediaMTX.
func (c *Client) DeletePath(name string) error {
	url := fmt.Sprintf("%s/v3/config/paths/%s", c.baseURL, name)
	req, _ := http.NewRequest("DELETE", url, nil)
	return c.do(req)
}

// PatchPath partially updates a path config (e.g. enable/disable recording).
func (c *Client) PatchPath(name string, patch map[string]any) error {
	body, _ := json.Marshal(patch)
	url := fmt.Sprintf("%s/v3/config/paths/%s", c.baseURL, name)
	req, _ := http.NewRequest("PATCH", url, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	return c.do(req)
}

// ListPaths returns all registered paths from MediaMTX.
func (c *Client) ListPaths() ([]PathInfo, error) {
	url := fmt.Sprintf("%s/v3/config/paths/list", c.baseURL)
	resp, err := c.httpClient.Get(url)
	if err != nil {
		return nil, fmt.Errorf("list paths: %w", err)
	}
	defer resp.Body.Close()

	var result struct {
		Items []PathInfo `json:"items"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode paths: %w", err)
	}
	return result.Items, nil
}

// SnapshotPath returns the URL for fetching a JPEG snapshot from a path.
// MediaMTX serves JPEG frames directly at the stream root path.
func (c *Client) SnapshotPath(name string) string {
	return fmt.Sprintf("%s/%s", c.baseURL, name)
}

// HealthCheck checks if MediaMTX API is reachable.
func (c *Client) HealthCheck() error {
	url := fmt.Sprintf("%s/v3/config/global/get", c.baseURL)
	resp, err := c.httpClient.Get(url)
	if err != nil {
		return fmt.Errorf("mediamtx health check: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return fmt.Errorf("mediamtx health check: status %d", resp.StatusCode)
	}
	return nil
}

func (c *Client) do(req *http.Request) error {
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("mediamtx request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(resp.Body)
		logger.Errorf("mediamtx %s %s -> %d: %s", req.Method, req.URL.Path, resp.StatusCode, string(body))
		return fmt.Errorf("mediamtx error %d: %s", resp.StatusCode, string(body))
	}
	return nil
}

// PathConfig is the payload for AddPath.
type PathConfig struct {
	Source         string `json:"source"`
	SourceOnDemand bool   `json:"sourceOnDemand"`
	Record         bool   `json:"record,omitempty"`
}

// PathInfo is returned by ListPaths.
type PathInfo struct {
	Name    string `json:"name"`
	Source  string `json:"source"`
	Ready   bool   `json:"ready"`
	Tracks  []any  `json:"tracks"`
}
