package controller

import (
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"aiovms/internal/mediamtx"
	"aiovms/internal/model"
	"aiovms/pkg/logger"
	"aiovms/pkg/metrics"
)

// ---------------------------------------------------------------------------
// Local interfaces (ISP: only the methods the reconciler needs)
// ---------------------------------------------------------------------------

type CameraRepo interface {
	FindAll() ([]model.Camera, error)
	FindByID(id string) (*model.Camera, error)
	UpdateStatus(id string, status string) error
}

type ScheduleRepo interface {
	FindAllEnabled() ([]model.RecordSchedule, error)
	Update(sch *model.RecordSchedule) error
}

type RecordingRepo interface {
	FindActiveSessions() ([]model.RecordingSession, error)
	FindActiveSessionByCamera(cameraID string) (*model.RecordingSession, error)
	FindActiveSessionBySchedule(scheduleID string) (*model.RecordingSession, error)
	CreateSession(sess *model.RecordingSession) error
	CloseSession(id string, endTime time.Time) error
}

// MTXReader is the read-only MediaMTX surface the reconciler uses to observe
// actual state. Reads never mutate, so they bypass the actuator.
type MTXReader interface {
	ListPathConfigs() (map[string]mediamtx.PathConfigItem, error)
	HealthCheck() error
}

// MTXActuator is the write surface: every mutation is enqueued to the
// actuator (single writer, serial execution, retried). The reconciler never
// blocks on MediaMTX availability — all methods are fire-and-forget.
type MTXActuator interface {
	EnqueueEnsurePath(name string, cfg mediamtx.PathConfig)
	EnqueueDeletePath(name string)
	EnqueueSetRecord(path string, on bool)
}

// ---------------------------------------------------------------------------
// Reconciler
// ---------------------------------------------------------------------------

const (
	reconcileInterval = 10 * time.Second
	statusProbeEvery  = 3 // probe camera status every 3 ticks (30s)
	probeTimeout      = 3 * time.Second
	// orphanRecordConfirmTicks is the number of consecutive reconcile cycles an
	// "orphan" recording state (MediaMTX record=true but no active session and
	// no in-window schedule) must be observed before the reconciler forces it
	// off. The hysteresis avoids fighting with the brief gap in start flows
	// where record:true is patched a few milliseconds before the session row
	// is created.
	orphanRecordConfirmTicks = 2
)

// Reconciler is the unified control loop that replaces the former 3 separate
// sync mechanisms (startup sync, event-driven MTX recovery, 60s cron triggerJob).
//
// It runs every 10 seconds and performs three idempotent sub-reconciliations:
//  1. reconcileStreams  — ensure MediaMTX paths match DB (create missing, fix drift, remove orphans)
//  2. reconcileRecording — ensure recording state matches schedules + active sessions
//  3. reconcileCameraStatus — TCP-probe cameras for online/offline (every 30s)
//
// Design principle: Desired State (DB) → Read Actual State (MediaMTX) → Diff → Apply.
// No event dependency; self-healing after any outage.
type Reconciler struct {
	camRepo CameraRepo
	schRepo ScheduleRepo
	recRepo RecordingRepo
	reader  MTXReader
	act     MTXActuator

	recordPath     string
	segmentDuration string

	mtxDown      bool
	probeCounter int
	// sawCamPaths tracks whether cam-* paths existed on MediaMTX previously;
	// used to log a one-shot warning when they all vanish (MediaMTX restart).
	sawCamPaths bool
	// orphanRecordTicks counts consecutive observations of orphan recording
	// state per MediaMTX path (see orphanRecordConfirmTicks). Accessed only
	// from the single reconcile loop, so no locking is needed.
	orphanRecordTicks map[string]int
	stopCh            chan struct{}
	stopOnce          sync.Once
}

