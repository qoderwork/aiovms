package camera

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"time"

	"github.com/google/uuid"

	"aiovms/internal/mediamtx"
	"aiovms/internal/model"
	"aiovms/internal/onvif"
	"aiovms/pkg/apperror"
	"aiovms/pkg/crypto"
	"aiovms/pkg/logger"
)

type Service interface {
	List(ctx context.Context, tenantID int64, query string, page, pageSize int) ([]model.Camera, int64, error)
	Create(ctx context.Context, cam *model.Camera) error
	Get(ctx context.Context, id string) (*model.Camera, error)
	Update(ctx context.Context, id string, cam *model.Camera) error
	Delete(ctx context.Context, id string) error
	Connect(ctx context.Context, id string) error
	Disconnect(ctx context.Context, id string) error
	GetStreamURLs(ctx context.Context, id string) (*StreamURLs, error)
	UpdateStatus(ctx context.Context, id string, status string) error
	Discover(ctx context.Context, timeoutSec int) ([]onvif.DiscoveredDevice, error)
	Snapshot(ctx context.Context, id string) (*SnapshotResult, error)
	ListStatuses(ctx context.Context) ([]CameraStatus, error)
	DeleteAll(ctx context.Context, tenantID int64) (int64, error)
}

// CameraStatus is the lightweight status view for NMS subscription.
type CameraStatus struct {
	CameraID  string `json:"camera_id"`
	Status    string `json:"status"`
	LicenseID int64  `json:"license_id"`
}

type SnapshotResult struct {
	CameraID string `json:"camera_id"`
	ImageURL string `json:"image_url"`
}

// mediaMTXClient abstracts MediaMTX HTTP API for testability.
type mediaMTXClient interface {
	AddPath(name string, cfg mediamtx.PathConfig) error
	DeletePath(name string) error
	SnapshotPath(name string) string
}

type service struct {
	repo Repository
	mtx  mediaMTXClient
}

func NewService(repo Repository, mtx *mediamtx.Client) Service {
	return &service{repo: repo, mtx: mtx}
}


type StreamURLs struct {
	FLV    string `json:"flv"`
	HLS    string `json:"hls"`
	WebRTC string `json:"webrtc"`
}

func (s *service) List(ctx context.Context, tenantID int64, query string, page, pageSize int) ([]model.Camera, int64, error) {
	offset := (page - 1) * pageSize
	if offset < 0 {
		offset = 0
	}
	return s.repo.ListByTenant(tenantID, query, offset, pageSize)
}

func (s *service) Create(ctx context.Context, cam *model.Camera) error {
	if err := validateCamera(cam); err != nil {
		return err
	}

	if exists, err := s.repo.ExistsByName(cam.LicenseID, cam.Name, ""); err != nil {
		return apperror.Wrap(err, 50000, 500, "failed to check camera duplicate")
	} else if exists {
		return apperror.ErrConflict.WithMessage("camera name already exists")
	}

	if exists, err := s.repo.ExistsByIPPort(cam.LicenseID, cam.IP, cam.Port, ""); err != nil {
		return apperror.Wrap(err, 50000, 500, "failed to check camera duplicate")
	} else if exists {
		return apperror.ErrConflict.WithMessage("camera with same IP and port already exists")
	}

	if exists, err := s.repo.ExistsByStreamURL(cam.LicenseID, cam.StreamURL, ""); err != nil {
		return apperror.Wrap(err, 50000, 500, "failed to check camera duplicate")
	} else if exists {
		return apperror.ErrConflict.WithMessage("camera stream_url already exists")
	}

	cam.ID = uuid.NewString()
	cam.MediaMTXPath = fmt.Sprintf("cam-%s", cam.ID[:8])

	// Encrypt password if provided
	if cam.Password != "" {
		enc, err := crypto.Encrypt(cam.Password)
		if err != nil {
			return apperror.Wrap(err, 50000, 500, "failed to encrypt password")
		}
		cam.PasswordEnc = enc
		cam.Password = "" // clear plaintext
	}

	// VMS trusts license_id/site_id from NMS (validated upstream).
	// Initial status is "connecting" until StatusChecker probes online.
	cam.Status = "connecting"
	cam.CreatedAt = time.Now()
	cam.UpdatedAt = time.Now()

	if err := s.repo.Create(cam); err != nil {
		return apperror.Wrap(err, 50000, 500, "failed to create camera")
	}

	if err := s.mtx.AddPath(cam.MediaMTXPath, mediamtx.PathConfig{
		Source:         cam.StreamURL,
		SourceOnDemand: true,
	}); err != nil {
		logger.Errorf("mediamtx register failed for camera %s: %v", cam.ID, err)
		_ = s.repo.UpdateStatus(cam.ID, "error")
		return apperror.Wrap(err, 50301, 503, "failed to register camera to mediamtx")
	}

	return nil
}

