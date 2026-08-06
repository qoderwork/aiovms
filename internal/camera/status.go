package camera

import (
	"net"
	"strconv"
	"sync"
	"time"

	"aiovms/pkg/logger"
)

const (
	statusInterval = 30 * time.Second
)

// StatusChecker periodically pings camera IPs and updates their online/offline status.
// NMS queries camera status from DB directly.
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
		}
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
