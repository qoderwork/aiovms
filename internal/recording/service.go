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
	Get(ctx context.Context, id string) (*model.Recording, string, error)
	Delete(ctx context.Context, id string) error
	StartManual(ctx context.Context, cameraID string) error
	StopManual(ctx context.Context, cameraID string) error
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
	Get(ctx context.Context, id string) (*model.Camera, error)
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

func (s *service) Get(ctx context.Context, id string) (*model.Recording, string, error) {
	rec, err := s.repo.FindByID(id)
	if err != nil {
		return nil, "", apperror.ErrRecordingNotFound
	}
	// Playback URL served by Nginx from /recordings/ path
	playURL := fmt.Sprintf("/recordings/%s/%s", rec.CameraID, rec.Filename)
	return rec, playURL, nil
}

func (s *service) Delete(ctx context.Context, id string) error {
	rec, err := s.repo.FindByID(id)
	if err != nil {
		return apperror.ErrRecordingNotFound
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

func (s *service) StartManual(ctx context.Context, cameraID string) error {
	cam, err := s.camSvc.Get(ctx, cameraID)
	if err != nil {
		return apperror.ErrCameraNotFound
	}
	return s.mtx.PatchPath(cam.MediaMTXPath, map[string]any{"record": true})
}

func (s *service) StopManual(ctx context.Context, cameraID string) error {
	cam, err := s.camSvc.Get(ctx, cameraID)
	if err != nil {
		return apperror.ErrCameraNotFound
	}
	return s.mtx.PatchPath(cam.MediaMTXPath, map[string]any{"record": false})
}

// Upsert creates or updates a recording record (called by file scanner).
// Uses ON DUPLICATE KEY UPDATE for atomicity.
func (s *service) Upsert(ctx context.Context, rec *model.Recording) error {
	if rec.ID == "" {
		rec.ID = uuid.NewString()
	}
	now := time.Now()
	rec.CreatedAt = now
	rec.UpdatedAt = now
	return s.repo.Upsert(rec)
}
