package recording

import (
	"os"
	"sync"
	"time"

	"github.com/shirou/gopsutil/v4/disk"

	"aiovms/pkg/logger"
)

// Retention manages recording file lifecycle — deletes old recordings
// based on age policy (retentionDays) and disk space watermark.
type Retention struct {
	repo          Repository
	recordPath    string
	retentionDays int
	diskWatermark int
	interval      time.Duration
	stopCh        chan struct{}
	stopOnce      sync.Once
}

func NewRetention(repo Repository, recordPath string, retentionDays, diskWatermark int) *Retention {
	return &Retention{
		repo:          repo,
		recordPath:    recordPath,
		retentionDays: retentionDays,
		diskWatermark: diskWatermark,
		interval:      10 * time.Minute,
		stopCh:        make(chan struct{}),
	}
}

func (r *Retention) Run() {
	logger.Infof("recording retention started (days=%d, watermark=%d%%)", r.retentionDays, r.diskWatermark)
	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()
	r.cleanup()
	for {
		select {
		case <-ticker.C:
			r.cleanup()
		case <-r.stopCh:
			logger.Info("recording retention stopped")
			return
		}
	}
}

func (r *Retention) Stop() { r.stopOnce.Do(func() { close(r.stopCh) }) }

func (r *Retention) cleanup() {
	r.cleanupByAge()
	r.cleanupByDiskUsage()
}

func (r *Retention) cleanupByAge() {
	cutoff := time.Now().Add(-time.Duration(r.retentionDays) * 24 * time.Hour)
	recordings, err := r.repo.FindOlderThan(cutoff)
	if err != nil {
		logger.Errorf("retention: query old recordings: %v", err)
		return
	}
	for _, rec := range recordings {
		os.Remove(rec.FilePath)
		r.repo.Delete(&rec)
	}
	if len(recordings) > 0 {
		logger.Infof("retention: deleted %d recordings older than %v", len(recordings), cutoff.Format("2006-01-02"))
	}
}

func (r *Retention) cleanupByDiskUsage() {
	usage := r.diskUsagePercent()
	if usage < float64(r.diskWatermark) {
		return
	}
	logger.Warnf("retention: disk usage %.1f%% > %d%%, cleaning oldest recordings", usage, r.diskWatermark)

	allRecs, err := r.repo.FindAllSortedByTime()
	if err != nil {
		logger.Errorf("retention: fetch all recordings: %v", err)
		return
	}

	deleted := 0
	for _, rec := range allRecs {
		if r.diskUsagePercent() < float64(r.diskWatermark)-5 {
			break
		}
		os.Remove(rec.FilePath)
		r.repo.Delete(&rec)
		deleted++
	}
	if deleted > 0 {
		logger.Infof("retention: watermark cleanup deleted %d recordings", deleted)
	}
}

func (r *Retention) diskUsagePercent() float64 {
	usage, err := disk.Usage(r.recordPath)
	if err != nil {
		logger.Errorf("retention: disk usage: %v", err)
		return 0
	}
	return usage.UsedPercent
}
