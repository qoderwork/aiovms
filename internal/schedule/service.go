package schedule

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/robfig/cron/v3"

	"aiovms/internal/mediamtx"
	"aiovms/internal/model"
	"aiovms/pkg/apperror"
	"aiovms/pkg/logger"
)

type Service interface {
	List(ctx context.Context, tenantID int64, cameraID string) ([]model.RecordSchedule, error)
	Get(ctx context.Context, id string) (*model.RecordSchedule, error)
	Create(ctx context.Context, sch *model.RecordSchedule) error
	Update(ctx context.Context, id string, sch *model.RecordSchedule) error
	Delete(ctx context.Context, id string) error
	Toggle(ctx context.Context, id string) (*model.RecordSchedule, error)

	// Called by cron scheduler every 60s to check active schedules
	TriggerActive(ctx context.Context) error
}

// mediaMTXClient abstracts MediaMTX API for testability.
type mediaMTXClient interface {
	PatchPath(name string, patch map[string]any) error
	ListPaths() ([]mediamtx.PathInfo, error)
}

type service struct {
	repo     Repository
	camRepo  CameraRepository
	mtx      mediaMTXClient
	cronSched *cron.Cron
}

type CameraRepository interface {
	FindByID(id string) (*model.Camera, error)
}

func NewService(repo Repository, camRepo CameraRepository, mtx *mediamtx.Client) Service {
	cronSched := cron.New(cron.WithSeconds())
	svc := &service{repo: repo, camRepo: camRepo, mtx: mtx, cronSched: cronSched}
	cronSched.AddFunc("@every 60s", svc.triggerJob)
	cronSched.Start()
	return svc
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

// Toggle flips the enabled flag of a schedule.
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

// validateTimeRange ensures start_time < end_time (both "HH:MM" format).
func validateTimeRange(start, end string) error {
	if start == "" || end == "" {
		return nil
	}
	if start >= end {
		return apperror.ErrInvalidInput.WithMessage("start_time must be earlier than end_time")
	}
	return nil
}

func (s *service) TriggerActive(ctx context.Context) error {
	s.triggerJob()
	return nil
}

func (s *service) triggerJob() {
	now := time.Now()
	weekday := fmt.Sprintf("%d", now.Weekday()) // 0=Sun
	timeStr := now.Format("15:04")

	schedules, err := s.repo.FindAllEnabled()
	if err != nil {
		logger.Errorf("schedule trigger: fetch failed: %v", err)
		return
	}

	// Read back MediaMTX actual recording states to reconcile DB drift.
	mtxRecording := make(map[string]bool) // path -> recording?
	if paths, err := s.mtx.ListPaths(); err == nil {
		for _, p := range paths {
			mtxRecording[p.Name] = isRecordingPath(p)
		}
	} else {
		logger.Warnf("schedule trigger: list paths failed, fallback to DB state only: %v", err)
	}

	for i := range schedules {
		sch := &schedules[i]
		if !sch.Enabled {
			continue
		}
		if !containsWeekday(sch.Weekdays, weekday) {
			continue
		}

		cam, err := s.camRepo.FindByID(sch.CameraID)
		if err != nil || cam == nil {
			logger.Errorf("schedule trigger: camera %s not found", sch.CameraID)
			continue
		}

		inWindow := true
		if sch.StartTime != "" && timeStr < sch.StartTime {
			inWindow = false
		}
		if sch.EndTime != "" && timeStr > sch.EndTime {
			inWindow = false
		}

		actualRecording := mtxRecording[cam.MediaMTXPath]

		switch {
		case inWindow && sch.LastAction != "start":
			// Should be recording but not started — start it.
			if err := s.mtx.PatchPath(cam.MediaMTXPath, map[string]any{"record": true}); err != nil {
				logger.Errorf("schedule trigger: start failed for camera %s: %v", sch.CameraID, err)
				continue
			}
			sch.LastAction = "start"
			nowCopy := now
			sch.LastTriggeredAt = &nowCopy
			_ = s.repo.Update(sch)
			logger.Infof("schedule: started recording camera %s (schedule %s)", sch.CameraID, sch.Name)

		case !inWindow && sch.LastAction == "start":
			// Was recording, window ended — stop it.
			if err := s.mtx.PatchPath(cam.MediaMTXPath, map[string]any{"record": false}); err != nil {
				logger.Errorf("schedule trigger: stop failed for camera %s: %v", sch.CameraID, err)
				continue
			}
			sch.LastAction = "stop"
			_ = s.repo.Update(sch)
			logger.Infof("schedule: stopped recording camera %s (schedule %s)", sch.CameraID, sch.Name)

		case inWindow && sch.LastAction == "start" && !actualRecording:
			// DB says started but MediaMTX lost state (e.g. restart) — re-start.
			if err := s.mtx.PatchPath(cam.MediaMTXPath, map[string]any{"record": true}); err != nil {
				logger.Errorf("schedule trigger: re-start failed for camera %s: %v", sch.CameraID, err)
				continue
			}
			logger.Warnf("schedule: re-started recording camera %s (MediaMTX state drift)", sch.CameraID)
		}
	}
}

// isRecordingPath checks whether a MediaMTX path is currently recording.
func isRecordingPath(p mediamtx.PathInfo) bool {
	// PathInfo does not carry a record flag directly; treat presence in list
	// with Ready=true as an active path. Actual record state is controlled by
	// our PatchPath calls, so this is a best-effort reconciliation.
	return p.Ready
}

func containsWeekday(weekdays string, day string) bool {
	if weekdays == "" {
		return false
	}
	for _, d := range strings.Split(weekdays, ",") {
		if strings.TrimSpace(d) == day {
			return true
		}
	}
	return false
}