func (s *service) Get(ctx context.Context, id string) (*model.Camera, error) {
	cam, err := s.repo.FindByID(id)
	if err != nil {
		return nil, apperror.ErrCameraNotFound
	}
	return cam, nil
}

func (s *service) Update(ctx context.Context, id string, cam *model.Camera) error {
	if err := validateCamera(cam); err != nil {
		return err
	}

	existing, err := s.repo.FindByID(id)
	if err != nil {
		return apperror.ErrCameraNotFound
	}

	if exists, err := s.repo.ExistsByName(existing.LicenseID, cam.Name, id); err != nil {
		return apperror.Wrap(err, 50000, 500, "failed to check camera duplicate")
	} else if exists {
		return apperror.ErrConflict.WithMessage("camera name already exists")
	}

	if exists, err := s.repo.ExistsByIPPort(existing.LicenseID, cam.IP, cam.Port, id); err != nil {
		return apperror.Wrap(err, 50000, 500, "failed to check camera duplicate")
	} else if exists {
		return apperror.ErrConflict.WithMessage("camera with same IP and port already exists")
	}

	if exists, err := s.repo.ExistsByStreamURL(existing.LicenseID, cam.StreamURL, id); err != nil {
		return apperror.Wrap(err, 50000, 500, "failed to check camera duplicate")
	} else if exists {
		return apperror.ErrConflict.WithMessage("camera stream_url already exists")
	}

	existing.Name = cam.Name
	existing.IP = cam.IP
	existing.Port = cam.Port
	existing.Username = cam.Username
	if cam.Password != "" {
		// Re-encrypt when a new plaintext password is provided; otherwise keep existing.
		enc, err := crypto.Encrypt(cam.Password)
		if err != nil {
			return apperror.Wrap(err, 50000, 500, "failed to encrypt password")
		}
		existing.PasswordEnc = enc
	}
	existing.StreamURL = cam.StreamURL
	existing.SubStreamURL = cam.SubStreamURL
	existing.UpdatedAt = time.Now()

	if err := s.repo.Update(existing); err != nil {
		return apperror.Wrap(err, 50000, 500, "failed to update camera")
	}

	if err := s.mtx.AddPath(existing.MediaMTXPath, mediamtx.PathConfig{
		Source:         existing.StreamURL,
		SourceOnDemand: true,
	}); err != nil {
		logger.Errorf("mediamtx re-register failed for camera %s: %v", id, err)
		return apperror.Wrap(err, 50301, 503, "failed to re-register camera to mediamtx")
	}

	return nil
}

func (s *service) Delete(ctx context.Context, id string) error {
	cam, err := s.repo.FindByID(id)
	if err != nil {
		return apperror.ErrCameraNotFound
	}
	_ = s.mtx.DeletePath(cam.MediaMTXPath)
	return s.repo.Delete(id)
}

func (s *service) DeleteAll(ctx context.Context, tenantID int64) (int64, error) {
	cams, err := s.repo.FindAllByTenant(tenantID)
	if err != nil {
		return 0, apperror.Wrap(err, 50000, 500, "failed to list cameras for deletion")
	}
	// Best-effort delete MediaMTX paths.
	for _, cam := range cams {
		_ = s.mtx.DeletePath(cam.MediaMTXPath)
	}
	count, err := s.repo.DeleteAllByTenant(tenantID)
	if err != nil {
		return 0, apperror.Wrap(err, 50000, 500, "failed to delete cameras")
	}
	return count, nil
}