func NewReconciler(
	camRepo CameraRepo,
	schRepo ScheduleRepo,
	recRepo RecordingRepo,
	reader MTXReader,
	act MTXActuator,
	recordPath, segmentDuration string,
) *Reconciler {
	return &Reconciler{
		camRepo:           camRepo,
		schRepo:           schRepo,
		recRepo:           recRepo,
		reader:            reader,
		act:               act,
		recordPath:        recordPath,
		segmentDuration:   segmentDuration,
		orphanRecordTicks: make(map[string]int),
		stopCh:            make(chan struct{}),
	}
}

func (r *Reconciler) Run() {
	logger.Infof("reconciler started (interval=%s)", reconcileInterval)
	ticker := time.NewTicker(reconcileInterval)
	defer ticker.Stop()

	r.reconcile() // initial full reconcile on startup
	for {
		select {
		case <-ticker.C:
			r.reconcile()
		case <-r.stopCh:
			logger.Info("reconciler stopped")
			return
		}
	}
}

func (r *Reconciler) Stop() {
	r.stopOnce.Do(func() { close(r.stopCh) })
}

// reconcile runs all sub-reconcilers. Each is independent and idempotent.
func (r *Reconciler) reconcile() {
	metrics.ReconcileCycleTotal.Inc()

	// 1. Check MediaMTX health
	if err := r.reader.HealthCheck(); err != nil {
		metrics.MediaMTXUp.Set(0)
		if !r.mtxDown {
			logger.Warnf("reconciler: mediamtx is down: %v", err)
			r.mtxDown = true
		}
		return
	}
	metrics.MediaMTXUp.Set(1)
	if r.mtxDown {
		logger.Info("reconciler: mediamtx recovered, running full reconcile")
		r.mtxDown = false
	}

	// 2. Fetch MediaMTX path configs once (shared by sub-reconcilers)
	mtxConfigs, err := r.reader.ListPathConfigs()
	if err != nil {
		logger.Errorf("reconciler: list mediamtx configs: %v", err)
		return
	}

	// 3. Ensure streams exist (idempotent)
	r.reconcileStreams(mtxConfigs)

	// 4. Ensure recording state matches desired state
	r.reconcileRecording(mtxConfigs)

	// 5. Update metrics for camera status counts
	r.updateCameraMetrics()

	// 6. Update metrics for active recording sessions
	r.updateRecordingMetrics()

	// 7. Probe camera status every 3rd tick (30s)
	r.probeCounter++
	if r.probeCounter >= statusProbeEvery {
		r.reconcileCameraStatus()
		r.probeCounter = 0
	}
}

// updateCameraMetrics refreshes camera count gauges by status.
func (r *Reconciler) updateCameraMetrics() {
	cams, err := r.camRepo.FindAll()
	if err != nil {
		return
	}
	counts := make(map[string]int)
	for _, cam := range cams {
		counts[cam.Status]++
	}
	// Reset all known statuses then set current values
	for _, s := range []string{"online", "offline", "connecting", "disconnected", "error"} {
		metrics.CameraStatus.WithLabelValues(s).Set(float64(counts[s]))
	}
}

// updateRecordingMetrics refreshes active recording session gauges.
func (r *Reconciler) updateRecordingMetrics() {
	sessions, err := r.recRepo.FindActiveSessions()
	if err != nil {
		return
	}
	counts := make(map[string]int)
	for _, sess := range sessions {
		counts[sess.TriggerType]++
	}
	for _, t := range []string{"manual", "schedule"} {
		metrics.RecordingActiveSessions.WithLabelValues(t).Set(float64(counts[t]))
	}
}

// ---------------------------------------------------------------------------
// Sub-reconciler: Streams
// ---------------------------------------------------------------------------

