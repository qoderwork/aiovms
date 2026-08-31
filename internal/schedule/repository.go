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
	// FindByIDAndTenant loads a schedule only if it belongs to the given tenant.
	// All request-facing lookups MUST use this method to enforce tenant isolation.
	FindByIDAndTenant(id string, tenantID int64) (*model.RecordSchedule, error)
	Delete(id string) error
	// DeleteByCamera removes all schedules belonging to a camera. Called by
	// the reconciler when a camera has been deleted but its schedules remain
	// (orphan cleanup), and by camera.Delete for cascade prevention.
	DeleteByCamera(cameraID string) error
	FindAllEnabled() ([]model.RecordSchedule, error)
	FindEnabledByCamera(cameraID string) ([]model.RecordSchedule, error)
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

func (r *repository) FindByIDAndTenant(id string, tenantID int64) (*model.RecordSchedule, error) {
	var sch model.RecordSchedule
	err := r.db.Where("id = ? AND license_id = ?", id, tenantID).First(&sch).Error
	if err != nil {
		return nil, err
	}
	return &sch, nil
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

func (r *repository) DeleteByCamera(cameraID string) error {
	return r.db.Where("camera_id = ?", cameraID).Delete(&model.RecordSchedule{}).Error
}

func (r *repository) FindAllEnabled() ([]model.RecordSchedule, error) {
	var schedules []model.RecordSchedule
	err := r.db.Where("enabled = ?", true).Find(&schedules).Error
	return schedules, err
}

func (r *repository) FindEnabledByCamera(cameraID string) ([]model.RecordSchedule, error) {
	var schedules []model.RecordSchedule
	err := r.db.Where("enabled = ? AND camera_id = ?", true, cameraID).Find(&schedules).Error
	return schedules, err
}
