package recording

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/google/uuid"

	"aiovms/internal/mediamtx"
	"aiovms/internal/model"
	"aiovms/pkg/apperror"
	"aiovms/pkg/logger"
)

type Service interface {
	List(ctx context.Context, tenantID int64, cameraID, startTime, endTime string, page, pageSize int) ([]model.Recording, int64, error)
	Get(ctx context.Context, tenantID int64, id string) (*model.Recording, string, error)
	Delete(ctx context.Context, tenantID int64, id string) error
	StartManual(ctx context.Context, tenantID int64, cameraID string) error
	StopManual(ctx context.Context, tenantID int64, cameraID string) error
	Upsert(ctx context.Context, rec *model.Recording) error
}

// mediaMTXClient abstracts MediaMTX API for testability.
type mediaMTXClient interface {
	PatchPath(name string, patch map[string]any) error
}

type service struct {
	repo   Repository
	camSvc CameraService
	mtx    mediaMTXClient
}

type CameraService interface {
	Get(ctx context.Context, tenantID int64, id string) (*model.Camera, error)
}

func NewService(repo Repository, camSvc CameraService, mtx *mediamtx.Client) Service {
	return &service{repo: repo, camSvc: camSvc, mtx: mtx}
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
	// Playback URL served by Nginx from /recordings/ path.
	// Uses MediaMTXPath (e.g. "cam-a1b2c3d4") which matches the physical directory
	// structure, NOT the full camera UUID.
	playURL := fmt.Sprintf("/recordings/%s/%s", rec.MediaMTXPath, rec.Filename)
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

func (s *service) StartManual(ctx context.Context, tenantID int64, cameraID string) error {
	cam, err := s.camSvc.Get(ctx, tenantID, cameraID)
	if err != nil {
		return err
	}

	// Close any pre-existing active session for this camera (defensive: should not happen
	// in normal flow, but guards against duplicate sessions if a previous Stop failed).
	if existing, err := s.repo.FindActiveSessionByCamera(cameraID); err == nil && existing != nil {
		logger.Warnf("start manual: found stale active session %s for camera %s, closing", existing.ID, cameraID)
		_ = s.repo.CloseSession(existing.ID, time.Now())
	}

	// 1. Patch MediaMTX first: if it fails, we don't create a dangling session.
//    注意：一期录像始终使用主码流（cam.MediaMTXPath），stream_type 暂不生效。
//    同时关闭 sourceOnDemand，确保 MediaMTX 主动拉流录像，不依赖有人预览。
	if err := s.mtx.PatchPath(cam.MediaMTXPath, map[string]any{
		"record":          true,
		"sourceOnDemand":  false,
	}); err != nil {
		return err
	}

	// 2. Create session record (end_time IS NULL = active).
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
		// Best-effort rollback: disable recording to avoid a recording without a session.
		logger.Errorf("start manual: create session for camera %s: %v (rolling back mediamtx)", cameraID, err)
		_ = s.mtx.PatchPath(cam.MediaMTXPath, map[string]any{
			"record":         false,
			"sourceOnDemand": true,
		})
		return apperror.Wrap(err, 50000, 500, "failed to create recording session")
	}
	return nil
}

func (s *service) StopManual(ctx context.Context, tenantID int64, cameraID string) error {
	cam, err := s.camSvc.Get(ctx, tenantID, cameraID)
	if err != nil {
		return err
	}

	// 1. Close the active session FIRST. The session is the source of truth for
	//    desired recording state: while it is open, the reconciler's drift recovery
	//    re-applies record:true every cycle. Closing it before stopping MediaMTX
	//    removes the window where Stop patches record:false but the reconciler
	//    immediately flips it back to true (stop silently undone).
	sess, sessErr := s.repo.FindActiveSessionByCamera(cameraID)
	if sessErr == nil && sess != nil {
		if err := s.repo.CloseSession(sess.ID, time.Now()); err != nil {
			logger.Errorf("stop manual: close session %s for camera %s: %v", sess.ID, cameraID, err)
			return apperror.Wrap(err, 50000, 500, "failed to close recording session")
		}
	}
	// No active session — MediaMTX may still be recording (e.g. previous stop
	// crashed after closing the session); fall through to patch anyway (idempotent).

	// 2. Stop MediaMTX recording (idempotent: safe even if not recording).
	//    恢复 sourceOnDemand=true，无人预览时停止拉流节省资源。
	if err := s.mtx.PatchPath(cam.MediaMTXPath, map[string]any{
		"record":         false,
		"sourceOnDemand": true,
	}); err != nil {
		// Session already closed. If the patch fails, the reconciler's orphan-record
		// repair will stop recording on a later cycle, so no retry loop is needed here.
		return err
	}
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