// reconcileStreams ensures every DB camera has a corresponding path in MediaMTX
// with the correct source configuration, and removes orphaned cam-* paths.
//
// Idempotent: skips paths that already exist with matching config.
// Respects subjective states: disconnected cameras are not re-created;
// their stale paths (if any) are treated as orphans and removed.
func (r *Reconciler) reconcileStreams(mtxConfigs map[string]mediamtx.PathConfigItem) {
	cams, err := r.camRepo.FindAll()
	if err != nil {
		logger.Errorf("reconcile streams: fetch cameras: %v", err)
		return
	}

	// Detect MediaMTX restart: cameras exist in DB but no cam-* path survived.
	// reconcileStreams re-registers everything below anyway; this just makes
	// the event explicit in logs instead of silent churn.
	hasCamPaths := false
	for name := range mtxConfigs {
		if strings.HasPrefix(name, "cam-") {
			hasCamPaths = true
			break
		}
	}
	if len(cams) > 0 && !hasCamPaths {
		if r.sawCamPaths {
			logger.Warnf("reconcile streams: all cam-* paths vanished while %d cameras exist — mediamtx likely restarted, re-registering all paths", len(cams))
		}
		r.sawCamPaths = false
	} else if hasCamPaths {
		r.sawCamPaths = true
	}

	// Ensure all DB cameras have paths with correct config
	dbPaths := make(map[string]bool, len(cams))
	for _, cam := range cams {
		// Skip disconnected cameras — user explicitly detached, don't rebuild path.
		// Their path (if any) will be treated as orphan and cleaned up below.
		if cam.Status == "disconnected" {
			continue
		}
		dbPaths[cam.MediaMTXPath] = true

		existing, exists := mtxConfigs[cam.MediaMTXPath]
		if exists {
			// Only compare source for drift detection.
			// sourceOnDemand is intentionally EXCLUDED: it is dynamically managed
			// by the recording logic (false while recording, true otherwise).
			// Including it here caused reconcileStreams to fight with reconcileRecording,
			// deleting the path every 10s and breaking recording into ~20s fragments.
			if existing.Source == cam.StreamURL {
				continue // source matches, nothing to do
			}
			// Source changed — delete and re-add. The actuator serializes
			// per-path mutations, so the ensure lands after the delete.
			logger.Warnf("reconcile streams: drift detected for %s (source %q -> %q)",
				cam.MediaMTXPath, existing.Source, cam.StreamURL)
			r.act.EnqueueDeletePath(cam.MediaMTXPath)
		}

		r.act.EnqueueEnsurePath(cam.MediaMTXPath, mediamtx.PathConfig{
			Source:                cam.StreamURL,
			SourceOnDemand:        true,
			RecordPath:            r.recordPath + "/%path/%Y-%m-%d_%H-%M-%S",
			RecordSegmentDuration: r.segmentDuration,
		})
		logger.Infof("reconcile streams: ensured path %s for camera %s", cam.MediaMTXPath, cam.ID)
		_ = r.camRepo.UpdateStatus(cam.ID, "connecting")
	}

	// Remove orphaned cam-* paths (not in DB, or disconnected cameras' stale paths)
	for name := range mtxConfigs {
		if !strings.HasPrefix(name, "cam-") {
			continue
		}
		if !dbPaths[name] {
			r.act.EnqueueDeletePath(name)
			logger.Infof("reconcile streams: queued removal of orphan path %s", name)
		}
	}
}

// ---------------------------------------------------------------------------
// Sub-reconciler: Recording
// ---------------------------------------------------------------------------

