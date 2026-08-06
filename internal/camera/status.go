package camera

import (
	"context"
	"encoding/json"
	"net"
	"strconv"
	"sync"
	"time"

	"aiovms/pkg/logger"
	pkgredis "aiovms/pkg/redis"
)

const (
	statusChannel  = "vms:camera:status"
	statusInterval = 30 * time.Second
)

// StatusMessage is published to Redis when camera status changes.
type StatusMessage struct {
	CameraID  string `json:"camera_id"`
	LicenseID int64  `json:"license_id"`
	OldStatus string `json:"old_status"`
	NewStatus string `json:"new_status"`
	Timestamp string `json:"timestamp"`
}

// StatusChecker periodically pings camera IPs and updates their online/offline status.
// Status changes are published to Redis for Java NMS to consume.
type StatusChecker struct {
	repo     Repository
	stopCh   chan struct{}
	stopOnce sync.Once
}

func NewStatusChecker(repo Repository) *StatusChecker {
	return &StatusChecker{
		repo:   repo,
		stopCh: make(chan struct{}),
	}
}

func (sc *StatusChecker) Run() {
	logger.Infof("camera status checker started (interval=%s)", statusInterval)
	ticker := time.NewTicker(statusInterval)
	defer ticker.Stop()

	sc.checkAll()

	for {
		select {
		case <-ticker.C:
			sc.checkAll()
		case <-sc.stopCh:
			logger.Info("camera status checker stopped")
			return
		}
	}
}

func (sc *StatusChecker) Stop() {
	sc.stopOnce.Do(func() { close(sc.stopCh) })
}

func (sc *StatusChecker) checkAll() {
	cams, err := sc.repo.FindAll()
	if err != nil {
		logger.Errorf("status checker: fetch cameras: %v", err)
		return
	}

	for _, cam := range cams {
		oldStatus := cam.Status
		newStatus := probeCamera(cam.IP, cam.Port)

		if oldStatus != newStatus {
			logger.Infof("camera %s status changed: %s → %s", cam.ID, oldStatus, newStatus)
			_ = sc.repo.UpdateStatus(cam.ID, newStatus)

			// Publish to Redis
			sc.publish(cam.ID, cam.LicenseID, oldStatus, newStatus)
		}
	}
}

func (sc *StatusChecker) publish(cameraID string, licenseID int64, oldStatus, newStatus string) {
	msg := StatusMessage{
		CameraID:  cameraID,
		LicenseID: licenseID,
		OldStatus: oldStatus,
		NewStatus: newStatus,
		Timestamp: time.Now().UTC().Format(time.RFC3339),
	}
	payload, _ := json.Marshal(msg)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	if err := pkgredis.Publish(ctx, statusChannel, string(payload)); err != nil {
		logger.Debugf("status checker: redis publish failed: %v", err)
	}
}

// probeCamera checks if a camera is reachable via TCP on its RTSP port.
// Returns "online" or "offline".
func probeCamera(ip string, port int) string {
	if port <= 0 {
		port = 554
	}
	addr := net.JoinHostPort(ip, strconv.Itoa(port))
	conn, err := net.DialTimeout("tcp", addr, 3*time.Second)
	if err != nil {
		return "offline"
	}
	conn.Close()
	return "online"
}
