package recording

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"aiovms/internal/mediaprobe"
	"aiovms/internal/model"
	"aiovms/pkg/logger"
)

// CameraLookup resolves a MediaMTX path name (e.g. "cam-a1b2c3d4") to the full
// Camera record. Used by Scanner to map recording file directories back to
// the canonical camera UUID.
type CameraLookup interface {
	FindByMediaMTXPath(path string) (*model.Camera, error)
}

// Scanner ingests recorded fMP4 segments: probe metadata, then upsert into
// the database. It serves two roles:
//
//  1. Fallback reconciler — periodically walks the recording directory so
//     segments are ingested even when the segment-complete hook is lost.
//  2. Shared ingestion path — the hook handler calls IngestFile directly for
//     immediate (fast-path) ingestion of completed segments.
type Scanner struct {
	svc        Service
	camLookup  CameraLookup
	recordPath string
	interval   time.Duration
	stopCh     chan struct{}
	stopOnce   sync.Once
}

// NewScanner creates a recording file scanner. interval is the fallback scan
// period; <= 0 defaults to 30s.
func NewScanner(svc Service, recordPath string, camLookup CameraLookup, interval time.Duration) *Scanner {
	if interval <= 0 {
		interval = 30 * time.Second
	}
	return &Scanner{
		svc:        svc,
		camLookup:  camLookup,
		recordPath: recordPath,
		interval:   interval,
		stopCh:     make(chan struct{}),
	}
}

// Run starts the scan loop. Blocks until Stop() is called.
func (s *Scanner) Run() {
	logger.Infof("recording scanner started (interval=%s, path=%s)", s.interval, s.recordPath)
	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()

	// Initial scan
	s.scan()

	for {
		select {
		case <-ticker.C:
			s.scan()
		case <-s.stopCh:
			logger.Info("recording scanner stopped")
			return
		}
	}
}

// Stop signals the scanner to stop. Safe for repeated calls.
func (s *Scanner) Stop() {
	s.stopOnce.Do(func() { close(s.stopCh) })
}

func (s *Scanner) scan() {
	err := filepath.Walk(s.recordPath, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			logger.Errorf("scanner: walk error %s: %v", path, err)
			return nil
		}
		if info.IsDir() {
			return nil
		}

		// Only process MP4 files
		name := strings.ToLower(info.Name())
		if !strings.HasSuffix(name, ".mp4") {
			return nil
		}

		// Skip files still being written (modified within 30s)
		if time.Since(info.ModTime()) < 30*time.Second {
			return nil
		}

		// Fallback path: status is heuristic (unknown completion), so pass
		// knownComplete=false.
		if err := s.ingest(path, info, false); err != nil {
			logger.Debugf("scanner: %v", err)
		}
		return nil
	})

	if err != nil {
		logger.Errorf("scanner: walk failed: %v", err)
	}
}

// IngestFile probes a single segment file and upserts it into the database.
// Shared by the segment-complete hook (fast path, knownComplete=true) and the
// scanner fallback. Idempotent — repeated calls converge via the file_path
// upsert, so hook and scanner may both ingest the same file safely.
func (s *Scanner) IngestFile(path string, knownComplete bool) error {
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("stat segment: %w", err)
	}
	if info.IsDir() {
		return fmt.Errorf("segment path is a directory: %s", path)
	}
	if !strings.HasSuffix(strings.ToLower(info.Name()), ".mp4") {
		return fmt.Errorf("not an mp4 segment: %s", path)
	}
	return s.ingest(path, info, knownComplete)
}

// ingest resolves the owning camera, probes the file, and upserts the record.
func (s *Scanner) ingest(path string, info os.FileInfo, knownComplete bool) error {
	// Parent directory is the MediaMTX path name (e.g. "cam-a1b2c3d4").
	// Resolve it to the full camera UUID via DB lookup.
	mtxPath := filepath.Base(filepath.Dir(path))
	cam, err := s.camLookup.FindByMediaMTXPath(mtxPath)
	if err != nil {
		return fmt.Errorf("no camera found for path %q (file %s)", mtxPath, path)
	}

	rec, err := s.probeAndCreate(path, cam, mtxPath, info, knownComplete)
	if err != nil {
		return fmt.Errorf("probe %s: %w", path, err)
	}

	// Background context for upsert (not request-scoped)
	return s.svc.Upsert(context.Background(), rec)
}

func (s *Scanner) probeAndCreate(path string, cam *model.Camera, mtxPath string, info os.FileInfo, knownComplete bool) (*model.Recording, error) {
	mi, err := mediaprobe.ProbeMP4(path)
	if err != nil {
		return nil, err
	}

	// Parse start time from MediaMTX filename: fmp4_2026-07-28_14-30-00_1.mp4
	startTime := info.ModTime()
	if parts := parseMediaMTXFilename(info.Name()); parts != "" {
		if t, err := time.Parse("2006-01-02_15-04-05", parts); err == nil {
			startTime = t
		}
	}

	endTime := startTime.Add(time.Duration(mi.Duration * float64(time.Second)))
	// Hook callers know the segment is finalized; scanner callers rely on the
	// mtime heuristic. (A scanner pass shortly after the hook may briefly flip
	// status back to "recording" — the next pass settles it; retention only
	// deletes "complete" rows, so this window is harmless.)
	status := "complete"
	if !knownComplete && time.Since(info.ModTime()) < 2*time.Minute {
		status = "recording"
	}

	durationSec := int(mi.Duration)
	if durationSec <= 0 {
		durationSec = 1
	}

	return &model.Recording{
		ID:           uuid.NewString(),
		CameraID:     cam.ID,
		MediaMTXPath: mtxPath,
		Filename:     info.Name(),
		FilePath:     path,
		FileSize:     info.Size(),
		StartTime:    startTime,
		EndTime:      endTime,
		Duration:     durationSec,
		Codec:        mi.CodecName,
		Resolution:   formatResolution(mi.Width, mi.Height),
		Status:       status,
		RecordType:   "scheduled",
		LicenseID:    cam.LicenseID,
		CreatedAt:    time.Now(),
	}, nil
}

// parseMediaMTXFilename extracts the timestamp part from fmp4_YYYY-MM-DD_HH-MM-SS_N.mp4.
func parseMediaMTXFilename(name string) string {
	if !strings.HasPrefix(name, "fmp4_") {
		return ""
	}
	// Strip fmp4_ prefix and _N.mp4 suffix
	rest := strings.TrimPrefix(name, "fmp4_")
	// Find the last underscore before the part number
	lastUnderscore := strings.LastIndex(rest, "_")
	if lastUnderscore < 0 {
		return ""
	}
	return rest[:lastUnderscore]
}

func formatResolution(w, h int) string {
	if w == 0 || h == 0 {
		return "unknown"
	}
	return itoa(w) + "x" + itoa(h)
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [12]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}
