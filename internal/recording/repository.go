package recording

import (
	"time"

	"aiovms/internal/model"
	"aiovms/pkg/baserepo"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type Repository interface {
	Create(rec *model.Recording) error
	Update(rec *model.Recording) error
	Upsert(rec *model.Recording) error
	FindByID(id string) (*model.Recording, error)
	// FindByIDAndTenant loads a recording only if it belongs to the given tenant.
	// All request-facing lookups MUST use this method to enforce tenant isolation.
	FindByIDAndTenant(id string, tenantID int64) (*model.Recording, error)
	FindAll(tenantID int64, cameraID, startTime, endTime string, offset, limit int) ([]model.Recording, int64, error)
	Delete(rec *model.Recording) error
	DeleteByIDs(ids []string) error
	FindByPath(filePath string) (*model.Recording, error)
	// ListFileSizes returns a map of file_path -> file_size for all recordings.
	// Used by the scanner to skip re-probing files that are already ingested
	// and unchanged (avoids repeated MP4 box parsing on every scan cycle).
	ListFileSizes() (map[string]int64, error)
	// FindByCamera returns all recordings of a camera (no pagination). Used by
	// DeleteByCamera to clear all recordings of a camera.
	FindByCamera(cameraID string) ([]model.Recording, error)
	FindOlderThan(cutoff time.Time) ([]model.Recording, error)
	FindOlderThanByStatus(cutoff time.Time, status string) ([]model.Recording, error)
	FindOldestComplete(limit int) ([]model.Recording, error)
	FindAllSortedByTime() ([]model.Recording, error)

	// Recording session operations.
	CreateSession(sess *model.RecordingSession) error
	FindActiveSessions() ([]model.RecordingSession, error)
	FindActiveSessionByCamera(cameraID string) (*model.RecordingSession, error)
	FindActiveManualSessionByCamera(cameraID string) (*model.RecordingSession, error)
	FindActiveSessionBySchedule(scheduleID string) (*model.RecordingSession, error)
	CloseSession(id string, endTime time.Time) error
	// CloseActiveSessionsByCamera sets end_time on all still-open sessions
	// for a camera. Called by the reconciler when a camera has been deleted
	// but its active sessions remain (orphan cleanup), and by camera.Delete
	// for cascade prevention.
	CloseActiveSessionsByCamera(cameraID string, endTime time.Time) error
	// FindSessionByCameraAndTime returns the session whose [start_time, end_time]
	// interval covers the given time t. Used by scanner to link an mp4 file to its
	// originating session. Returns gorm.ErrRecordNotFound if none matches.
	FindSessionByCameraAndTime(cameraID string, t time.Time) (*model.RecordingSession, error)
}

type repository struct {
	*baserepo.BaseRepository[model.Recording, string]
	db *gorm.DB
}

func NewRepository(db *gorm.DB) Repository {
	return &repository{
		BaseRepository: baserepo.New[model.Recording, string](db, "id"),
		db:             db,
	}
}

func (r *repository) Update(rec *model.Recording) error {
	return r.db.Save(rec).Error
}

func (r *repository) FindByIDAndTenant(id string, tenantID int64) (*model.Recording, error) {
	var rec model.Recording
	err := r.db.Where("id = ? AND license_id = ?", id, tenantID).First(&rec).Error
	if err != nil {
		return nil, err
	}
	return &rec, nil
}

// Upsert inserts a recording or updates matching file_path via ON DUPLICATE KEY UPDATE.
// Atomic operation avoids the race condition of FindByPath-then-Create.
func (r *repository) Upsert(rec *model.Recording) error {
	return r.db.Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "file_path"}},
		DoUpdates: clause.AssignmentColumns([]string{
			"file_size", "start_time", "end_time", "duration",
			"codec", "resolution", "status", "updated_at",
		}),
	}).Create(rec).Error
}

func (r *repository) FindAll(tenantID int64, cameraID, startTime, endTime string, offset, limit int) ([]model.Recording, int64, error) {
	q := r.db.Model(&model.Recording{}).Where("license_id = ?", tenantID)
	if cameraID != "" {
		q = q.Where("camera_id = ?", cameraID)
	}
	if startTime != "" {
		q = q.Where("start_time >= ?", startTime)
	}
	if endTime != "" {
		q = q.Where("start_time <= ?", endTime)
	}
	result, err := r.FindPage(q, "start_time DESC", offset, limit)
	if err != nil {
		return nil, 0, err
	}
	return result.Items, result.Total, nil
}

func (r *repository) Delete(rec *model.Recording) error {
	return r.db.Delete(rec).Error
}

// DeleteByIDs batch-deletes recordings by primary key IDs.
// Uses a single DELETE ... WHERE id IN (...) query for efficiency.
func (r *repository) DeleteByIDs(ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	return r.db.Where("id IN ?", ids).Delete(&model.Recording{}).Error
}

func (r *repository) FindByPath(filePath string) (*model.Recording, error) {
	var rec model.Recording
	err := r.db.Where("file_path = ?", filePath).First(&rec).Error
	if err != nil {
		return nil, err
	}
	return &rec, nil
}

