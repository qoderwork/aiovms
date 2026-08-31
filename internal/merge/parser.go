// Package merge — port of MiBeeNvr internal/merge/parser.go
// Pure-Go MP4 box structure parsing for mediaprobe.
// Uses abema/go-mp4 to walk moov/stbl boxes, extracts codec/SPS/PPS/sample tables.

package merge

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/abema/go-mp4"
)

// SegmentInfo contains parsed metadata and sample table from an MP4 segment.
type SegmentInfo struct {
	Codec         string
	SPS           []byte
	PPS           []byte
	VPS           []byte
	Timescale     uint32
	SampleCount   int
	TotalDuration time.Duration
	Samples       []SampleEntry
	FilePath      string
}

// SampleEntry describes a single media sample within mdat.
type SampleEntry struct {
	Offset     int64
	Size       uint32
	Duration   uint32
	IsKeyFrame bool
}

type trackAccum struct {
	handlerType [4]byte
	timescale   uint32
	codec       string
	sps, pps    []byte
	vps         []byte
	trackID     uint32
	sttsEntries []mp4.SttsEntry
	stszSizes   []uint32
	stszUniform uint32
	sampleCount uint32
	stscEntries []mp4.StscEntry
	stcoOffsets []uint32
	co64Offsets []uint64
}

// fragAccum holds accumulated sample count and duration for one track across
// all moof/traf/trun boxes (fragmented MP4 / fMP4). When the moov sample
// table is empty, this is the source of truth for SampleCount/Duration.
type fragAccum struct {
	samples  uint32
	duration uint64 // in timescale units of the track
}

