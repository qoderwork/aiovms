package recording

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/google/uuid"

	"aiovms/internal/model"
	"aiovms/pkg/apperror"
	"aiovms/pkg/logger"
)

type Service interface {
	List(ctx context.Context, tenantID int64, cameraID, startTime, endTime string, page, pageSize int) ([]model.Recording, int64, error)
	Get(ctx context.Context, tenantID int64, id string) (*model.Recording, string, error)
	Delete(ctx context.Context, tenantID int64, id string) error
	// DeleteByCamera deletes all recordings (DB rows + disk files) of a camera.
	DeleteByCamera(ctx context.Context, tenantID int64, cameraID string) (int, error)
	StartManual(ctx context.Context, tenantID int64, cameraID string) error
	StopManual(ctx context.Context, tenantID int64, cameraID string) error
	// CloseActiveManualSession closes any active manual session for the camera.
	// Called by the schedule handler when a schedule is created (schedule has
	// highest priority). No-op if no manual session exists.
	CloseActiveManualSession(cameraID string) error
	Upsert(ctx context.Context, rec *model.Recording) error
}

// ScheduleChecker checks whether a camera currently has an enabled schedule
// within its recording window. Implemented by schedule.Service.
type ScheduleChecker interface {
	IsInWindow(cameraID string, now time.Time) bool
}

// recordActuator abstracts the MediaMTX actuator (single writer) for
// testability. Recording control uses the ASYNC variant on purpose: the API
// contract is intent-commit — the session row is the commit point, and MTX
// convergence is guaranteed by drift recovery + orphan repair, observable
// via vms_drift_events_total.
type recordActuator interface {
	EnqueueSetRecord(path string, on bool)
}

type service struct {
	repo       Repository
	camSvc     CameraService
	act        recordActuator
	schChecker ScheduleChecker
}

type CameraService interface {
	Get(ctx context.Context, tenantID int64, id string) (*model.Camera, error)
}

func NewService(repo Repository, camSvc CameraService, act recordActuator, schChecker ScheduleChecker) Service {
	return &service{repo: repo, camSvc: camSvc, act: act, schChecker: schChecker}
}

func (s *service) List(ctx context.Context, tenantID int64, cameraID, startTime, endTime string, page, pageSize int) ([]model.Recording, int64, error) {
	offset := (page - 1) * pageSize
	if offset < 0 {
		offset = 0
	}
	return s.repo.FindAll(tenantID, cameraID, startTime, endTime, offset, pageSize)
}

// getForTenant loads a recording and enforces tenant isolation.
// Returns ErrRecordingNotFound when absent, ErrForbidden when it belongs to
// another tenant (design doc: 越权返回 403).
func (s *service) getForTenant(tenantID int64, id string) (*model.Recording, error) {
	rec, err := s.repo.FindByIDAndTenant(id, tenantID)
	if err == nil {
		return rec, nil
	}
	if _, err := s.repo.FindByID(id); err == nil {
		return nil, apperror.ErrForbidden.WithMessage("recording belongs to another tenant")
	}
	return nil, apperror.ErrRecordingNotFound
}

func (s *service) Get(ctx context.Context, tenantID int64, id string) (*model.Recording, string, error) {
	rec, err := s.getForTenant(tenantID, id)
	if err != nil {
		return nil, "", err
	}
	// Playback URL. In production the file bytes are delivered by the integrated
	// deployment layer (Java NMS backend or its nginx) from the shared recordings
	// volume — aiovms only reports the path. For local self-test, aiovms serves the
	// same file via GET /recordings/files/* (see Handler.ServeRecording), so this URL
	// is directly openable in a browser. Uses MediaMTXPath (e.g. "cam-a1b2c3d4") which
	// matches the physical directory structure, NOT the full camera UUID.
	playURL := fmt.Sprintf("/recordings/files/%s/%s", rec.MediaMTXPath, rec.Filename)
	return rec, playURL, nil
}

func (s *service) Delete(ctx context.Context, tenantID int64, id string) error {
	rec, err := s.getForTenant(tenantID, id)
	if err != nil {
		return err
	}
	// Delete DB record first. If it succeeds but os.Remove fails, the file
	// will be re-discovered by the scanner on the next cycle and Upserted back.
	// This avoids the worse failure mode: file deleted but DB record persists
	// as an orphan pointing to a non-existent path.
	if err := s.repo.Delete(rec); err != nil {
		return err
	}
	if rec.FilePath != "" {
		if err := os.Remove(rec.FilePath); err != nil && !os.IsNotExist(err) {
			logger.Warnf("recording delete: remove file %s: %v", rec.FilePath, err)
		}
	}
	return nil
}