// ListFileSizes loads all ingested recording paths and their sizes in one
// query. The scanner uses this as an in-memory index to skip re-probing
// unchanged files, avoiding thousands of ProbeMP4 (box parsing) calls per scan.
func (r *repository) ListFileSizes() (map[string]int64, error) {
	var rows []struct {
		FilePath string `gorm:"column:file_path"`
		FileSize int64  `gorm:"column:file_size"`
	}
	err := r.db.Model(&model.Recording{}).
		Select("file_path", "file_size").
		Find(&rows).Error
	if err != nil {
		return nil, err
	}
	m := make(map[string]int64, len(rows))
	for _, row := range rows {
		m[row.FilePath] = row.FileSize
	}
	return m, nil
}

// FindByCamera returns all recordings of a camera (no pagination).
func (r *repository) FindByCamera(cameraID string) ([]model.Recording, error) {
	var recs []model.Recording
	err := r.db.Where("camera_id = ?", cameraID).Find(&recs).Error
	return recs, err
}

func (r *repository) FindOlderThan(cutoff time.Time) ([]model.Recording, error) {
	var recs []model.Recording
	err := r.db.Where("start_time < ?", cutoff).Find(&recs).Error
	return recs, err
}

// FindOlderThanByStatus fetches recordings older than cutoff with a specific status.
// Filters to "complete" to avoid deleting still-writing segments.
func (r *repository) FindOlderThanByStatus(cutoff time.Time, status string) ([]model.Recording, error) {
	var recs []model.Recording
	err := r.db.Where("start_time < ? AND status = ?", cutoff, status).
		Order("start_time ASC").Find(&recs).Error
	return recs, err
}

// FindOldestComplete returns the oldest completed recordings for disk-threshold cleanup.
// Limits the result to avoid loading all recordings into memory at once.
func (r *repository) FindOldestComplete(limit int) ([]model.Recording, error) {
	var recs []model.Recording
	err := r.db.Where("status = ?", "complete").
		Order("start_time ASC").Limit(limit).Find(&recs).Error
	return recs, err
}

func (r *repository) FindAllSortedByTime() ([]model.Recording, error) {
	var recs []model.Recording
	err := r.db.Order("start_time ASC").Find(&recs).Error
	return recs, err
}

// CreateSession inserts a new recording session.
func (r *repository) CreateSession(sess *model.RecordingSession) error {
	return r.db.Create(sess).Error
}

// FindActiveSessions returns all sessions with end_time IS NULL (currently recording).
// Used by recovery logic to re-apply record:true after MediaMTX restart.
func (r *repository) FindActiveSessions() ([]model.RecordingSession, error) {
	var sessions []model.RecordingSession
	err := r.db.Where("end_time IS NULL").Find(&sessions).Error
	return sessions, err
}

// FindActiveSessionByCamera returns the single active session for a camera, if any.
// If multiple exist (should not happen in normal flow), returns the latest by start_time.
func (r *repository) FindActiveSessionByCamera(cameraID string) (*model.RecordingSession, error) {
	var sess model.RecordingSession
	err := r.db.Where("camera_id = ? AND end_time IS NULL", cameraID).
		Order("start_time DESC").First(&sess).Error
	if err != nil {
		return nil, err
	}
	return &sess, nil
}

// FindActiveManualSessionByCamera returns the active manual session for a
// camera, if any. Returns gorm.ErrRecordNotFound if no manual session exists.
func (r *repository) FindActiveManualSessionByCamera(cameraID string) (*model.RecordingSession, error) {
	var sess model.RecordingSession
	err := r.db.Where("camera_id = ? AND end_time IS NULL AND trigger_type = ?", cameraID, "manual").
		Order("start_time DESC").First(&sess).Error
	if err != nil {
		return nil, err
	}
	return &sess, nil
}

// FindActiveSessionBySchedule returns the active session for a schedule, if any.
// Used by the reconciler to close sessions when a schedule time window ends.
func (r *repository) FindActiveSessionBySchedule(scheduleID string) (*model.RecordingSession, error) {
	var sess model.RecordingSession
	err := r.db.Where("schedule_id = ? AND end_time IS NULL", scheduleID).
		Order("start_time DESC").First(&sess).Error
	if err != nil {
		return nil, err
	}
	return &sess, nil
}

// CloseSession sets end_time on a session, marking it stopped.
// Uses UPDATE ... WHERE end_time IS NULL to avoid closing an already-closed session.
func (r *repository) CloseSession(id string, endTime time.Time) error {
	res := r.db.Model(&model.RecordingSession{}).
		Where("id = ? AND end_time IS NULL", id).
		Updates(map[string]any{"end_time": endTime, "updated_at": endTime})
	return res.Error
}

// CloseActiveSessionsByCamera closes all still-open sessions of a camera.
func (r *repository) CloseActiveSessionsByCamera(cameraID string, endTime time.Time) error {
	res := r.db.Model(&model.RecordingSession{}).
		Where("camera_id = ? AND end_time IS NULL", cameraID).
		Updates(map[string]any{"end_time": endTime, "updated_at": endTime})
	return res.Error
}

// FindSessionByCameraAndTime returns the session covering time t for a camera.
// A session covers t if start_time <= t AND (end_time IS NULL OR end_time >= t).
// If multiple match (should not happen), returns the latest by start_time.
func (r *repository) FindSessionByCameraAndTime(cameraID string, t time.Time) (*model.RecordingSession, error) {
	var sess model.RecordingSession
	err := r.db.Where(
		"camera_id = ? AND start_time <= ? AND (end_time IS NULL OR end_time >= ?)",
		cameraID, t, t,
	).Order("start_time DESC").First(&sess).Error
	if err != nil {
		return nil, err
	}
	return &sess, nil
}
