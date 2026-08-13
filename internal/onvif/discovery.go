// Package onvif provides ONVIF device discovery and stream URL extraction
// using IOTechSystems/onvif library.

package onvif

import (
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"strings"
	"time"

	onvif2 "github.com/IOTechSystems/onvif"
	"github.com/IOTechSystems/onvif/media"
	xsd "github.com/IOTechSystems/onvif/xsd/onvif"

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
	GetStreamURL(ctx context.Context, deviceAddr, user, pass string) (string, error)
	GetDeviceInfo(ctx context.Context, deviceAddr, user, pass string) (*DeviceInfo, error)
}

type discoveryService struct{}

// NewDiscoveryService creates an ONVIF discovery service backed by IOTechSystems/onvif.
func NewDiscoveryService() DiscoveryService {
	return &discoveryService{}
}

// Discover performs WS-Discovery multicast Probe to find ONVIF devices.
// interfaceName optionally binds the multicast socket to a specific NIC
// (empty = auto-select). timeoutSec controls the maximum wait time for
// device responses (default 5s).
//
// The multicast probe is self-implemented (see multicast.go) because the
// upstream IOTechSystems/onvif library hardcodes a 1-second read deadline,
// which is too short for real cameras and caused "same-subnet scan finds
// nothing" on multi-NIC hosts.
func (s *discoveryService) Discover(ctx context.Context, interfaceName string, timeoutSec int) ([]DiscoveredDevice, error) {
	if timeoutSec <= 0 {
		timeoutSec = 5
	}

	type result struct {
		devices []onvif2.Device
		err     error
	}
	ch := make(chan result, 1)
	go func() {
		devices, err := multicastProbe(interfaceName, time.Duration(timeoutSec)*time.Second)
		ch <- result{devices, err}
	}()

	var r result
	select {
	case r = <-ch:
	case <-ctx.Done():
		return nil, ctx.Err()
	}

	if r.err != nil {
		return nil, fmt.Errorf("discovery probe: %w", r.err)
	}
	onvifDevices := r.devices
	if len(onvifDevices) == 0 {
		return nil, nil
	}

	logger.Infof("ONVIF discovery: found %d devices", len(onvifDevices))

	var devices []DiscoveredDevice
	for _, d := range onvifDevices {
		select {
		case <-ctx.Done():
			return devices, ctx.Err()
		default:
		}

		info := d.GetDeviceInfo()

		// Filter out non-camera WS-Discovery responders (Synology NAS,
		// printers, scanners, Windows machines) that answer any Probe
		// regardless of type filter. Adapted from MiBeeNvr (#266 fix).
		if !isONVIFCamera(info.Manufacturer, info.Model) {
			logger.Debugf("ONVIF discovery: skipping non-camera device manufacturer=%q model=%q",
				info.Manufacturer, info.Model)
			continue
		}

		params := d.GetDeviceParams()

		dev := DiscoveredDevice{
			IP:           parseIP(params.Xaddr),
			Port:         parsePort(params.Xaddr),
			Name:         info.Name,
			Manufacturer: info.Manufacturer,
			Model:        info.Model,
			Firmware:     info.FirmwareVersion,
			SerialNumber: info.SerialNumber,
		}

		if urls, err := s.tryGetStreamURLs(&d, params.Username, params.Password); err == nil {
			dev.StreamURLs = urls
		}

		devices = append(devices, dev)
	}
	return devices, nil
}

// GetStreamURL connects to an ONVIF device and extracts the first RTSP stream URL.
func (s *discoveryService) GetStreamURL(ctx context.Context, deviceAddr, user, pass string) (string, error) {
	dev, err := onvif2.NewDevice(onvif2.DeviceParams{
		Xaddr: deviceAddr, Username: user, Password: pass,
	})
	if err != nil {
		return "", fmt.Errorf("connect device: %w", err)
	}

	resp, err := dev.CallMethod(media.GetProfiles{})
	if err != nil {
		return "", fmt.Errorf("get profiles: %w", err)
	}
	defer resp.Body.Close()

	var profilesResp media.GetProfilesResponse
	if err := decodeXMLResp(resp.Body, &profilesResp); err != nil {
		return "", fmt.Errorf("decode profiles: %w", err)
	}
	if len(profilesResp.Profiles) == 0 {
		return "", fmt.Errorf("no media profiles found")
	}

	// Get stream URI from first profile
	profile := profilesResp.Profiles[0]
	proto := xsd.TransportProtocol("RTSP")
	streamResp, err := dev.CallMethod(media.GetStreamUri{
		StreamSetup: &xsd.StreamSetup{
			Stream:    streamType("RTP-Unicast"),
			Transport: &xsd.Transport{Protocol: &proto},
		},
		ProfileToken: &profile.Token,
	})
	if err != nil {
		return "", fmt.Errorf("get stream uri: %w", err)
	}
	defer streamResp.Body.Close()

	var uriResp media.GetStreamUriResponse
	if err := decodeXMLResp(streamResp.Body, &uriResp); err != nil {
		return "", fmt.Errorf("decode stream uri: %w", err)
	}

	return string(uriResp.MediaUri.Uri), nil
}

