package baserepo

import "gorm.io/gorm"

type BaseRepository[T any, PK comparable] struct {
	DB       *gorm.DB
	PKColumn string
}

func New[T any, PK comparable](db *gorm.DB, pkColumn string) *BaseRepository[T, PK] {
	if pkColumn == "" {
		pkColumn = "id"
	}
	return &BaseRepository[T, PK]{DB: db, PKColumn: pkColumn}
}

type PageResult[T any] struct {
	Items []T
	Total int64
}

func (r *BaseRepository[T, PK]) Create(entity *T) error { return r.DB.Create(entity).Error }
func (r *BaseRepository[T, PK]) Save(entity *T) error   { return r.DB.Save(entity).Error }

func (r *BaseRepository[T, PK]) FindByID(id PK) (*T, error) {
	var entity T
	if err := r.DB.Where(r.PKColumn+" = ?", id).First(&entity).Error; err != nil {
		return nil, err
	}
	return &entity, nil
}

func (r *BaseRepository[T, PK]) DeleteByID(id PK) error {
	var entity T
	return r.DB.Where(r.PKColumn+" = ?", id).Delete(&entity).Error
}

func (r *BaseRepository[T, PK]) UpdateFields(id PK, fields map[string]interface{}) error {
	var entity T
	return r.DB.Model(&entity).Where(r.PKColumn+" = ?", id).Updates(fields).Error
}

func (r *BaseRepository[T, PK]) FindAll(query *gorm.DB) ([]T, error) {
	var items []T
	if err := query.Find(&items).Error; err != nil {
		return nil, err
	}
	return items, nil
}

func (r *BaseRepository[T, PK]) Count(query *gorm.DB) (int64, error) {
	var count int64
	if err := query.Count(&count).Error; err != nil {
		return 0, err
	}
	return count, nil
}

func (r *BaseRepository[T, PK]) FindPage(baseQuery *gorm.DB, orderCol string, offset, limit int) (*PageResult[T], error) {
	var items []T
	var total int64
	if err := baseQuery.Count(&total).Error; err != nil {
		return nil, err
	}
	if err := baseQuery.Order(orderCol).Offset(offset).Limit(limit).Find(&items).Error; err != nil {
		return nil, err
	}
	return &PageResult[T]{Items: items, Total: total}, nil
}
