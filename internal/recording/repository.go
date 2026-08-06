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
	FindAll(tenantID int64, cameraID, startTime, endTime string, offset, limit int) ([]model.Recording, int64, error)
	Delete(rec *model.Recording) error
	FindByPath(filePath string) (*model.Recording, error)
	FindOlderThan(cutoff time.Time) ([]model.Recording, error)
	FindAllSortedByTime() ([]model.Recording, error)
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

func (r *repository) FindByPath(filePath string) (*model.Recording, error) {
	var rec model.Recording
	err := r.db.Where("file_path = ?", filePath).First(&rec).Error
	if err != nil {
		return nil, err
	}
	return &rec, nil
}

func (r *repository) FindOlderThan(cutoff time.Time) ([]model.Recording, error) {
	var recs []model.Recording
	err := r.db.Where("start_time < ?", cutoff).Find(&recs).Error
	return recs, err
}

func (r *repository) FindAllSortedByTime() ([]model.Recording, error) {
	var recs []model.Recording
	err := r.db.Order("start_time ASC").Find(&recs).Error
	return recs, err
}
