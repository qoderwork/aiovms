// Package nalutil provides shared NALU (Network Abstraction Layer Unit) detection
// utilities for H.264 and H.265 video streams.
//
// This is the single source of truth for IDR/keyframe detection across the codebase.
//
// Adapted from MiBeeNvr (MIT License, Copyright (c) Mi&Bee Studio).
package nalutil

// IsKeyframeNALU checks if a single NALU is an IDR frame.
//
// H.264: NAL type 5 = IDR (extract via nalu[0] & 0x1F)
// H.265: NAL type 19 (IDR_W_RADL) and 20 (IDR_N_LP) = IDR (extract via (nalu[0] >> 1) & 0x3F)
func IsKeyframeNALU(nalu []byte, isH265 bool) bool {
	if len(nalu) == 0 {
		return false
	}
	if isH265 {
		naluType := (nalu[0] >> 1) & 0x3F
		return naluType == 19 || naluType == 20
	}
	naluType := nalu[0] & 0x1F
	return naluType == 5
}

// IsIDR checks if an access unit (a slice of NALUs, e.g., [SPS, PPS, IDR])
// contains at least one IDR NALU.
func IsIDR(au [][]byte, isH265 bool) bool {
	for _, nalu := range au {
		if IsKeyframeNALU(nalu, isH265) {
			return true
		}
	}
	return false
}

// ExtractParamSetsH264 scans an H.264 access unit and returns the most recently
// observed SPS (NAL type 7) and PPS (NAL type 8), without the start-code prefix.
// Returns nil for either if not present.
func ExtractParamSetsH264(au [][]byte) (sps, pps []byte) {
	for _, nalu := range au {
		if len(nalu) == 0 {
			continue
		}
		switch nalu[0] & 0x1F {
		case 7: // SPS
			sps = nalu
		case 8: // PPS
			pps = nalu
		}
	}
	return sps, pps
}

// ExtractParamSetsH265 scans an H.265 access unit and returns the most recently
// observed VPS (NAL type 32), SPS (NAL type 33), and PPS (NAL type 34), without
// the start-code prefix. Returns nil for any that are not present.
func ExtractParamSetsH265(au [][]byte) (vps, sps, pps []byte) {
	for _, nalu := range au {
		if len(nalu) == 0 {
			continue
		}
		switch (nalu[0] >> 1) & 0x3F {
		case 32: // VPS_NUT
			vps = nalu
		case 33: // SPS_NUT
			sps = nalu
		case 34: // PPS_NUT
			pps = nalu
		}
	}
	return vps, sps, pps
}

// HasCompleteParamSets reports whether an access unit contains the full set of
// parameter-set NALUs needed to (re)configure a decoder for that codec:
//   - H.264: SPS (type 7) AND PPS (type 8)
//   - H.265: VPS (type 32) AND SPS (type 33) AND PPS (type 34)
func HasCompleteParamSets(au [][]byte, isH265 bool) bool {
	if isH265 {
		vps, sps, pps := ExtractParamSetsH265(au)
		return vps != nil && sps != nil && pps != nil
	}
	sps, pps := ExtractParamSetsH264(au)
	return sps != nil && pps != nil
}

// EqualParamSets reports whether two SPS/PPS NAL units are byte-identical.
// nil comparisons are treated as equal-to-nil only (nil != non-nil).
func EqualParamSets(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
