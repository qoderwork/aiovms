// Package mediaprobe provides pure-Go MP4 metadata probing — ffprobe replacement.
// Ported from MiBeeNvr internal/mediaprobe (MIT license).
//
// It reads MP4 box metadata (moov/mdhd/stsz/avcC/hvcC) via abema/go-mp4 and
// parses SPS for resolution — never decoding pixel data. This makes it
// dramatically faster than shelling out to ffprobe and removes the ffmpeg
// binary dependency from the VMS Docker image (~80MB → ~20MB).
package mediaprobe

import (
	"fmt"
	"time"

	"aiovms/internal/merge"
)

// MediaInfo holds metadata extracted from a media file.
type MediaInfo struct {
	CodecName  string  // ffprobe-compatible: "h264", "hevc"
	Duration   float64 // seconds
	Width      int
	Height     int
	FrameCount int
	Codec      string // internal: "h264" or "h265"
	FileSize   int64
}

// ProbeMP4 reads an MP4 file and extracts codec, duration, resolution, and
// frame count using only box-structure parsing.
func ProbeMP4(filePath string) (*MediaInfo, error) {
	seg, err := merge.ParseSegment(filePath)
	if err != nil {
		return nil, fmt.Errorf("mediaprobe: %w", err)
	}

	info := &MediaInfo{
		Duration:   seg.TotalDuration.Seconds(),
		FrameCount: seg.SampleCount,
		Codec:      seg.Codec,
	}

	switch seg.Codec {
	case "h264":
		info.CodecName = "h264"
	case "h265":
		info.CodecName = "hevc"
	default:
		info.CodecName = seg.Codec
	}

	if w, h, err := merge.ParseSPSResolution(seg.SPS); err == nil {
		info.Width = w
		info.Height = h
	}

	return info, nil
}

// ProbeDuration reads only the duration (in seconds) from an MP4 file.
func ProbeDuration(filePath string) (float64, error) {
	return merge.ParseSegmentDurationOnly(filePath)
}

// FormatDuration mirrors time.Duration formatting for logging.
func (m *MediaInfo) FormatDuration() string {
	return time.Duration(m.Duration * float64(time.Second)).Round(time.Millisecond).String()
}
