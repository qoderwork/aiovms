package mediaprobe

import (
	"os"
	"path/filepath"
	"testing"

	"aiovms/internal/merge"
	"github.com/abema/go-mp4"
)

// --- minimal MP4 fixture generation (pure-Go, no ffmpeg dependency) ---
// These helpers build a minimal but structurally valid fMP4 covering exactly the
// box tree mediaprobe.ParseSegment walks: ftyp, moov(mvhd, trak(mdia(mdhd,
// hdlr, stbl(stsd(avc1(avcC)), stts, stsz, stsc, stco)))), mdat.

type bitWriter struct {
	buf   []byte
	cur   byte
	nbits int
}

func (b *bitWriter) putBit(v int) {
	b.cur = (b.cur << 1) | byte(v&1)
	b.nbits++
	if b.nbits == 8 {
		b.buf = append(b.buf, b.cur)
		b.cur = 0
		b.nbits = 0
	}
}

func (b *bitWriter) putBits(v, n int) {
	for i := n - 1; i >= 0; i-- {
		b.putBit((v >> i) & 1)
	}
}

func (b *bitWriter) putUE(v int) {
	x := v + 1
	n := 0
	for t := x; t > 0; t >>= 1 {
		n++
	}
	for i := 0; i < n-1; i++ {
		b.putBit(0)
	}
	for i := n - 1; i >= 0; i-- {
		b.putBit((x >> i) & 1)
	}
}

func (b *bitWriter) flush() []byte {
	if b.nbits > 0 {
		b.cur <<= uint(8 - b.nbits)
		b.buf = append(b.buf, b.cur)
		b.nbits = 0
	}
	return b.buf
}

// baselineSPS encodes a baseline-profile (profile_idc=66) SPS for width x height
// with no cropping. The returned bytes include the NAL header byte (0x67) and are
// therefore directly usable as avcC.SequenceParameterSets[].NALUnit, which is how
// mediaprobe feeds them to ParseSPSResolution.
func baselineSPS(width, height int) []byte {
	mbW := width / 16
	mbH := height / 16
	bw := &bitWriter{}
	bw.putBits(66, 8) // profile_idc (baseline)
	bw.putBits(0, 8)  // constraint flags + reserved
	bw.putBits(31, 8) // level_idc
	bw.putUE(0)       // seq_parameter_set_id
	bw.putUE(0)       // log2_max_frame_num_minus4
	bw.putUE(0)       // pic_order_cnt_type
	bw.putUE(0)       // log2_max_pic_order_cnt_lsb_minus4
	bw.putUE(1)       // max_num_ref_frames
	bw.putBit(0)      // gaps_in_frame_num_value_allowed_flag
	bw.putUE(mbW - 1) // pic_width_in_mbs_minus1
	bw.putUE(mbH - 1) // pic_height_in_map_units_minus1
	bw.putBit(1)      // frame_mbs_only_flag
	bw.putBit(1)      // direct_8x8_inference_flag
	bw.putBit(0)      // frame_cropping_flag
	rbsp := bw.flush()
	return append([]byte{0x67}, rbsp...)
}