// DeleteByCamera deletes all recordings (DB rows + disk files) of a camera.
// Tenant isolation is enforced via the camera service lookup (403 if the camera
// belongs to another tenant). Returns the number of deleted recordings.
func (s *service) DeleteByCamera(ctx context.Context, tenantID int64, cameraID string) (int, error) {
	// Enforce tenant isolation: the camera must belong to this tenant.
	if _, err := s.camSvc.Get(ctx, tenantID, cameraID); err != nil {
		return 0, err
	}

	recs, err := s.repo.FindByCamera(cameraID)
	if err != nil {
		return 0, apperror.Wrap(err, 50000, 500, "failed to list recordings for camera")
	}

	for i := range recs {
		rec := &recs[i]
		// Delete DB record first (same rationale as Delete: if os.Remove fails,
		// the scanner re-discovers the file and upserts it back on the next cycle).
		if err := s.repo.Delete(rec); err != nil {
			logger.Errorf("recording delete by camera: delete row %s: %v", rec.ID, err)
			continue
		}
		if rec.FilePath != "" {
			if err := os.Remove(rec.FilePath); err != nil && !os.IsNotExist(err) {
				logger.Warnf("recording delete by camera: remove file %s: %v", rec.FilePath, err)
			}
		}
	}
	return len(recs), nil
}

func (s *service) StartManual(ctx context.Context, tenantID int64, cameraID string) error {
	cam, err := s.camSvc.Get(ctx, tenantID, cameraID)
	if err != nil {
		return err
	}

	// Schedule has highest priority: if any enabled schedule is currently
	// in its recording window, reject manual start to avoid session ambiguity.
	if s.schChecker != nil && s.schChecker.IsInWindow(cameraID, time.Now()) {
		return apperror.New(40903, 409, "schedule recording in progress, cannot start manual")
	}

	// Reject duplicate manual start — prevents truncating an existing manual
	// session. Schedule sessions are left untouched (manual and schedule
	// sessions can coexist independently).
	if existing, err := s.repo.FindActiveManualSessionByCamera(cameraID); err == nil && existing != nil {
		return apperror.New(40902, 409, "camera is already in manual recording")
	}

	// Intent-commit contract: the session row is the commit point. Create it
	// FIRST so the API answer reflects committed intent; the MediaMTX apply
	// is enqueued asynchronously. If the apply is delayed (MTX slow/down),
	// drift recovery (active session + not recording -> re-enable) converges
	// it on a later cycle — the response does not depend on MTX state.
	now := time.Now()
	sess := &model.RecordingSession{
		ID:          uuid.NewString(),
		CameraID:    cameraID,
		TriggerType: "manual",
		StartTime:   now,
		LicenseID:   cam.LicenseID,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	if err := s.repo.CreateSession(sess); err != nil {
		return apperror.Wrap(err, 50000, 500, "failed to create recording session")
	}

	// 注意：一期录像始终使用主码流（cam.MediaMTXPath），stream_type 暂不生效。
	// record-on 同时关闭 sourceOnDemand，确保 MediaMTX 主动拉流录像，不依赖有人预览。
	s.act.EnqueueSetRecord(cam.MediaMTXPath, true)
	return nil
}

func (s *service) StopManual(ctx context.Context, tenantID int64, cameraID string) error {
	cam, err := s.camSvc.Get(ctx, tenantID, cameraID)
	if err != nil {
		return err
	}

	// Only close the manual session — leave schedule sessions untouched so
	// the reconciler can keep schedule recording alive. If no manual session
	// exists, return 404 without touching MediaMTX (a schedule session may
	// still be actively recording).
	sess, sessErr := s.repo.FindActiveManualSessionByCamera(cameraID)
	if sessErr == nil && sess != nil {
		if err := s.repo.CloseSession(sess.ID, time.Now()); err != nil {
			logger.Errorf("stop manual: close session %s for camera %s: %v", sess.ID, cameraID, err)
			return apperror.Wrap(err, 50000, 500, "failed to close recording session")
		}
		s.act.EnqueueSetRecord(cam.MediaMTXPath, false)
		return nil
	}

	return apperror.New(40402, 404, "no active manual recording for this camera")
}

// CloseActiveManualSession closes any active manual session for the camera
// without touching MediaMTX. Called when a schedule is created (schedule has
// highest priority). The reconciler will converge MediaMTX state on the next
// tick. No-op if no manual session exists.
func (s *service) CloseActiveManualSession(cameraID string) error {
	sess, err := s.repo.FindActiveManualSessionByCamera(cameraID)
	if err != nil || sess == nil {
		return nil
	}
	if err := s.repo.CloseSession(sess.ID, time.Now()); err != nil {
		logger.Errorf("close active manual session: camera %s: %v", cameraID, err)
		return apperror.Wrap(err, 50000, 500, "failed to close manual session")
	}
	logger.Infof("closed active manual session %s for camera %s (schedule priority)", sess.ID, cameraID)
	return nil
}

// Upsert creates or updates a recording record (called by file scanner).
// Uses ON DUPLICATE KEY UPDATE for atomicity.
// If rec.SessionID is unset, attempts to link to the originating session by
// (camera_id, start_time) interval. Failure to find a session is non-fatal
// (legacy files or files produced before sessions existed).
func (s *service) Upsert(ctx context.Context, rec *model.Recording) error {
	if rec.ID == "" {
		rec.ID = uuid.NewString()
	}
	now := time.Now()
	rec.CreatedAt = now
	rec.UpdatedAt = now

	if rec.SessionID == nil || *rec.SessionID == "" {
		if sess, err := s.repo.FindSessionByCameraAndTime(rec.CameraID, rec.StartTime); err == nil && sess != nil {
			sid := sess.ID
			rec.SessionID = &sid
		}
	}

	return s.repo.Upsert(rec)
}