// GetDeviceInfo connects to an ONVIF device and retrieves manufacturer/model/firmware.
func (s *discoveryService) GetDeviceInfo(ctx context.Context, deviceAddr, user, pass string) (*DeviceInfo, error) {
	dev, err := onvif2.NewDevice(onvif2.DeviceParams{
		Xaddr: deviceAddr, Username: user, Password: pass,
	})
	if err != nil {
		return nil, fmt.Errorf("connect device: %w", err)
	}
	info := dev.GetDeviceInfo()
	return &DeviceInfo{
		Manufacturer: info.Manufacturer,
		Model:        info.Model,
		Firmware:     info.FirmwareVersion,
		SerialNumber: info.SerialNumber,
	}, nil
}

func (s *discoveryService) tryGetStreamURLs(d *onvif2.Device, user, pass string) ([]string, error) {
	params := d.GetDeviceParams()
	dev, err := onvif2.NewDevice(onvif2.DeviceParams{
		Xaddr: params.Xaddr, Username: user, Password: pass,
	})
	if err != nil {
		return nil, err
	}

	resp, err := dev.CallMethod(media.GetProfiles{})
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var profilesResp media.GetProfilesResponse
	if err := decodeXMLResp(resp.Body, &profilesResp); err != nil {
		return nil, err
	}

	var urls []string
	proto := xsd.TransportProtocol("RTSP")
	for _, p := range profilesResp.Profiles {
		profile := p
		uriResp, err := dev.CallMethod(media.GetStreamUri{
			StreamSetup: &xsd.StreamSetup{
				Stream:    streamType("RTP-Unicast"),
				Transport: &xsd.Transport{Protocol: &proto},
			},
			ProfileToken: &profile.Token,
		})
		if err != nil {
			continue
		}
		var uri media.GetStreamUriResponse
		if err := decodeXMLResp(uriResp.Body, &uri); err != nil {
			uriResp.Body.Close()
			continue
		}
		uriResp.Body.Close()
		if uri.MediaUri.Uri != "" {
			urls = append(urls, string(uri.MediaUri.Uri))
		}
	}
	return urls, nil
}

func decodeXMLResp(r io.Reader, v interface{}) error {
	body, err := io.ReadAll(r)
	if err != nil {
		return fmt.Errorf("read body: %w", err)
	}
	if err := xml.Unmarshal(body, v); err != nil {
		return fmt.Errorf("xml unmarshal: %w", err)
	}
	return nil
}

func streamType(s string) *xsd.StreamType {
	t := xsd.StreamType(s)
	return &t
}

func parseIP(addr string) string {
	for i := len(addr) - 1; i >= 0; i-- {
		if addr[i] == ':' {
			return addr[:i]
		}
	}
	return addr
}

// isONVIFCamera heuristically filters non-camera WS-Discovery responders.
// Many NAS devices (Synology), printers, and Windows machines respond to
// ANY WS-Discovery Probe regardless of the d:Types filter. They typically
// advertise manufacturer/model strings that are clearly not cameras.
//
// Filtering at the discovery boundary prevents empty-shell camera records
// from being created for devices that can never stream video.
// Adapted from MiBeeNvr (MIT License, issue #266).
func isONVIFCamera(manufacturer, model string) bool {
	m := strings.ToLower(manufacturer)
	md := strings.ToLower(model)

	// Known non-camera WS-Discovery responders.
	nonCamera := []string{
		"synology", "qnap", "western digital", "wd ", "seagate",
		"hewlett-packard", "hp ", "canon", "epson", "brother",
		"xerox", "ricoh", "lexmark", "samsung printer", "windows",
		"microsoft", "netgear", "cisco", "ubiquiti", "aruba",
		"raspberry pi",
	}
	for _, nc := range nonCamera {
		if strings.Contains(m, nc) || strings.Contains(md, nc) {
			return false
		}
	}
	return true
}

func parsePort(addr string) int {
	for i := len(addr) - 1; i >= 0; i-- {
		if addr[i] == ':' {
			port := 0
			for _, c := range addr[i+1:] {
				if c < '0' || c > '9' {
					return 80
				}
				port = port*10 + int(c-'0')
			}
			return port
		}
	}
	return 80
}