// reconcileRecording ensures MediaMTX recording state matches the desired state
// derived from schedules and active recording sessions.
//
// Logic:
//  1. Process schedule transitions (start/stop) and manage RecordingSessions
//  2. Compute desired recording state per camera (schedule OR manual session)
//  3. Compare with actual MediaMTX record config and apply diff
//
// 注意：schedule.StreamType（main/sub）为一期预留字段，暂不生效。
// 当前录像始终使用主码流（cam.MediaMTXPath → stream_url），二期实现子码流录制时
// 需注册 cam-{id}-sub 路径并按 stream_type 选择 PatchPath 目标。
func (r *Reconciler) reconcileRecording(mtxConfigs map[string]mediamtx.PathConfigItem) {
	now := time.Now()
	weekday := int(now.Weekday())
	timeStr := now.Format("15:04")

	// --- 1. Process schedule transitions ---

	schedules, err := r.schRepo.FindAllEnabled()
	if err != nil {
		logger.Errorf("reconcile recording: fetch schedules: %v", err)
		return
	}

	// Track cameras that should be recording due to schedule
	scheduleRecording := make(map[string]bool) // cameraID → true

	for i := range schedules {
		sch := &schedules[i]
		if !containsWeekday(sch.Weekdays, weekday) {
			continue
		}

		cam, err := r.camRepo.FindByID(sch.CameraID)
		if err != nil || cam == nil {
			logger.Errorf("reconcile recording: camera %s not found for schedule %s", sch.CameraID, sch.ID)
			continue
		}

		inWindow := true
		if sch.StartTime != "" && timeStr < sch.StartTime {
			inWindow = false
		}
		if sch.EndTime != "" && timeStr > sch.EndTime {
			inWindow = false
		}

		switch {
		case inWindow && sch.LastAction != "start":
			// Intent first: enqueue record-on, then persist the session. The
			// actuator retries until applied, and drift recovery re-applies if
			// the state is ever lost — so no rollback dance is needed here.
			r.act.EnqueueSetRecord(cam.MediaMTXPath, true)
			sess := &model.RecordingSession{
				ID:          uuid.NewString(),
				CameraID:    cam.ID,
				TriggerType: "schedule",
				ScheduleID:  &sch.ID,
				StartTime:   now,
				LicenseID:   cam.LicenseID,
				CreatedAt:   now,
				UpdatedAt:   now,
			}
			if err := r.recRepo.CreateSession(sess); err != nil {
				logger.Errorf("reconcile recording: create session for schedule %s: %v", sch.ID, err)
			}
			sch.LastAction = "start"
			nowCopy := now
			sch.LastTriggeredAt = &nowCopy
			_ = r.schRepo.Update(sch)
			scheduleRecording[cam.ID] = true
			logger.Infof("reconcile recording: started schedule %s camera %s", sch.ID, sch.CameraID)

		case !inWindow && sch.LastAction == "start":
			// Close the schedule session FIRST: the session is the source of truth
			// for desired recording state. While it is open, drift recovery (below)
			// re-applies record:true and would undo the stop patch.
			if sess, err := r.recRepo.FindActiveSessionBySchedule(sch.ID); err == nil && sess != nil {
				_ = r.recRepo.CloseSession(sess.ID, now)
			}
			// Stop recording (unless a manual session is still active)
			manualActive := false
			if sess, err := r.recRepo.FindActiveSessionByCamera(sch.CameraID); err == nil && sess != nil {
				if sess.TriggerType == "manual" {
					manualActive = true
				}
			}
			if !manualActive {
				r.act.EnqueueSetRecord(cam.MediaMTXPath, false)
			}
			sch.LastAction = "stop"
			_ = r.schRepo.Update(sch)
			logger.Infof("reconcile recording: stopped schedule %s camera %s (manual_active=%v)", sch.ID, sch.CameraID, manualActive)

		case inWindow && sch.LastAction == "start":
			scheduleRecording[cam.ID] = true
		}
	}

	// --- 2. Recover active sessions (manual + schedule drift) ---

	sessions, err := r.recRepo.FindActiveSessions()
	if err != nil {
		logger.Errorf("reconcile recording: fetch active sessions: %v", err)
		return
	}

	restored := 0
	for _, sess := range sessions {
		cam, err := r.camRepo.FindByID(sess.CameraID)
		if err != nil || cam == nil {
			continue
		}

		actualRecording := false
		if cfg, exists := mtxConfigs[cam.MediaMTXPath]; exists {
			actualRecording = cfg.Record
		}

		if !actualRecording {
			// Active session exists but MediaMTX not recording — re-apply
			r.act.EnqueueSetRecord(cam.MediaMTXPath, true)
			restored++
			metrics.DriftEvents.WithLabelValues("forward").Inc()
			logger.Warnf("reconcile recording: recovered session %s camera %s (drift)", sess.ID, sess.CameraID)
		}
	}
	if restored > 0 {
		logger.Infof("reconcile recording: recovered %d sessions from drift", restored)
	}

	// --- 3. Stop orphan recordings (reverse-direction repair) ---
	// Ensure paths with NO active session and NO in-window schedule are not
	// recording. This completes the stop semantics: StopManual / schedule stop
	// close the session first and then patch MediaMTX; if that patch fails (or
	// MediaMTX state drifts for any other reason), this section forces
	// record:false on a later cycle. Without it, a failed stop patch would
	// leave the camera recording forever with no session to account for it.
	//
	// Hysteresis (orphanRecordConfirmTicks): start flows patch record:true a
	// few milliseconds before creating the session row; requiring two
	// consecutive orphan observations guarantees we never stop a recording
	// that is in the middle of starting.
	wantRecord := make(map[string]bool, len(sessions)+len(scheduleRecording))
	for _, sess := range sessions {
		wantRecord[sess.CameraID] = true
	}
	for camID := range scheduleRecording {
		wantRecord[camID] = true
	}

	cams, err := r.camRepo.FindAll()
	if err != nil {
		logger.Errorf("reconcile recording: fetch cameras for orphan check: %v", err)
		return
	}
	for _, cam := range cams {
		cfg, exists := mtxConfigs[cam.MediaMTXPath]
		if !exists || !cfg.Record || wantRecord[cam.ID] {
			delete(r.orphanRecordTicks, cam.MediaMTXPath)
			continue
		}
		// record=true without any session or schedule wanting it — count it.
		r.orphanRecordTicks[cam.MediaMTXPath]++
		if r.orphanRecordTicks[cam.MediaMTXPath] < orphanRecordConfirmTicks {
			logger.Infof("reconcile recording: orphan recording suspected on %s (observation %d/%d)",
				cam.MediaMTXPath, r.orphanRecordTicks[cam.MediaMTXPath], orphanRecordConfirmTicks)
			continue
		}
		r.act.EnqueueSetRecord(cam.MediaMTXPath, false)
		delete(r.orphanRecordTicks, cam.MediaMTXPath)
		metrics.DriftEvents.WithLabelValues("reverse").Inc()
		logger.Warnf("reconcile recording: stopped orphan recording on %s (no active session or schedule)", cam.MediaMTXPath)
	}
}

