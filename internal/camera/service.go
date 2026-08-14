package camera

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"sync"
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
	Get(ctx context.Context, tenantID int64, id string) (*model.Camera, error)
	Update(ctx context.Context, tenantID int64, id string, cam *model.Camera) error
	Delete(ctx context.Context, tenantID int64, id string) error
	Connect(ctx context.Context, tenantID int64, id string) error
	Disconnect(ctx context.Context, tenantID int64, id string) error
	GetStreamURLs(ctx context.Context, tenantID int64, id string) (*StreamURLs, error)
	UpdateStatus(ctx context.Context, id string, status string) error
	Discover(ctx context.Context, interfaceName string, timeoutSec int) ([]onvif.DiscoveredDevice, error)
	ProbeONVIF(ctx context.Context, ip string, port int, username, password string) (*onvif.DiscoveredDevice, error)
	ScanONVIF(ctx context.Context, cidr string, port int, username, password string, timeoutSec int) ([]onvif.DiscoveredDevice, error)
	Snapshot(ctx context.Context, tenantID int64, id string) (*SnapshotResult, error)
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

// cameraActuator abstracts the MediaMTX actuator (single writer) for
// testability: path lifecycle mutations go through it, serially and retried.
type cameraActuator interface {
	EnsurePath(name string, cfg mediamtx.PathConfig) error
	DeletePath(name string) error
}

// snapshotter builds snapshot URLs (pure function, no MediaMTX mutation).
type snapshotter interface {
	SnapshotPath(name string) string
}

type service struct {
	repo            Repository
	act             cameraActuator
	snap            snapshotter
	recordPath      string
	segmentDuration string
	hookCommand     string
}

func NewService(repo Repository, act cameraActuator, snap snapshotter, recordPath, segmentDuration, hookCommand string) Service {
	return &service{repo: repo, act: act, snap: snap, recordPath: recordPath, segmentDuration: segmentDuration, hookCommand: hookCommand}
}

// getForTenant loads a camera and enforces tenant isolation.
// Returns ErrCameraNotFound when the camera does not exist, and ErrForbidden
// when it exists but belongs to another tenant (design doc: 越权返回 403).
func (s *service) getForTenant(tenantID int64, id string) (*model.Camera, error) {
	cam, err := s.repo.FindByIDAndTenant(id, tenantID)
	if err == nil {
		return cam, nil
	}
	// Distinguish "not found" from "belongs to another tenant".
	if _, err := s.repo.FindByID(id); err == nil {
		return nil, apperror.ErrForbidden.WithMessage("camera belongs to another tenant")
	}
	return nil, apperror.ErrCameraNotFound
}

// buildPathConfig constructs the self-contained PathConfig for a camera.
// 显式下发 recordPath 和 recordSegmentDuration，避免依赖 mediamtx.yml 的 all_others 继承
// （显式 add 的命名路径不会继承 all_others，会用 setDefaults 硬编码默认值）。
// hookCommand 为空时该字段被 omitempty 省略，即不启用分片完成回调。
// Source 会通过 model.Camera.SourceURL 注入 RTSP 认证凭据。
func (s *service) buildPathConfig(cam *model.Camera) (mediamtx.PathConfig, error) {
	source, err := cam.SourceURL()
	if err != nil {
		return mediamtx.PathConfig{}, apperror.Wrap(err, 50000, 500, "failed to build camera source URL")
	}
	return mediamtx.PathConfig{
		Source:                     source,
		SourceOnDemand:             true,
		RecordPath:                 s.recordPath + "/%path/%Y-%m-%d_%H-%M-%S",
		RecordSegmentDuration:      s.segmentDuration,
		RunOnRecordSegmentComplete: s.hookCommand,
	}, nil
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

	// 一期仅注册主码流（stream_url）到 MediaMTX；sub_stream_url 暂未注册，二期实现子码流预览/录制时启用。
	cfg, err := s.buildPathConfig(cam)
	if err != nil {
		return err
	}
	if err := s.act.EnsurePath(cam.MediaMTXPath, cfg); err != nil {
		logger.Errorf("mediamtx register failed for camera %s: %v", cam.ID, err)
		_ = s.repo.UpdateStatus(cam.ID, "error")
		return apperror.Wrap(err, 50301, 503, "failed to register camera to mediamtx")
	}

	return nil
}