func (s *service) Connect(ctx context.Context, id string) error {
	cam, err := s.repo.FindByID(id)
	if err != nil {
		return apperror.ErrCameraNotFound
	}
	if err := s.mtx.AddPath(cam.MediaMTXPath, mediamtx.PathConfig{
		Source:         cam.StreamURL,
		SourceOnDemand: true,
	}); err != nil {
		return err
	}
	// Mark as connecting; StatusChecker will probe to online/offline.
	return s.repo.UpdateStatus(id, "connecting")
}

func (s *service) Disconnect(ctx context.Context, id string) error {
	cam, err := s.repo.FindByID(id)
	if err != nil {
		return apperror.ErrCameraNotFound
	}
	if err := s.mtx.DeletePath(cam.MediaMTXPath); err != nil {
		return err
	}
	// Mark as disconnected (manually detached, not probed offline).
	return s.repo.UpdateStatus(id, "disconnected")
}

func (s *service) GetStreamURLs(ctx context.Context, id string) (*StreamURLs, error) {
	cam, err := s.repo.FindByID(id)
	if err != nil {
		return nil, apperror.ErrCameraNotFound
	}
	return &StreamURLs{
		FLV:    fmt.Sprintf("/live/%s/stream.flv", cam.MediaMTXPath),
		HLS:    fmt.Sprintf("/stream/%s/index.m3u8", cam.MediaMTXPath),
		WebRTC: fmt.Sprintf("/stream/%s/", cam.MediaMTXPath),
	}, nil
}

func (s *service) UpdateStatus(ctx context.Context, id string, status string) error {
	return s.repo.UpdateStatus(id, status)
}

func (s *service) Discover(ctx context.Context, timeoutSec int) ([]onvif.DiscoveredDevice, error) {
	if timeoutSec <= 0 {
		timeoutSec = 5
	}
	return onvif.NewDiscoveryService().Discover(ctx, timeoutSec)
}

func (s *service) Snapshot(ctx context.Context, id string) (*SnapshotResult, error) {
	cam, err := s.repo.FindByID(id)
	if err != nil {
		return nil, apperror.ErrCameraNotFound
	}
	return &SnapshotResult{
		CameraID: cam.ID,
		ImageURL: s.mtx.SnapshotPath(cam.MediaMTXPath),
	}, nil
}

// ListStatuses returns lightweight status view of all cameras for NMS polling.
func (s *service) ListStatuses(ctx context.Context) ([]CameraStatus, error) {
	cams, err := s.repo.FindAll()
	if err != nil {
		return nil, err
	}
	out := make([]CameraStatus, 0, len(cams))
	for _, cam := range cams {
		out = append(out, CameraStatus{
			CameraID:  cam.ID,
			Status:    cam.Status,
			LicenseID: cam.LicenseID,
		})
	}
	return out, nil
}

// validateCamera validates required fields and formats for camera create/update.
func validateCamera(cam *model.Camera) error {
	if cam.Name == "" {
		return apperror.ErrInvalidInput.WithMessage("name is required")
	}
	if cam.IP == "" {
		return apperror.ErrInvalidInput.WithMessage("ip is required")
	}
	if net.ParseIP(cam.IP) == nil {
		return apperror.ErrInvalidInput.WithMessage("invalid ip format: " + cam.IP)
	}
	if cam.Port < 1 || cam.Port > 65535 {
		return apperror.ErrInvalidInput.WithMessage("port must be 1-65535")
	}
	if cam.Protocol != "RTSP" && cam.Protocol != "ONVIF" {
		return apperror.ErrInvalidInput.WithMessage("protocol must be RTSP or ONVIF")
	}
	if cam.StreamURL == "" {
		return apperror.ErrInvalidInput.WithMessage("stream_url is required")
	}
	if _, err := url.Parse(cam.StreamURL); err != nil {
		return apperror.ErrInvalidInput.WithMessage("invalid stream_url format")
	}
	return nil
}
