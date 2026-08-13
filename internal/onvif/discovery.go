// Package onvif provides ONVIF device discovery and stream URL extraction
// using github.com/0x524a/onvif-go (modern, actively-maintained ONVIF client).
package onvif

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"time"

	onvifgo "github.com/0x524a/onvif-go"
	"github.com/0x524a/onvif-go/discovery"

	"aiovms/pkg/logger"
)

// DiscoveredDevice represents a camera found via ONVIF WS-Discovery.
type DiscoveredDevice struct {
	IP           string   `json:"ip"`
	Port         int      `json:"port"`
	Name         string   `json:"name"`
	Manufacturer string   `json:"manufacturer"`
	Model        string   `json:"model"`
	Firmware     string   `json:"firmware"`
	SerialNumber string   `json:"serial_number"`
	StreamURLs   []string `json:"stream_urls"`
}

// DeviceInfo holds basic manufacturer/model info.
type DeviceInfo struct {
	Manufacturer string `json:"manufacturer"`
	Model        string `json:"model"`
	Firmware     string `json:"firmware"`
	SerialNumber string `json:"serial_number"`
}

// DiscoveryService is the ONVIF device discovery interface.
type DiscoveryService interface {
	Discover(ctx context.Context, interfaceName string, timeoutSec int) ([]DiscoveredDevice, error)
	// ProbeDevice probes a single device by IP:port via ONVIF unicast (no
	// multicast), returning device info and the primary RTSP stream URL.
	ProbeDevice(ctx context.Context, ip string, port int, user, pass string) (*DiscoveredDevice, error)
}

type discoveryService struct{}

// NewDiscoveryService creates an ONVIF discovery service backed by onvif-go.
func NewDiscoveryService() DiscoveryService {
	return &discoveryService{}
}

// Discover performs WS-Discovery multicast Probe to find ONVIF devices, then
// enriches each with GetDeviceInformation (best-effort, no auth). interfaceName
// optionally binds the multicast socket to a specific NIC (empty = auto).
// timeoutSec is the multicast response window (default 5s).
//
// The multicast probe is provided by onvif-go's discovery package, which has a
// configurable read deadline — unlike the previously-used IOTechSystems/onvif
// library whose 1-second hardcoded deadline caused "same-subnet scan finds
// nothing" on real cameras.
func (s *discoveryService) Discover(ctx context.Context, interfaceName string, timeoutSec int) ([]DiscoveredDevice, error) {
	if timeoutSec <= 0 {
		timeoutSec = 5
	}
	timeout := time.Duration(timeoutSec) * time.Second

	devices, err := discovery.DiscoverWithOptions(ctx, timeout, &discovery.DiscoverOptions{
		NetworkInterface: interfaceName,
	})
	if err != nil {
		return nil, fmt.Errorf("discovery probe: %w", err)
	}
	if len(devices) == 0 {
		return nil, nil
	}

	logger.Infof("ONVIF discovery: found %d devices", len(devices))

	result := make([]DiscoveredDevice, 0, len(devices))
	for _, d := range devices {
		// Filter non-camera WS-Discovery responders (Synology NAS, printers,
		// Windows machines) using the ONVIF Types/Scopes signals.
		if !isONVIFCamera(d) {
			logger.Debugf("ONVIF discovery: skipping non-camera device endpoint=%q types=%v",
				d.GetDeviceEndpoint(), d.Types)
			continue
		}

		endpoint := d.GetDeviceEndpoint()
		ip, port := parseEndpoint(endpoint)

		dev := DiscoveredDevice{
			IP:   ip,
			Port: port,
			Name: d.GetName(),
		}

		// Enrich with device information (no auth, best-effort). GetDeviceInformation
		// is unauthenticated on most cameras.
		if info, err := s.getDeviceInfo(ctx, endpoint, "", ""); err == nil {
			dev.Manufacturer = info.Manufacturer
			dev.Model = info.Model
			dev.Firmware = info.Firmware
			dev.SerialNumber = info.SerialNumber
		}

		result = append(result, dev)
	}
	return result, nil
}