// ---------------------------------------------------------------------------
// Sub-reconciler: Camera Status
// ---------------------------------------------------------------------------

// reconcileCameraStatus TCP-probes each camera and updates online/offline status.
//
// Status protection rules:
//   - disconnected: skip (user explicitly detached; probe must not override)
//   - error:        skip (system error state; probe must not auto-clear)
//   - connecting:   probe, but only transition to online (grace period —
//                   don't write offline on first probe to avoid UI flicker)
//   - online/offline: probe normally
func (r *Reconciler) reconcileCameraStatus() {
	cams, err := r.camRepo.FindAll()
	if err != nil {
		logger.Errorf("reconcile status: fetch cameras: %v", err)
		return
	}

	for _, cam := range cams {
		// Skip subjective/system states that must not be overwritten by probe
		if cam.Status == "disconnected" || cam.Status == "error" {
			continue
		}

		oldStatus := cam.Status
		newStatus := probeCamera(cam.IP, cam.Port)

		// For connecting cameras, only transition to online (grace period)
		if oldStatus == "connecting" && newStatus == "offline" {
			continue
		}

		if oldStatus != newStatus {
			logger.Infof("reconcile status: camera %s %s -> %s", cam.ID, oldStatus, newStatus)
			_ = r.camRepo.UpdateStatus(cam.ID, newStatus)
		}
	}
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func probeCamera(ip string, port int) string {
	if port <= 0 {
		port = 554
	}
	addr := net.JoinHostPort(ip, strconv.Itoa(port))
	conn, err := net.DialTimeout("tcp", addr, probeTimeout)
	if err != nil {
		return "offline"
	}
	conn.Close()
	return "online"
}

func containsWeekday(weekdays string, day int) bool {
	if weekdays == "" {
		return false
	}
	for _, s := range strings.Split(weekdays, ",") {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		d, err := strconv.Atoi(s)
		if err != nil {
			continue
		}
		if d == day {
			return true
		}
	}
	return false
}
