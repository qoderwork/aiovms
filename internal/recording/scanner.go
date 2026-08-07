package recording

import (
	"context"
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

// Scanner periodically scans the recording directory for new/updated fMP4 files,
// extracts metadata via mediaprobe, and upserts into the database.
type Scanner struct {
	svc        Service
	camLookup  CameraLookup
	recordPath string
	interval   time.Duration
	stopCh     chan struct{}
	stopOnce   sync.Once
}

// NewScanner creates a recording file scanner.
func NewScanner(svc Service, recordPath string, camLookup CameraLookup) *Scanner {
	return &Scanner{
		svc:        svc,
		camLookup:  camLookup,
		recordPath: recordPath,
		interval:   30 * time.Second,
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

		// Parent directory is the MediaMTX path name (e.g. "cam-a1b2c3d4").
		// Resolve it to the full camera UUID via DB lookup.
		mtxPath := filepath.Base(filepath.Dir(path))
		cam, err := s.camLookup.FindByMediaMTXPath(mtxPath)
		if err != nil {
			logger.Warnf("scanner: no camera found for path %q (file %s), skipping", mtxPath, path)
			return nil
		}

		rec, err := s.probeAndCreate(path, cam, mtxPath, info)
		if err != nil {
			logger.Debugf("scanner: probe %s: %v", path, err)
			return nil
		}

		if rec != nil {
			// Background context for upsert (not request-scoped)
			_ = s.svc.Upsert(context.Background(), rec)
		}
		return nil
	})

	if err != nil {
		logger.Errorf("scanner: walk failed: %v", err)
	}
}

func (s *Scanner) probeAndCreate(path string, cam *model.Camera, mtxPath string, info os.FileInfo) (*model.Recording, error) {
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
	status := "complete"
	if time.Since(info.ModTime()) < 2*time.Minute {
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
