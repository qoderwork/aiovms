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
//
// Improved with patterns from MiBeeNvr (MIT License):
//   - Batch deletion with size limits to avoid long-running DB transactions
//   - Disk-threshold cleanup rechecks disk usage after each batch
//   - Status-aware: skips "recording" (still-writing) files
//   - Pre-filters expired recordings instead of scanning all files
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
	if r.retentionDays <= 0 {
		return
	}

	cutoff := time.Now().Add(-time.Duration(r.retentionDays) * 24 * time.Hour)
	recordings, err := r.repo.FindOlderThanByStatus(cutoff, "complete")
	if err != nil {
		logger.Errorf("retention: query old recordings: %v", err)
		return
	}

	// Batch delete to avoid holding a long transaction.
	// Each pair of os.Remove + repo.Delete is atomic: file goes first,
	// DB record follows. If DB delete fails, scanner will re-discover
	// and upsert; if file delete fails, it's just a waste of space.
	// This avoids the worse failure mode: DB record deleted but file
	// remains as undetectable orphan.
	const batchSize = 50
	deleted := 0
	for i := 0; i < len(recordings); i += batchSize {
		end := i + batchSize
		if end > len(recordings) {
			end = len(recordings)
		}
		batch := recordings[i:end]
		ids := make([]string, len(batch))
		for j, rec := range batch {
			ids[j] = rec.ID
		}

		// Delete files first (best-effort), then batch-delete DB records.
		for _, rec := range batch {
			if rec.FilePath != "" {
				_ = os.Remove(rec.FilePath)
			}
		}
		if err := r.repo.DeleteByIDs(ids); err != nil {
			logger.Warnf("retention: batch delete DB records: %v", err)
			// Continue with next batch — partial success is better than
			// blocking forever on a transient DB error.
		} else {
			deleted += len(batch)
		}
	}

	if deleted > 0 {
		logger.Infof("retention: deleted %d recordings older than %v",
			deleted, cutoff.Format("2006-01-02"))
	}
}

func (r *Retention) cleanupByDiskUsage() {
	if r.diskWatermark <= 0 {
		return
	}

	usage := r.diskUsagePercent()
	if usage < float64(r.diskWatermark) {
		return
	}

	logger.Warnf("retention: disk usage %.1f%% > %d%%, cleaning oldest recordings",
		usage, r.diskWatermark)

	// Fetch oldest recordings in batches rather than loading everything.
	const batchSize = 50
	const targetUsage = 5.0 // stop when usage drops watermark-5%

	deleted := 0
	for {
		if r.diskUsagePercent() < float64(r.diskWatermark)-targetUsage {
			break
		}

		batch, err := r.repo.FindOldestComplete(batchSize)
		if err != nil {
			logger.Errorf("retention: fetch oldest recordings: %v", err)
			return
		}
		if len(batch) == 0 {
			break
		}

		ids := make([]string, len(batch))
		for j, rec := range batch {
			ids[j] = rec.ID
			if rec.FilePath != "" {
				_ = os.Remove(rec.FilePath)
			}
		}

		if err := r.repo.DeleteByIDs(ids); err != nil {
			logger.Warnf("retention: batch delete for disk threshold: %v", err)
		} else {
			deleted += len(batch)
		}
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