func (s *service) Get(ctx context.Context, tenantID int64, id string) (*model.Camera, error) {
	return s.getForTenant(tenantID, id)
}

func (s *service) Update(ctx context.Context, tenantID int64, id string, cam *model.Camera) error {
	if err := validateCamera(cam); err != nil {
		return err
	}

	existing, err := s.getForTenant(tenantID, id)
	if err != nil {
		return err
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
	existing.Protocol = cam.Protocol
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
	// Copy descriptive fields that were previously silently dropped.
	existing.Manufacturer = cam.Manufacturer
	existing.Model = cam.Model
	existing.SiteID = cam.SiteID
	existing.Latitude = cam.Latitude
	existing.Longitude = cam.Longitude
	existing.UpdatedAt = time.Now()

	if err := s.repo.Update(existing); err != nil {
		return apperror.Wrap(err, 50000, 500, "failed to update camera")
	}

	// 重新注册主码流（一期不注册子码流）
	cfg, err := s.buildPathConfig(existing)
	if err != nil {
		return err
	}
	if err := s.act.EnsurePath(existing.MediaMTXPath, cfg); err != nil {
		logger.Errorf("mediamtx re-register failed for camera %s: %v", id, err)
		return apperror.Wrap(err, 50301, 503, "failed to re-register camera to mediamtx")
	}

	return nil
}

func (s *service) Delete(ctx context.Context, tenantID int64, id string) error {
	cam, err := s.getForTenant(tenantID, id)
	if err != nil {
		return err
	}
	// Path removal is enqueued best-effort; if it fails, the reconciler's
	// orphan-path cleanup removes the stale path on a later cycle.
	_ = s.act.DeletePath(cam.MediaMTXPath)
	return s.repo.Delete(id)
}

func (s *service) DeleteAll(ctx context.Context, tenantID int64) (int64, error) {
	cams, err := s.repo.FindAllByTenant(tenantID)
	if err != nil {
		return 0, apperror.Wrap(err, 50000, 500, "failed to list cameras for deletion")
	}
	// Best-effort delete MediaMTX paths.
	for _, cam := range cams {
		_ = s.act.DeletePath(cam.MediaMTXPath)
	}
	count, err := s.repo.DeleteAllByTenant(tenantID)
	if err != nil {
		return 0, apperror.Wrap(err, 50000, 500, "failed to delete cameras")
	}
	return count, nil
}

func (s *service) Connect(ctx context.Context, tenantID int64, id string) error {
	cam, err := s.getForTenant(tenantID, id)
	if err != nil {
		return err
	}
	cfg, err := s.buildPathConfig(cam)
	if err != nil {
		return err
	}
	if err := s.act.EnsurePath(cam.MediaMTXPath, cfg); err != nil {
		return err
	}
	// Mark as connecting; StatusChecker will probe to online/offline.
	return s.repo.UpdateStatus(id, "connecting")
}

func (s *service) Disconnect(ctx context.Context, tenantID int64, id string) error {
	cam, err := s.getForTenant(tenantID, id)
	if err != nil {
		return err
	}
	if err := s.act.DeletePath(cam.MediaMTXPath); err != nil {
		return err
	}
	// Mark as disconnected (manually detached, not probed offline).
	return s.repo.UpdateStatus(id, "disconnected")
}

func (s *service) GetStreamURLs(ctx context.Context, tenantID int64, id string) (*StreamURLs, error) {
	cam, err := s.getForTenant(tenantID, id)
	if err != nil {
		return nil, err
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

func (s *service) Discover(ctx context.Context, interfaceName string, timeoutSec int) ([]onvif.DiscoveredDevice, error) {
	if timeoutSec <= 0 {
		timeoutSec = 5
	}
	return onvif.NewDiscoveryService().Discover(ctx, interfaceName, timeoutSec)
}

// ProbeONVIF directly connects to a camera via ONVIF unicast (no multicast),
// retrieving device info and the primary stream URL. Suitable for Docker
// deployments where WS-Discovery multicast does not cross the bridge network.
func (s *service) ProbeONVIF(ctx context.Context, ip string, port int, username, password string) (*onvif.DiscoveredDevice, error) {
	dev, err := onvif.NewDiscoveryService().ProbeDevice(ctx, ip, port, username, password)
	if err != nil {
		return nil, apperror.ErrInvalidInput.WithMessage("failed to connect ONVIF device: " + err.Error())
	}
	return dev, nil
}

// ScanONVIF scans an entire CIDR network range by unicast-probing each IP.
// Suitable for Docker deployments where WS-Discovery multicast is unavailable.
// Uses a worker pool (max 50 concurrent) to scan in parallel.
func (s *service) ScanONVIF(ctx context.Context, cidr string, port int, username, password string, timeoutSec int) ([]onvif.DiscoveredDevice, error) {
	ips, err := parseCIDR(cidr)
	if err != nil {
		return nil, apperror.ErrInvalidInput.WithMessage("invalid cidr: " + err.Error())
	}
	if port == 0 {
		port = 80
	}
	if timeoutSec <= 0 {
		timeoutSec = 10
	}

	type result struct {
		dev *onvif.DiscoveredDevice
		err error
		ip  string
	}

	var wg sync.WaitGroup
	workers := 50
	if len(ips) < workers {
		workers = len(ips)
	}
	ipCh := make(chan string, len(ips))
	resultCh := make(chan result, len(ips))

	// Producer
	for _, ip := range ips {
		ipCh <- ip
	}
	close(ipCh)

	// Consumers
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			svc := onvif.NewDiscoveryService()
			for ip := range ipCh {
				if ctx.Err() != nil {
					return
				}
				probeCtx, cancel := context.WithTimeout(ctx, time.Duration(timeoutSec)*time.Second)
				dev, err := svc.ProbeDevice(probeCtx, ip, port, username, password)
				cancel()
				if err == nil && dev != nil {
					resultCh <- result{dev: dev, ip: ip}
				} else {
					resultCh <- result{err: err, ip: ip}
				}
			}
		}()
	}

	wg.Wait()
	close(resultCh)

	var devices []onvif.DiscoveredDevice
	for r := range resultCh {
		if r.dev != nil {
			devices = append(devices, *r.dev)
		}
	}
	return devices, nil
}

// parseCIDR parses a CIDR string (e.g. "172.16.2.0/24") and returns all host IPs.
func parseCIDR(cidr string) ([]string, error) {
	ip, ipnet, err := net.ParseCIDR(cidr)
	if err != nil {
		return nil, err
	}
	var ips []string
	for ip := ip.Mask(ipnet.Mask); ipnet.Contains(ip); incIP(ip) {
		ips = append(ips, ip.String())
	}
	// Remove network and broadcast addresses for /24 and smaller
	if len(ips) > 2 {
		ips = ips[1 : len(ips)-1]
	}
	return ips, nil
}

func incIP(ip net.IP) {
	for j := len(ip) - 1; j >= 0; j-- {
		ip[j]++
		if ip[j] > 0 {
			break
		}
	}
}

func (s *service) Snapshot(ctx context.Context, tenantID int64, id string) (*SnapshotResult, error) {
	cam, err := s.getForTenant(tenantID, id)
	if err != nil {
		return nil, err
	}
	return &SnapshotResult{
		CameraID: cam.ID,
		ImageURL: s.snap.SnapshotPath(cam.MediaMTXPath),
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
	u, err := url.Parse(cam.StreamURL)
	if err != nil || u.Scheme != "rtsp" || u.Host == "" || u.Path == "" || u.Path == "/" {
		return apperror.ErrStreamURLInvalid.WithMessage("stream_url must be a valid rtsp://host[:port]/path URL with a non-empty path")
	}
	return nil
}
