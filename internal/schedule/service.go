package schedule

import (
	"context"
	"time"

	"github.com/google/uuid"

	"aiovms/internal/model"
	"aiovms/pkg/apperror"
)

type Service interface {
	List(ctx context.Context, tenantID int64, cameraID string) ([]model.RecordSchedule, error)
	Get(ctx context.Context, tenantID int64, id string) (*model.RecordSchedule, error)
	Create(ctx context.Context, sch *model.RecordSchedule) error
	Update(ctx context.Context, tenantID int64, id string, sch *model.RecordSchedule) error
	Delete(ctx context.Context, tenantID int64, id string) error
	Toggle(ctx context.Context, tenantID int64, id string) (*model.RecordSchedule, error)
}

type service struct {
	repo Repository
}

func NewService(repo Repository) Service {
	return &service{repo: repo}
}

func (s *service) List(ctx context.Context, tenantID int64, cameraID string) ([]model.RecordSchedule, error) {
	return s.repo.FindAll(tenantID, cameraID)
}

// getForTenant loads a schedule and enforces tenant isolation.
// Returns ErrScheduleNotFound when absent, ErrForbidden when it belongs to
// another tenant (design doc: 越权返回 403).
func (s *service) getForTenant(tenantID int64, id string) (*model.RecordSchedule, error) {
	sch, err := s.repo.FindByIDAndTenant(id, tenantID)
	if err == nil {
		return sch, nil
	}
	if _, err := s.repo.FindByID(id); err == nil {
		return nil, apperror.ErrForbidden.WithMessage("schedule belongs to another tenant")
	}
	return nil, apperror.ErrScheduleNotFound
}

func (s *service) Get(ctx context.Context, tenantID int64, id string) (*model.RecordSchedule, error) {
	return s.getForTenant(tenantID, id)
}

func (s *service) Create(ctx context.Context, sch *model.RecordSchedule) error {
	if err := validateTimeRange(sch.StartTime, sch.EndTime); err != nil {
		return err
	}
	sch.ID = uuid.NewString()
	sch.CreatedAt = time.Now()
	sch.UpdatedAt = time.Now()
	return s.repo.Create(sch)
}

func (s *service) Update(ctx context.Context, tenantID int64, id string, sch *model.RecordSchedule) error {
	existing, err := s.getForTenant(tenantID, id)
	if err != nil {
		return err
	}
	if err := validateTimeRange(sch.StartTime, sch.EndTime); err != nil {
		return err
	}
	existing.Name = sch.Name
	existing.Enabled = sch.Enabled
	existing.Weekdays = sch.Weekdays
	existing.StreamType = sch.StreamType
	existing.StartTime = sch.StartTime
	existing.EndTime = sch.EndTime
	existing.UpdatedAt = time.Now()
	return s.repo.Update(existing)
}

func (s *service) Delete(ctx context.Context, tenantID int64, id string) error {
	if _, err := s.getForTenant(tenantID, id); err != nil {
		return err
	}
	return s.repo.Delete(id)
}

func (s *service) Toggle(ctx context.Context, tenantID int64, id string) (*model.RecordSchedule, error) {
	existing, err := s.getForTenant(tenantID, id)
	if err != nil {
		return nil, err
	}
	existing.Enabled = !existing.Enabled
	existing.UpdatedAt = time.Now()
	if err := s.repo.Update(existing); err != nil {
		return nil, err
	}
	return existing, nil
}

func validateTimeRange(start, end string) error {
	if start == "" || end == "" {
		return nil
	}
	if start >= end {
		return apperror.ErrInvalidInput.WithMessage("start_time must be earlier than end_time")
	}
	return nil
}