func writeMinimalMP4(t *testing.T, sps, pps []byte, width, height int, timescale, numSamples, sampleDelta uint32) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "sample.mp4")
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create fixture: %v", err)
	}
	defer f.Close()
	w := mp4.NewWriter(f)
	ctx := mp4.Context{}

	mustStart := func(typ mp4.BoxType) {
		if _, err := w.StartBox(&mp4.BoxInfo{Type: typ}); err != nil {
			t.Fatalf("start %s: %v", typ, err)
		}
	}
	mustEnd := func() {
		if _, err := w.EndBox(); err != nil {
			t.Fatalf("end box: %v", err)
		}
	}
	mustMarshal := func(box mp4.IImmutableBox) {
		if _, err := mp4.Marshal(w, box, ctx); err != nil {
			t.Fatalf("marshal %T: %v", box, err)
		}
	}

	// ftyp
	mustStart(mp4.BoxTypeFtyp())
	mustMarshal(&mp4.Ftyp{
		MajorBrand:       [4]byte{'i', 's', 'o', 'm'},
		MinorVersion:     0,
		CompatibleBrands: []mp4.CompatibleBrandElem{{CompatibleBrand: [4]byte{'i', 's', 'o', 'm'}}, {CompatibleBrand: [4]byte{'m', 'p', '4', '2'}}},
	})
	mustEnd()

	// moov
	mustStart(mp4.BoxTypeMoov())
	// mvhd
	mustStart(mp4.BoxTypeMvhd())
	mustMarshal(&mp4.Mvhd{Timescale: timescale, DurationV0: timescale, NextTrackID: 2})
	mustEnd()
	// trak
	mustStart(mp4.BoxTypeTrak())
	// mdia
	mustStart(mp4.BoxTypeMdia())
	// mdhd
	mustStart(mp4.BoxTypeMdhd())
	mustMarshal(&mp4.Mdhd{Timescale: timescale, DurationV0: timescale})
	mustEnd()
	// hdlr (video)
	mustStart(mp4.BoxTypeHdlr())
	mustMarshal(&mp4.Hdlr{HandlerType: [4]byte{'v', 'i', 'd', 'e'}, Name: "VideoHandler"})
	mustEnd()
	// stbl
	mustStart(mp4.BoxTypeStbl())
	// stsd
	mustStart(mp4.BoxTypeStsd())
	mustMarshal(&mp4.Stsd{EntryCount: 1})
	// avc1
	mustStart(mp4.BoxTypeAvc1())
	mustMarshal(&mp4.VisualSampleEntry{
		SampleEntry:     mp4.SampleEntry{AnyTypeBox: mp4.AnyTypeBox{Type: mp4.BoxTypeAvc1()}, DataReferenceIndex: 1},
		Width:           uint16(width),
		Height:          uint16(height),
		Horizresolution: 0x00480000,
		Vertresolution:  0x00480000,
		FrameCount:      1,
		Depth:           24,
	})
	// avcC
	mustStart(mp4.BoxTypeAvcC())
	mustMarshal(&mp4.AVCDecoderConfiguration{
		AnyTypeBox:                 mp4.AnyTypeBox{Type: mp4.BoxTypeAvcC()},
		ConfigurationVersion:       1,
		Profile:                    66,
		ProfileCompatibility:       0,
		Level:                      31,
		Reserved:                   63,
		LengthSizeMinusOne:         0,
		Reserved2:                  7,
		NumOfSequenceParameterSets: 1,
		SequenceParameterSets:      []mp4.AVCParameterSet{{Length: uint16(len(sps)), NALUnit: sps}},
		NumOfPictureParameterSets:  1,
		PictureParameterSets:       []mp4.AVCParameterSet{{Length: uint16(len(pps)), NALUnit: pps}},
	})
	mustEnd() // avcC
	mustEnd() // avc1
	mustEnd() // stsd
	// stts
	mustStart(mp4.BoxTypeStts())
	mustMarshal(&mp4.Stts{EntryCount: 1, Entries: []mp4.SttsEntry{{SampleCount: numSamples, SampleDelta: sampleDelta}}})
	mustEnd()
	// stsz
	mustStart(mp4.BoxTypeStsz())
	mustMarshal(&mp4.Stsz{SampleSize: 100, SampleCount: numSamples})
	mustEnd()
	// stsc
	mustStart(mp4.BoxTypeStsc())
	mustMarshal(&mp4.Stsc{EntryCount: 1, Entries: []mp4.StscEntry{{FirstChunk: 1, SamplesPerChunk: numSamples, SampleDescriptionIndex: 1}}})
	mustEnd()
	// stco
	mustStart(mp4.BoxTypeStco())
	mustMarshal(&mp4.Stco{EntryCount: 1, ChunkOffset: []uint32{8}})
	mustEnd()
	mustEnd() // stbl
	mustEnd() // mdia
	mustEnd() // trak
	mustEnd() // moov

	// mdat (dummy payload; offsets are unused by the metadata parser)
	mustStart(mp4.BoxTypeMdat())
	if _, err := w.Write(make([]byte, numSamples*100)); err != nil {
		t.Fatalf("write mdat: %v", err)
	}
	mustEnd()

	return path
}

