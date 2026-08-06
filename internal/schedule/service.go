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
	Get(ctx context.Context, id string) (*model.RecordSchedule, error)
	Create(ctx context.Context, sch *model.RecordSchedule) error
	Update(ctx context.Context, id string, sch *model.RecordSchedule) error
	Delete(ctx context.Context, id string) error
	Toggle(ctx context.Context, id string) (*model.RecordSchedule, error)
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

func (s *service) Get(ctx context.Context, id string) (*model.RecordSchedule, error) {
	sch, err := s.repo.FindByID(id)
	if err != nil {
		return nil, apperror.ErrScheduleNotFound
	}
	return sch, nil
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

func (s *service) Update(ctx context.Context, id string, sch *model.RecordSchedule) error {
	existing, err := s.repo.FindByID(id)
	if err != nil {
		return apperror.ErrScheduleNotFound
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

func (s *service) Delete(ctx context.Context, id string) error {
	_, err := s.repo.FindByID(id)
	if err != nil {
		return apperror.ErrScheduleNotFound
	}
	return s.repo.Delete(id)
}

func (s *service) Toggle(ctx context.Context, id string) (*model.RecordSchedule, error) {
	existing, err := s.repo.FindByID(id)
	if err != nil {
		return nil, apperror.ErrScheduleNotFound
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