// ProbeDevice probes a single device at ip:port via ONVIF unicast, returning
// device info and the primary RTSP stream URL. Suitable for Docker deployments
// where WS-Discovery multicast does not cross the bridge network boundary.
func (s *discoveryService) ProbeDevice(ctx context.Context, ip string, port int, user, pass string) (*DiscoveredDevice, error) {
	if port <= 0 {
		port = 80
	}
	addr := fmt.Sprintf("%s:%d", ip, port)

	info, err := s.getDeviceInfo(ctx, addr, user, pass)
	if err != nil {
		return nil, err
	}

	dev := &DiscoveredDevice{
		IP:           ip,
		Port:         port,
		Manufacturer: info.Manufacturer,
		Model:        info.Model,
		Firmware:     info.Firmware,
		SerialNumber: info.SerialNumber,
	}

	// Stream URL is best-effort: a device may have no media profiles or may
	// reject the GetStreamUri call even though GetDeviceInformation succeeded.
	if streamURL, err := s.getStreamURL(ctx, addr, user, pass); err == nil && streamURL != "" {
		dev.StreamURLs = []string{streamURL}
	}

	return dev, nil
}

// getStreamURL connects to an ONVIF device and extracts the first profile's RTSP stream URL.
// deviceAddr can be a full URL, "ip:port", or bare IP.
func (s *discoveryService) getStreamURL(ctx context.Context, deviceAddr, user, pass string) (string, error) {
	client, err := onvifgo.NewClient(deviceAddr,
		onvifgo.WithCredentials(user, pass),
		onvifgo.WithTimeout(5*time.Second),
	)
	if err != nil {
		return "", fmt.Errorf("create onvif client: %w", err)
	}

	if err := client.Initialize(ctx); err != nil {
		return "", fmt.Errorf("initialize onvif client: %w", err)
	}

	profiles, err := client.GetProfiles(ctx)
	if err != nil {
		return "", fmt.Errorf("get profiles: %w", err)
	}
	if len(profiles) == 0 {
		return "", fmt.Errorf("no media profiles found")
	}

	uri, err := client.GetStreamURI(ctx, profiles[0].Token)
	if err != nil {
		return "", fmt.Errorf("get stream uri: %w", err)
	}
	return uri.URI, nil
}

// getDeviceInfo connects to an ONVIF device and retrieves manufacturer/model/firmware.
// GetDeviceInformation is typically unauthenticated on most cameras.
func (s *discoveryService) getDeviceInfo(ctx context.Context, deviceAddr, user, pass string) (*DeviceInfo, error) {
	client, err := onvifgo.NewClient(deviceAddr,
		onvifgo.WithCredentials(user, pass),
		onvifgo.WithTimeout(5*time.Second),
	)
	if err != nil {
		return nil, fmt.Errorf("create onvif client: %w", err)
	}

	info, err := client.GetDeviceInformation(ctx)
	if err != nil {
		return nil, fmt.Errorf("get device information: %w", err)
	}

	return &DeviceInfo{
		Manufacturer: info.Manufacturer,
		Model:        info.Model,
		Firmware:     info.FirmwareVersion,
		SerialNumber: info.SerialNumber,
	}, nil
}

// isONVIFCamera reports whether a WS-Discovery responder is an ONVIF network
// video device, as opposed to a generic WS-Discovery responder (Synology NAS,
// Windows machines, printers) that answers ANY Probe regardless of type filter.
//
// Per ONVIF Core Spec, a real camera advertises either:
//   - Types containing "NetworkVideoTransmitter", or
//   - a Scope beginning with "onvif://www.onvif.org/".
//
// The check is permissive (matches either signal) to avoid false-negative drops
// of marginal ONVIF implementations.
func isONVIFCamera(d *discovery.Device) bool {
	for _, t := range d.Types {
		if strings.Contains(t, "NetworkVideoTransmitter") {
			return true
		}
	}
	for _, sc := range d.Scopes {
		if strings.HasPrefix(sc, "onvif://www.onvif.org/") {
			return true
		}
	}
	return false
}

// parseEndpoint extracts host and port from a device endpoint URL.
// Endpoint may be "http://192.168.1.100/onvif/device_service" or "192.168.1.100:8080".
func parseEndpoint(endpoint string) (host string, port int) {
	if endpoint == "" {
		return "", 0
	}
	// Ensure it has a scheme for url.Parse to work uniformly.
	if !strings.Contains(endpoint, "://") {
		endpoint = "http://" + endpoint
	}
	u, err := url.Parse(endpoint)
	if err != nil {
		return "", 0
	}
	host = u.Hostname()
	port = 80
	if p := u.Port(); p != "" {
		if n := parseIntSafe(p); n > 0 {
			port = n
		}
	}
	return host, port
}

func parseIntSafe(s string) int {
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0
		}
		n = n*10 + int(c-'0')
	}
	return n
}