func TestProbeMP4_H264(t *testing.T) {
	sps := baselineSPS(1280, 720)
	pps := []byte{0x68, 0xce, 0x3c, 0x80}
	path := writeMinimalMP4(t, sps, pps, 1280, 720, 1000, 10, 100)

	info, err := ProbeMP4(path)
	if err != nil {
		t.Fatalf("ProbeMP4: %v", err)
	}
	if info.CodecName != "h264" {
		t.Errorf("CodecName = %q, want h264", info.CodecName)
	}
	if info.Codec != "h264" {
		t.Errorf("Codec = %q, want h264", info.Codec)
	}
	if info.Width != 1280 || info.Height != 720 {
		t.Errorf("resolution = %dx%d, want 1280x720", info.Width, info.Height)
	}
	if info.FrameCount != 10 {
		t.Errorf("FrameCount = %d, want 10", info.FrameCount)
	}
	if got := info.Duration; got != 1.0 {
		t.Errorf("Duration = %v, want 1.0", got)
	}
}

// TestProbeMP4_ResolutionFromSPS proves the reported resolution comes from the SPS
// bitstream, not from the avc1 box Width/Height fields (which we deliberately lie
// about here). Regression coverage for merge.ParseSPSResolution wiring.
func TestProbeMP4_ResolutionFromSPS(t *testing.T) {
	sps := baselineSPS(640, 480)
	pps := []byte{0x68, 0xce, 0x3c, 0x80}
	path := writeMinimalMP4(t, sps, pps, 999, 999, 1000, 5, 200)

	info, err := ProbeMP4(path)
	if err != nil {
		t.Fatalf("ProbeMP4: %v", err)
	}
	if info.Width != 640 || info.Height != 480 {
		t.Errorf("resolution = %dx%d, want 640x480 (derived from SPS)", info.Width, info.Height)
	}
	if info.FrameCount != 5 {
		t.Errorf("FrameCount = %d, want 5", info.FrameCount)
	}
	if got := info.Duration; got != 1.0 {
		t.Errorf("Duration = %v, want 1.0 (5*200/1000)", got)
	}
}

func TestProbeDuration(t *testing.T) {
	sps := baselineSPS(1280, 720)
	pps := []byte{0x68, 0xce, 0x3c, 0x80}
	path := writeMinimalMP4(t, sps, pps, 1280, 720, 1000, 10, 100)
	d, err := ProbeDuration(path)
	if err != nil {
		t.Fatalf("ProbeDuration: %v", err)
	}
	if d != 1.0 {
		t.Errorf("ProbeDuration = %v, want 1.0", d)
	}
}

func TestProbeMP4_FileNotFound(t *testing.T) {
	if _, err := ProbeMP4("/nonexistent/path/to/file.mp4"); err == nil {
		t.Fatal("expected error for missing file, got nil")
	}
}

func TestParseSPSResolution(t *testing.T) {
	cases := []struct {
		name   string
		width  int
		height int
	}{
		{"1280x720", 1280, 720},
		{"640x480", 640, 480},
		{"1920x1088", 1920, 1088},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			sps := baselineSPS(c.width, c.height)
			w, h, err := merge.ParseSPSResolution(sps)
			if err != nil {
				t.Fatalf("ParseSPSResolution: %v", err)
			}
			if w != c.width || h != c.height {
				t.Errorf("got %dx%d, want %dx%d", w, h, c.width, c.height)
			}
		})
	}
}

func TestParseSPSResolution_TooShort(t *testing.T) {
	if _, _, err := merge.ParseSPSResolution([]byte{0x67, 0x42}); err == nil {
		t.Fatal("expected error for too-short SPS, got nil")
	}
}
