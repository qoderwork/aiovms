package schedule

import (
	"aiovms/internal/model"
	"aiovms/pkg/baserepo"
	"gorm.io/gorm"
)

type Repository interface {
	Create(sch *model.RecordSchedule) error
	Update(sch *model.RecordSchedule) error
	FindAll(tenantID int64, cameraID string) ([]model.RecordSchedule, error)
	FindByID(id string) (*model.RecordSchedule, error)
	Delete(id string) error
	FindAllEnabled() ([]model.RecordSchedule, error)
}

type repository struct {
	*baserepo.BaseRepository[model.RecordSchedule, string]
	db *gorm.DB
}

func NewRepository(db *gorm.DB) Repository {
	return &repository{
		BaseRepository: baserepo.New[model.RecordSchedule, string](db, "id"),
		db:             db,
	}
}

func (r *repository) Update(sch *model.RecordSchedule) error {
	return r.db.Save(sch).Error
}

func (r *repository) FindAll(tenantID int64, cameraID string) ([]model.RecordSchedule, error) {
	q := r.db.Where("license_id = ?", tenantID)
	if cameraID != "" {
		q = q.Where("camera_id = ?", cameraID)
	}
	var schedules []model.RecordSchedule
	err := q.Order("created_at DESC").Find(&schedules).Error
	return schedules, err
}

func (r *repository) Delete(id string) error {
	return r.db.Where("id = ?", id).Delete(&model.RecordSchedule{}).Error
}

func (r *repository) FindAllEnabled() ([]model.RecordSchedule, error) {
	var schedules []model.RecordSchedule
	err := r.db.Where("enabled = ?", true).Find(&schedules).Error
	return schedules, err
}
