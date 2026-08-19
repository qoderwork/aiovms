// Package rtsp provides lightweight RTSP stream probing via gortsplib.
//
// It is a pure-Go, ffprobe-free way to learn the actual video codec of an RTSP
// stream: it only issues an RTSP DESCRIBE (no data-plane pull), so it is fast
// and carries no ffmpeg dependency.
package rtsp

import (
	"fmt"
	"net/url"
	"time"

	"github.com/bluenviron/gortsplib/v5"
	"github.com/bluenviron/gortsplib/v5/pkg/base"
	"github.com/bluenviron/gortsplib/v5/pkg/format"
)

// ProbeEncoding connects to an RTSP stream and reports its actual video codec.
// It returns "H264", "H265" or "MJPEG" on success, or an error when the probe
// fails (unreachable host, auth failure, DESCRIBE rejected, unknown format).
//
// The result is authoritative over ONVIF metadata: some cameras advertise H264
// in their ONVIF profile while actually streaming H265, so callers should prefer
// this value over the ONVIF-declared encoding.
//
// Ported from MiBeeNvr internal/recorder probeRTSPEncodingFor.
func ProbeEncoding(rtspURL, username, password string) (string, error) {
	u, err := base.ParseURL(rtspURL)
	if err != nil {
		return "", fmt.Errorf("parse rtsp url: %w", err)
	}
	if u.User == nil && username != "" {
		u.User = url.UserPassword(username, password)
	}

	tcp := gortsplib.ProtocolTCP
	client := &gortsplib.Client{
		Scheme:       u.Scheme,
		Host:         u.Host,
		Protocol:     &tcp,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Second,
	}
	if err := client.Start(); err != nil {
		return "", fmt.Errorf("rtsp client start: %w", err)
	}
	defer client.Close()

	desc, _, err := client.Describe(u)
	if err != nil {
		return "", fmt.Errorf("rtsp describe: %w", err)
	}

	// Check H265 first — many ONVIF cameras report H264 but stream H265.
	var h265Forma *format.H265
	if desc.FindFormat(&h265Forma) != nil {
		return "H265", nil
	}
	var h264Forma *format.H264
	if desc.FindFormat(&h264Forma) != nil {
		return "H264", nil
	}
	var mjpegForma *format.MJPEG
	if desc.FindFormat(&mjpegForma) != nil {
		return "MJPEG", nil
	}

	return "", fmt.Errorf("unknown video format in stream")
}
