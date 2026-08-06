package camera

import (
	"context"
	"net"
	"strconv"
	"sync"
	"time"

	"aiovms/pkg/logger"
)

const (
	statusInterval = 30 * time.Second
)

// mtxHealthChecker is the minimal MediaMTX health API needed by StatusChecker.
type mtxHealthChecker interface {
	HealthCheck() error
}

// RecoveryHook is invoked once when MediaMTX transitions from down→up.
// The recording service registers its RecoverRecording method here.
type RecoveryHook func(ctx context.Context)

// StatusChecker periodically pings camera IPs and updates their online/offline status.
// It also polls MediaMTX health; on a down→up transition it triggers the registered
// RecoveryHook so the recording service can re-apply record:true for active sessions.
// NMS queries camera status from DB directly.
type StatusChecker struct {
	repo     Repository
	mtx      mtxHealthChecker
	recover  RecoveryHook
	stopCh   chan struct{}
	stopOnce sync.Once

	// mtxDown tracks the last observed MediaMTX state to detect transitions.
	mtxDown bool
}

func NewStatusChecker(repo Repository) *StatusChecker {
	return &StatusChecker{
		repo:   repo,
		stopCh: make(chan struct{}),
	}
}

// WithMediaMTXHealth enables MediaMTX health monitoring on this StatusChecker.
// When MTX transitions from down→up, the recover hook is invoked once.
func (sc *StatusChecker) WithMediaMTXHealth(mtx mtxHealthChecker, recover RecoveryHook) *StatusChecker {
	sc.mtx = mtx
	sc.recover = recover
	return sc
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
			sc.checkMediaMTX()
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

// checkMediaMTX probes MediaMTX health and fires the recovery hook on down→up.
// Safe to call when MTX monitoring is not configured (no-op).
func (sc *StatusChecker) checkMediaMTX() {
	if sc.mtx == nil || sc.recover == nil {
		return
	}
	err := sc.mtx.HealthCheck()
	nowDown := err != nil

	if nowDown && !sc.mtxDown {
		logger.Warnf("status checker: mediamtx went down: %v", err)
	}
	if !nowDown && sc.mtxDown {
		logger.Info("status checker: mediamtx recovered, triggering recording recovery")
		sc.recover(context.Background())
	}
	sc.mtxDown = nowDown
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
