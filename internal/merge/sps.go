// Package merge — port of MiBeeNvr internal/merge/sps.go
// H.264/H.265 SPS resolution parsing for mediaprobe.

package merge

import "fmt"

type bitReader struct {
	data   []byte
	offset int
}

func (r *bitReader) readBit() (int, error) {
	if r.offset >= len(r.data)*8 {
		return 0, fmt.Errorf("merge: bitReader overflow at offset %d (data length %d bits)", r.offset, len(r.data)*8)
	}
	byteIdx := r.offset / 8
	bitIdx := 7 - (r.offset % 8)
	r.offset++
	return int((r.data[byteIdx] >> bitIdx) & 1), nil
}

func (r *bitReader) readBits(n int) (int, error) {
	var val int
	for range n {
		bit, err := r.readBit()
		if err != nil {
			return 0, err
		}
		val = (val << 1) | bit
	}
	return val, nil
}

func (r *bitReader) readUE() (int, error) {
	leadingZeros := 0
	for {
		bit, err := r.readBit()
		if err != nil {
			return 0, err
		}
		if bit == 1 {
			break
		}
		leadingZeros++
		if leadingZeros > 32 {
			return 0, fmt.Errorf("merge: sps readUE leadingZeros overflow (%d)", leadingZeros)
		}
	}
	if leadingZeros == 0 {
		return 0, nil
	}
	bits, err := r.readBits(leadingZeros)
	if err != nil {
		return 0, err
	}
	return (1 << leadingZeros) - 1 + bits, nil
}

func (r *bitReader) readSE() (int, error) {
	val, err := r.readUE()
	if err != nil {
		return 0, err
	}
	if val%2 == 0 {
		return -(val / 2), nil
	}
	return (val + 1) / 2, nil
}

func removeEmulationPrevention(data []byte) []byte {
	var result []byte
	i := 0
	for i < len(data) {
		if i+2 < len(data) && data[i] == 0 && data[i+1] == 0 && data[i+2] == 3 {
			result = append(result, 0, 0)
			i += 3
		} else {
			result = append(result, data[i])
			i++
		}
	}
	return result
}

func parseSPSResolution(sps []byte) (width, height int, err error) {
	if len(sps) < 8 {
		return 0, 0, fmt.Errorf("merge: sps too short (%d bytes)", len(sps))
	}
	rbsp := removeEmulationPrevention(sps[1:])
	if len(rbsp) < 4 {
		return 0, 0, fmt.Errorf("merge: sps rbsp too short (%d bytes)", len(rbsp))
	}

	r := &bitReader{data: rbsp}
	var profileIDC int
	if profileIDC, err = r.readBits(8); err != nil {
		return
	}
	if _, err = r.readBits(8); err != nil {
		return
	}
	if _, err = r.readBits(8); err != nil {
		return
	}
	if _, err = r.readUE(); err != nil {
		return
	}

	highProfile := profileIDC == 100 || profileIDC == 110 || profileIDC == 122 ||
		profileIDC == 244 || profileIDC == 44 || profileIDC == 83 ||
		profileIDC == 86 || profileIDC == 118 || profileIDC == 128 ||
		profileIDC == 138 || profileIDC == 139 || profileIDC == 134

	chromaFormatIDC := 1
	if highProfile {
		if chromaFormatIDC, err = r.readUE(); err != nil {
			return
		}
		if chromaFormatIDC == 3 {
			if _, err = r.readBit(); err != nil {
				return
			}
		}
		if _, err = r.readUE(); err != nil {
			return
		}
		if _, err = r.readUE(); err != nil {
			return
		}
		if _, err = r.readBit(); err != nil {
			return
		}
		var scalingPresent int
		if scalingPresent, err = r.readBit(); err != nil {
			return
		}
		if scalingPresent == 1 {
			count := 8
			if chromaFormatIDC == 3 {
				count = 12
			}
			for i := range count {
				var present int
				if present, err = r.readBit(); err != nil {
					return
				}
				if present == 1 {
					size := 16
					if i >= 6 {
						size = 64
					}
					lastScale := 8
					for range size {
						var delta int
						if delta, err = r.readSE(); err != nil {
							return
						}
						nextScale := (lastScale + delta + 256) % 256
						if nextScale == 0 {
							nextScale = 256
						}
						lastScale = nextScale
					}
				}
			}
		}
	}

	if _, err = r.readUE(); err != nil {
		return
	}
	var picOrderCntType int
	if picOrderCntType, err = r.readUE(); err != nil {
		return
	}
	if picOrderCntType == 0 {
		if _, err = r.readUE(); err != nil {
			return
		}
	} else if picOrderCntType == 1 {
		if _, err = r.readBit(); err != nil {
			return
		}
		if _, err = r.readSE(); err != nil {
			return
		}
		if _, err = r.readSE(); err != nil {
			return
		}
		var numRefFrames int
		if numRefFrames, err = r.readUE(); err != nil {
			return
		}
		for range numRefFrames {
			if _, err = r.readSE(); err != nil {
				return
			}
		}
	}

	if _, err = r.readUE(); err != nil {
		return
	}
	if _, err = r.readBit(); err != nil {
		return
	}
	var picWidthInMbs, picHeightInMapUnits, frameMbsOnly int
	if picWidthInMbs, err = r.readUE(); err != nil {
		return
	}
	picWidthInMbs++
	if picHeightInMapUnits, err = r.readUE(); err != nil {
		return
	}
	picHeightInMapUnits++
	if frameMbsOnly, err = r.readBit(); err != nil {
		return
	}
	if frameMbsOnly == 0 {
		if _, err = r.readBit(); err != nil {
			return
		}
	}
	if _, err = r.readBit(); err != nil {
		return
	}
	var frameCropping int
	if frameCropping, err = r.readBit(); err != nil {
		return
	}

	var cropLeft, cropRight, cropTop, cropBottom int
	if frameCropping == 1 {
		var cl, cr, ct, cb int
		if cl, err = r.readUE(); err != nil {
			return
		}
		if cr, err = r.readUE(); err != nil {
			return
		}
		if ct, err = r.readUE(); err != nil {
			return
		}
		if cb, err = r.readUE(); err != nil {
			return
		}
		var cropUnitX, cropUnitY int
		if chromaFormatIDC == 0 {
			cropUnitX, cropUnitY = 1, 1
		} else if chromaFormatIDC == 1 {
			cropUnitX, cropUnitY = 2, 2
		} else if chromaFormatIDC == 2 {
			cropUnitX, cropUnitY = 2, 1
		} else {
			cropUnitX, cropUnitY = 1, 1
		}
		cropLeft = cropUnitX * cl
		cropRight = cropUnitX * cr
		cropTop = cropUnitY * ct
		cropBottom = cropUnitY * cb
	}

	width = picWidthInMbs*16 - cropLeft - cropRight
	height = (2-frameMbsOnly)*picHeightInMapUnits*16 - cropTop - cropBottom
	if width <= 0 || height <= 0 || width > 8192 || height > 8192 {
		return 0, 0, fmt.Errorf("merge: invalid sps resolution: width=%d, height=%d", width, height)
	}
	return
}