// ParseSegment reads an MP4 file and extracts codec config, sample tables,
// and duration from the moov box structure. Never loads mdat into memory.
func ParseSegment(filePath string) (*SegmentInfo, error) {
	f, err := os.Open(filePath)
	if err != nil {
		return nil, fmt.Errorf("parse MP4 %s: open: %w", filePath, err)
	}
	defer f.Close()

	fileInfo, err := f.Stat()
	if err != nil {
		return nil, fmt.Errorf("parse MP4 %s: stat: %w", filePath, err)
	}
	fileSize := fileInfo.Size()

	// Warn if file is still being written (common during active recording).
	if time.Since(fileInfo.ModTime()) < 2*time.Second {
		// Debug-level — normal for recording-in-progress files
	}

	var mdatOffset, mdatSize int64
	var tracks []*trackAccum
	var current *trackAccum

	// fMP4 accumulation: when the moov sample table is empty (fragmented MP4),
	// samples live in moof/traf/trun boxes. We accumulate per-track sample
	// count and duration across all trun boxes, then synthesize a minimal
	// SegmentInfo for the video track.
	var (
		fragByTrack = map[uint32]*fragAccum{}
		lastTfhdID  uint32
		lastTfhdDur uint32
	)

	_, err = mp4.ReadBoxStructure(f, func(h *mp4.ReadHandle) (interface{}, error) {
		boxType := h.BoxInfo.Type.String()

		if boxType == "mdat" {
			if len(h.Path) == 1 {
				mdatOffset = int64(h.BoxInfo.Offset)
				mdatSize = int64(h.BoxInfo.Size)
			}
			return nil, nil
		}

		if boxType == "trak" {
			current = &trackAccum{}
			tracks = append(tracks, current)
			return h.Expand()
		}

		if h.BoxInfo.Size > uint64(fileSize) {
			return nil, fmt.Errorf("box %q claims size %d but file is only %d bytes", boxType, h.BoxInfo.Size, fileSize)
		}

		if current != nil && (boxType == "ulaw" || boxType == "alaw") {
			return nil, nil
		}
		if current != nil && boxType == "Opus" {
			return nil, nil
		}
		if !h.BoxInfo.IsSupportedType() {
			return nil, nil
		}

		box, _, err := h.ReadPayload()
		if err != nil {
			return nil, err
		}

		// fMP4 fragment boxes (moof/traf/tfhd/trun) are outside any trak —
		// accumulate per-track sample count + duration for later synthesis.
		switch b := box.(type) {
		case *mp4.Tfhd:
			lastTfhdID = b.TrackID
			lastTfhdDur = b.DefaultSampleDuration
		case *mp4.Trun:
			fa := fragByTrack[lastTfhdID]
			if fa == nil {
				fa = &fragAccum{}
				fragByTrack[lastTfhdID] = fa
			}
			fa.samples += b.SampleCount
			var sumDur uint64
			for _, e := range b.Entries {
				sumDur += uint64(e.SampleDuration)
			}
			if sumDur == 0 && lastTfhdDur != 0 {
				sumDur = uint64(b.SampleCount) * uint64(lastTfhdDur)
			}
			fa.duration += sumDur
		}

		if current == nil {
			return h.Expand()
		}

		switch b := box.(type) {
		case *mp4.Tkhd:
			current.trackID = b.TrackID
		case *mp4.Hdlr:
			current.handlerType = b.HandlerType
		case *mp4.Mdhd:
			current.timescale = b.Timescale
		case *mp4.Stts:
			current.sttsEntries = b.Entries
		case *mp4.Stsz:
			current.sampleCount = b.SampleCount
			if b.SampleSize != 0 {
				current.stszUniform = b.SampleSize
			} else {
				current.stszSizes = b.EntrySize
			}
		case *mp4.Stsc:
			current.stscEntries = b.Entries
		case *mp4.Stco:
			current.stcoOffsets = b.ChunkOffset
		case *mp4.Co64:
			current.co64Offsets = b.ChunkOffset
		case *mp4.AVCDecoderConfiguration:
			current.codec = "h264"
			if len(b.SequenceParameterSets) > 0 {
				current.sps = make([]byte, len(b.SequenceParameterSets[0].NALUnit))
				copy(current.sps, b.SequenceParameterSets[0].NALUnit)
			}
			if len(b.PictureParameterSets) > 0 {
				current.pps = make([]byte, len(b.PictureParameterSets[0].NALUnit))
				copy(current.pps, b.PictureParameterSets[0].NALUnit)
			}
		case *mp4.HvcC:
			current.codec = "h265"
			for _, arr := range b.NaluArrays {
				if len(arr.Nalus) == 0 {
					continue
				}
				nal := arr.Nalus[0].NALUnit
				switch arr.NaluType {
				case 32:
					current.vps = make([]byte, len(nal))
					copy(current.vps, nal)
				case 33:
					current.sps = make([]byte, len(nal))
					copy(current.sps, nal)
				case 34:
					current.pps = make([]byte, len(nal))
					copy(current.pps, nal)
				}
			}
		}

		return h.Expand()
	})
	if err != nil {
		return nil, fmt.Errorf("parse MP4 %s: %w", filePath, err)
	}

	// Find video track
	var videoTrack *trackAccum
	for _, tr := range tracks {
		if !bytes.Equal(tr.handlerType[:], []byte("soun")) {
			videoTrack = tr
			break
		}
	}
	if videoTrack == nil {
		return nil, fmt.Errorf("parse MP4 %s: no video track found", filePath)
	}
	if videoTrack.timescale == 0 {
		return nil, fmt.Errorf("parse MP4 %s: no mdhd box found", filePath)
	}
	if videoTrack.sampleCount == 0 {
		// fMP4: samples live in moof/trun, not in moov sample table.
		// Synthesize a minimal SegmentInfo from the accumulated trun data.
		fa := fragByTrack[videoTrack.trackID]
		if fa == nil || fa.samples == 0 {
			return nil, fmt.Errorf("parse MP4 %s: no samples in segment", filePath)
		}
		totalDur := time.Duration(fa.duration) * time.Second / time.Duration(videoTrack.timescale)
		return &SegmentInfo{
			Codec:         videoTrack.codec,
			SPS:           videoTrack.sps,
			PPS:           videoTrack.pps,
			VPS:           videoTrack.vps,
			Timescale:     videoTrack.timescale,
			SampleCount:   int(fa.samples),
			TotalDuration: totalDur,
			FilePath:      filePath,
		}, nil
	}

	videoSamples, err := buildTrackSamples(videoTrack)
	if err != nil {
		return nil, fmt.Errorf("parse MP4 %s: build samples: %w", filePath, err)
	}

	_ = mdatOffset
	_ = mdatSize

	totalDur := time.Duration(0)
	for _, e := range videoTrack.sttsEntries {
		totalDur += time.Duration(e.SampleCount) * time.Duration(e.SampleDelta) * time.Second / time.Duration(videoTrack.timescale)
	}

	return &SegmentInfo{
		Codec:         videoTrack.codec,
		SPS:           videoTrack.sps,
		PPS:           videoTrack.pps,
		VPS:           videoTrack.vps,
		Timescale:     videoTrack.timescale,
		SampleCount:   len(videoSamples),
		TotalDuration: totalDur,
		Samples:       videoSamples,
		FilePath:      filePath,
	}, nil
}

func buildTrackSamples(tr *trackAccum) ([]SampleEntry, error) {
	stszSizes := tr.stszSizes
	if tr.stszUniform != 0 {
		stszSizes = make([]uint32, tr.sampleCount)
		for i := range stszSizes {
			stszSizes[i] = tr.stszUniform
		}
	}

	chunkOffsets := make([]int64, 0, len(tr.stcoOffsets)+len(tr.co64Offsets))
	for _, off := range tr.stcoOffsets {
		chunkOffsets = append(chunkOffsets, int64(off))
	}
	if len(tr.co64Offsets) > 0 {
		chunkOffsets = chunkOffsets[:0]
		for _, off := range tr.co64Offsets {
			chunkOffsets = append(chunkOffsets, int64(off))
		}
	}

	return buildSampleEntries(stszSizes, tr.stscEntries, chunkOffsets, tr.sttsEntries)
}

