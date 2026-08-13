package recording

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"aiovms/internal/model"
)

// mockRepoForRetention implements Repository for retention tests.
type mockRepoForRetention struct {
	recs         []model.Recording
	deletedPaths []string
}

func (m *mockRepoForRetention) Create(rec *model.Recording) error          { return nil }
func (m *mockRepoForRetention) Update(rec *model.Recording) error          { return nil }
func (m *mockRepoForRetention) Upsert(rec *model.Recording) error          { return nil }
func (m *mockRepoForRetention) FindByID(id string) (*model.Recording, error) { return nil, nil }
func (m *mockRepoForRetention) FindAll(tenantID int64, cameraID, startTime, endTime string, offset, limit int) ([]model.Recording, int64, error) {
	return nil, 0, nil
}
func (m *mockRepoForRetention) FindByPath(filePath string) (*model.Recording, error) { return nil, nil }

func (m *mockRepoForRetention) Delete(rec *model.Recording) error {
	m.deletedPaths = append(m.deletedPaths, rec.FilePath)
	return nil
}

func (m *mockRepoForRetention) DeleteByIDs(ids []string) error {
	for _, id := range ids {
		for _, r := range m.recs {
			if r.ID == id {
				m.deletedPaths = append(m.deletedPaths, r.FilePath)
				break
			}
		}
	}
	return nil
}

func (m *mockRepoForRetention) FindOlderThan(cutoff time.Time) ([]model.Recording, error) {
	var result []model.Recording
	for _, r := range m.recs {
		if r.StartTime.Before(cutoff) {
			result = append(result, r)
		}
	}
	return result, nil
}

func (m *mockRepoForRetention) FindOlderThanByStatus(cutoff time.Time, status string) ([]model.Recording, error) {
	var result []model.Recording
	for _, r := range m.recs {
		if r.StartTime.Before(cutoff) && r.Status == status {
			result = append(result, r)
		}
	}
	return result, nil
}

func (m *mockRepoForRetention) FindOldestComplete(limit int) ([]model.Recording, error) {
	var complete []model.Recording
	for _, r := range m.recs {
		if r.Status == "complete" {
			complete = append(complete, r)
		}
	}
	if len(complete) > limit {
		return complete[:limit], nil
	}
	return complete, nil
}

func (m *mockRepoForRetention) FindAllSortedByTime() ([]model.Recording, error) {
	return m.recs, nil
}

// Session methods are not exercised by retention tests; stub them to satisfy Repository.
func (m *mockRepoForRetention) CreateSession(sess *model.RecordingSession) error                  { return nil }
func (m *mockRepoForRetention) FindActiveSessions() ([]model.RecordingSession, error)             { return nil, nil }
func (m *mockRepoForRetention) FindActiveSessionByCamera(cameraID string) (*model.RecordingSession, error) {
	return nil, nil
}
func (m *mockRepoForRetention) FindActiveSessionBySchedule(scheduleID string) (*model.RecordingSession, error) {
	return nil, nil
}
func (m *mockRepoForRetention) CloseSession(id string, endTime time.Time) error                   { return nil }
func (m *mockRepoForRetention) FindSessionByCameraAndTime(cameraID string, t time.Time) (*model.RecordingSession, error) {
	return nil, nil
}

func TestCleanupByAge(t *testing.T) {
	now := time.Now()

	mock := &mockRepoForRetention{
		recs: []model.Recording{
			{ID: "1", FilePath: "/tmp/old1.mp4", StartTime: now.Add(-8 * 24 * time.Hour), Status: "complete"},
			{ID: "2", FilePath: "/tmp/old2.mp4", StartTime: now.Add(-9 * 24 * time.Hour), Status: "complete"},
			{ID: "3", FilePath: "/tmp/new1.mp4", StartTime: now.Add(-2 * 24 * time.Hour), Status: "complete"},
		},
	}

	r := &Retention{
		repo:          mock,
		recordPath:    os.TempDir(),
		retentionDays: 7,
		diskWatermark: 90,
	}

	r.cleanupByAge()

	if len(mock.deletedPaths) != 2 {
		t.Fatalf("expected 2 deleted, got %d", len(mock.deletedPaths))
	}
	if mock.deletedPaths[0] != "/tmp/old1.mp4" {
		t.Errorf("deleted[0] = %q, want /tmp/old1.mp4", mock.deletedPaths[0])
	}
	if mock.deletedPaths[1] != "/tmp/old2.mp4" {
		t.Errorf("deleted[1] = %q, want /tmp/old2.mp4", mock.deletedPaths[1])
	}
}

func TestCleanupByAgeNoOldRecords(t *testing.T) {
	now := time.Now()

	mock := &mockRepoForRetention{
		recs: []model.Recording{
			{ID: "1", FilePath: "/tmp/new1.mp4", StartTime: now.Add(-1 * 24 * time.Hour), Status: "complete"},
		},
	}

	r := &Retention{
		repo:          mock,
		recordPath:    os.TempDir(),
		retentionDays: 7,
		diskWatermark: 90,
	}

	r.cleanupByAge()

	if len(mock.deletedPaths) != 0 {
		t.Fatalf("expected 0 deleted, got %d", len(mock.deletedPaths))
	}
}

func TestCleanupByAgeEmptyRepo(t *testing.T) {
	mock := &mockRepoForRetention{}

	r := &Retention{
		repo:          mock,
		recordPath:    os.TempDir(),
		retentionDays: 7,
		diskWatermark: 90,
	}

	r.cleanupByAge()

	if len(mock.deletedPaths) != 0 {
		t.Fatalf("expected 0 deleted, got %d", len(mock.deletedPaths))
	}
}

func TestDiskUsagePercent(t *testing.T) {
	r := &Retention{recordPath: os.TempDir()}
	pct := r.diskUsagePercent()
	// TempDir should exist and return a valid usage percentage
	if pct <= 0 || pct > 100 {
		t.Errorf("unexpected disk usage: %.1f%%", pct)
	}
}

func TestDiskUsagePercentMissingPath(t *testing.T) {
	r := &Retention{recordPath: filepath.Join(os.TempDir(), "nonexistent-dir-12345")}
	pct := r.diskUsagePercent()
	// Non-existent path: gopsutil/disk returns error, we return 0.
	if pct != 0 {
		t.Errorf("expected 0 for missing path, got %.1f", pct)
	}
}

func TestRetentionStop(t *testing.T) {
	mock := &mockRepoForRetention{}
	r := &Retention{
		repo:          mock,
		recordPath:    os.TempDir(),
		retentionDays: 7,
		diskWatermark: 90,
		stopCh:        make(chan struct{}),
	}

	// Multiple stops must not panic.
	r.Stop()
	r.Stop()
	r.Stop()
}

func TestRetentionStopBeforeRun(t *testing.T) {
	mock := &mockRepoForRetention{}
	r := NewRetention(mock, os.TempDir(), 7, 90)
	// Stop before Run should not panic.
	r.Stop()
}