func parseHEVCSPSResolution(sps []byte) (width, height int, err error) {
	if len(sps) < 8 {
		return 0, 0, fmt.Errorf("merge: hevc sps too short (%d bytes)", len(sps))
	}
	rbsp := removeEmulationPrevention(sps[2:])
	if len(rbsp) < 13 {
		return 0, 0, fmt.Errorf("merge: hevc sps rbsp too short (%d bytes)", len(rbsp))
	}
	r := &bitReader{data: rbsp}

	if _, err = r.readBits(4); err != nil {
		return
	}
	var maxSubLayersMinus1 int
	if maxSubLayersMinus1, err = r.readBits(3); err != nil {
		return
	}
	if _, err = r.readBit(); err != nil {
		return
	}
	if _, err = r.readBits(8); err != nil {
		return
	}
	if _, err = r.readBits(32); err != nil {
		return
	}
	if _, err = r.readBits(48); err != nil {
		return
	}
	if _, err = r.readBits(8); err != nil {
		return
	}
	for range maxSubLayersMinus1 {
		if _, err = r.readBits(2); err != nil {
			return
		}
	}
	if maxSubLayersMinus1 > 0 {
		for range maxSubLayersMinus1 {
			if _, err = r.readBit(); err != nil {
				return
			}
		}
	}
	if _, err = r.readUE(); err != nil {
		return
	}
	var chromaFormatIDC int
	if chromaFormatIDC, err = r.readUE(); err != nil {
		return
	}
	if chromaFormatIDC == 3 {
		if _, err = r.readBit(); err != nil {
			return
		}
	}
	if width, err = r.readUE(); err != nil {
		return
	}
	if height, err = r.readUE(); err != nil {
		return
	}
	if width <= 0 || height <= 0 || width > 8192 || height > 8192 {
		return 0, 0, fmt.Errorf("merge: invalid hevc sps resolution: width=%d, height=%d", width, height)
	}
	return
}

func ParseSPSResolution(sps []byte) (width, height int, err error) {
	return parseSPSResolution(sps)
}

func ParseHEVCSPSResolution(sps []byte) (width, height int, err error) {
	return parseHEVCSPSResolution(sps)
}