func buildSampleEntries(sizes []uint32, stsc []mp4.StscEntry, chunkOffsets []int64, stts []mp4.SttsEntry) ([]SampleEntry, error) {
	n := len(sizes)
	if n == 0 {
		return nil, nil
	}
	if len(stsc) == 0 {
		return nil, fmt.Errorf("no stsc entries")
	}
	if len(chunkOffsets) == 0 {
		return nil, fmt.Errorf("no chunk offsets")
	}

	samples := make([]SampleEntry, n)

	if len(stts) > 0 {
		durIdx := 0
		durRemaining := stts[0].SampleCount
		for i := range n {
			for durRemaining == 0 && durIdx+1 < len(stts) {
				durIdx++
				durRemaining = stts[durIdx].SampleCount
			}
			if durRemaining > 0 {
				samples[i].Duration = stts[durIdx].SampleDelta
				durRemaining--
			}
		}
	}

	sampleIdx := 0
	for i, entry := range stsc {
		firstChunk := int(entry.FirstChunk)
		samplesPerChunk := int(entry.SamplesPerChunk)

		var lastChunk int
		if i+1 < len(stsc) {
			lastChunk = int(stsc[i+1].FirstChunk) - 1
		} else {
			lastChunk = len(chunkOffsets)
		}

		for chunkNum := firstChunk; chunkNum <= lastChunk; chunkNum++ {
			if chunkNum < 1 || chunkNum-1 >= len(chunkOffsets) || sampleIdx >= n {
				break
			}
			chunkOff := chunkOffsets[chunkNum-1]
			offsetInChunk := int64(0)
			for s := 0; s < samplesPerChunk && sampleIdx < n; s++ {
				samples[sampleIdx].Offset = chunkOff + offsetInChunk
				samples[sampleIdx].Size = sizes[sampleIdx]
				offsetInChunk += int64(sizes[sampleIdx])
				sampleIdx++
			}
		}
	}

	if sampleIdx != n {
		return nil, fmt.Errorf("sample count mismatch: got %d from stsc, expected %d from stsz", sampleIdx, n)
	}

	return samples, nil
}

// ParseSegmentDurationOnly reads only the duration (in seconds) from an MP4 file.
// Faster than ParseSegment — no sample table building.
func ParseSegmentDurationOnly(filePath string) (float64, error) {
	f, err := os.Open(filePath)
	if err != nil {
		return 0, fmt.Errorf("open: %w", err)
	}
	defer f.Close()

	type durTrack struct {
		timescale uint32
		stts      []mp4.SttsEntry
		isVideo   bool
		hasTime   bool
	}
	var tracks []*durTrack
	var current *durTrack

	_, err = mp4.ReadBoxStructure(f, func(h *mp4.ReadHandle) (interface{}, error) {
		boxType := h.BoxInfo.Type.String()
		if boxType == "mdat" {
			return nil, nil
		}
		if boxType == "trak" {
			current = &durTrack{}
			tracks = append(tracks, current)
			return h.Expand()
		}
		if current == nil {
			return h.Expand()
		}
		switch boxType {
		case "mdhd", "hdlr", "stts":
		case "mdia", "minf", "stbl", "dinf":
			return h.Expand()
		default:
			return nil, nil
		}
		if !h.BoxInfo.IsSupportedType() {
			return nil, nil
		}
		box, _, err := h.ReadPayload()
		if err != nil {
			return nil, err
		}
		switch b := box.(type) {
		case *mp4.Mdhd:
			current.timescale = b.Timescale
			current.hasTime = b.Timescale > 0
		case *mp4.Stts:
			current.stts = b.Entries
		case *mp4.Hdlr:
			current.isVideo = !bytes.Equal(b.HandlerType[:], []byte("soun"))
		}
		return nil, nil
	})
	if err != nil {
		return 0, fmt.Errorf("parse: %w", err)
	}

	var video *durTrack
	for _, tr := range tracks {
		if tr.isVideo {
			video = tr
			break
		}
	}
	if video == nil && len(tracks) > 0 {
		video = tracks[0]
	}
	if video == nil || !video.hasTime || video.timescale == 0 {
		return 0, fmt.Errorf("no video track with timescale found")
	}

	var totalUnits uint64
	for _, e := range video.stts {
		totalUnits += uint64(e.SampleCount) * uint64(e.SampleDelta)
	}
	return float64(totalUnits) / float64(video.timescale), nil
}

// Suppress unused import warnings.
var _ = errors.ErrUnsupported
var _ = io.EOF
