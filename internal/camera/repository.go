package camera

import (
	"aiovms/internal/model"
	"aiovms/pkg/baserepo"
	"gorm.io/gorm"
)

type Repository interface {
	Create(cam *model.Camera) error
	Update(cam *model.Camera) error
	FindByID(id string) (*model.Camera, error)
	ListByTenant(tenantID int64, query string, offset, limit int) ([]model.Camera, int64, error)
	Delete(id string) error
	FindAllByTenant(tenantID int64) ([]model.Camera, error)
	FindAll() ([]model.Camera, error)
	UpdateStatus(id string, status string) error
}

type repository struct {
	*baserepo.BaseRepository[model.Camera, string]
	db *gorm.DB
}

func NewRepository(db *gorm.DB) Repository {
	return &repository{
		BaseRepository: baserepo.New[model.Camera, string](db, "id"),
		db:             db,
	}
}

func (r *repository) Update(cam *model.Camera) error {
	return r.db.Save(cam).Error
}

func (r *repository) ListByTenant(tenantID int64, query string, offset, limit int) ([]model.Camera, int64, error) {
	q := r.db.Model(&model.Camera{}).Where("license_id = ?", tenantID)
	if query != "" {
		like := "%" + query + "%"
		q = q.Where("name LIKE ? OR ip LIKE ?", like, like)
	}
	result, err := r.FindPage(q, "created_at DESC", offset, limit)
	if err != nil {
		return nil, 0, err
	}
	return result.Items, result.Total, nil
}

func (r *repository) FindAll() ([]model.Camera, error) {
	var cams []model.Camera
	err := r.db.Find(&cams).Error
	return cams, err
}

func (r *repository) Delete(id string) error {
	return r.db.Where("id = ?", id).Delete(&model.Camera{}).Error
}

func (r *repository) FindAllByTenant(tenantID int64) ([]model.Camera, error) {
	var cams []model.Camera
	err := r.db.Where("license_id = ?", tenantID).Find(&cams).Error
	return cams, err
}

func (r *repository) UpdateStatus(id string, status string) error {
	return r.db.Model(&model.Camera{}).Where("id = ?", id).Update("status", status).Error
}
