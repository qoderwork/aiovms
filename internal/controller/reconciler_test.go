package controller

import (
	"errors"
	"net"
	"strconv"
	"testing"
	"time"

	"aiovms/internal/mediamtx"
	"aiovms/internal/model"
)

// ---------------------------------------------------------------------------
// Mock implementations
// ---------------------------------------------------------------------------

type mockCameraRepo struct {
	cams         []model.Camera
	findAllErr   error
	findByIDErr  error
	updateErr    error
	updatedIDs   []string // track UpdateStatus calls
	updatedStats []string // track status values written
}

func (m *mockCameraRepo) FindAll() ([]model.Camera, error) {
	if m.findAllErr != nil {
		return nil, m.findAllErr
	}
	return m.cams, nil
}

func (m *mockCameraRepo) FindByID(id string) (*model.Camera, error) {
	if m.findByIDErr != nil {
		return nil, m.findByIDErr
	}
	for i := range m.cams {
		if m.cams[i].ID == id {
			return &m.cams[i], nil
		}
	}
	return nil, errors.New("not found")
}

func (m *mockCameraRepo) UpdateStatus(id string, status string) error {
	if m.updateErr != nil {
		return m.updateErr
	}
	m.updatedIDs = append(m.updatedIDs, id)
	m.updatedStats = append(m.updatedStats, status)
	return nil
}

type mockScheduleRepo struct {
	schedules       []model.RecordSchedule
	findAllEnabErr  error
	updateErr       error
	lastUpdated     *model.RecordSchedule
	updateCallCount int
}

func (m *mockScheduleRepo) FindAllEnabled() ([]model.RecordSchedule, error) {
	if m.findAllEnabErr != nil {
		return nil, m.findAllEnabErr
	}
	return m.schedules, nil
}

func (m *mockScheduleRepo) Update(sch *model.RecordSchedule) error {
	if m.updateErr != nil {
		return m.updateErr
	}
	m.lastUpdated = sch
	m.updateCallCount++
	return nil
}

type mockRecRepo struct {
	activeSessions       []model.RecordingSession
	findActiveErr        error
	findByCamErr         error
	findBySchedErr       error
	createErr            error
	closeErr             error
	lastCreatedSession   *model.RecordingSession
	lastClosedSessionID  string
	lastClosedAt         time.Time
	findByCameraResult   *model.RecordingSession // override for FindActiveSessionByCamera
	findByScheduleResult *model.RecordingSession // override for FindActiveSessionBySchedule
}

func (m *mockRecRepo) FindActiveSessions() ([]model.RecordingSession, error) {
	return m.activeSessions, m.findActiveErr
}

func (m *mockRecRepo) FindActiveSessionByCamera(cameraID string) (*model.RecordingSession, error) {
	if m.findByCamErr != nil {
		return nil, m.findByCamErr
	}
	if m.findByCameraResult != nil {
		return m.findByCameraResult, nil
	}
	for i := range m.activeSessions {
		if m.activeSessions[i].CameraID == cameraID {
			return &m.activeSessions[i], nil
		}
	}
	return nil, errors.New("not found")
}

func (m *mockRecRepo) FindActiveSessionBySchedule(scheduleID string) (*model.RecordingSession, error) {
	if m.findBySchedErr != nil {
		return nil, m.findBySchedErr
	}
	if m.findByScheduleResult != nil {
		return m.findByScheduleResult, nil
	}
	for i := range m.activeSessions {
		if m.activeSessions[i].ScheduleID != nil && *m.activeSessions[i].ScheduleID == scheduleID {
			return &m.activeSessions[i], nil
		}
	}
	return nil, errors.New("not found")
}

func (m *mockRecRepo) CreateSession(sess *model.RecordingSession) error {
	if m.createErr != nil {
		return m.createErr
	}
	m.lastCreatedSession = sess
	return nil
}

func (m *mockRecRepo) CloseSession(id string, endTime time.Time) error {
	if m.closeErr != nil {
		return m.closeErr
	}
	m.lastClosedSessionID = id
	m.lastClosedAt = endTime
	return nil
}

type mockMTXClient struct {
	addPathCalls    []struct{ name string; cfg mediamtx.PathConfig }
	addPathErr      error
	deletePathCalls []string
	deletePathErr   error
	patchPathCalls  []struct{ name string; patch map[string]any }
	patchPathErr    error
	pathConfigs     map[string]mediamtx.PathConfigItem
	listConfigsErr  error
	healthErr       error
	healthCallCount int
}

func (m *mockMTXClient) AddPath(name string, cfg mediamtx.PathConfig) error {
	m.addPathCalls = append(m.addPathCalls, struct {
		name string
		cfg  mediamtx.PathConfig
	}{name, cfg})
	return m.addPathErr
}

func (m *mockMTXClient) DeletePath(name string) error {
	m.deletePathCalls = append(m.deletePathCalls, name)
	return m.deletePathErr
}

func (m *mockMTXClient) PatchPath(name string, patch map[string]any) error {
	m.patchPathCalls = append(m.patchPathCalls, struct {
		name  string
		patch map[string]any
	}{name, patch})
	return m.patchPathErr
}

func (m *mockMTXClient) ListPathConfigs() (map[string]mediamtx.PathConfigItem, error) {
	return m.pathConfigs, m.listConfigsErr
}

func (m *mockMTXClient) HealthCheck() error {
	m.healthCallCount++
	return m.healthErr
}

// ---------------------------------------------------------------------------
// Helper: build a reconciler with mocks
// ---------------------------------------------------------------------------

func newTestReconciler() (*Reconciler, *mockCameraRepo, *mockScheduleRepo, *mockRecRepo, *mockMTXClient) {
	camRepo := &mockCameraRepo{}
	schRepo := &mockScheduleRepo{}
	recRepo := &mockRecRepo{}
	mtx := &mockMTXClient{
		pathConfigs: make(map[string]mediamtx.PathConfigItem),
	}
	r := NewReconciler(camRepo, schRepo, recRepo, mtx, "/recordings", "1h")
	return r, camRepo, schRepo, recRepo, mtx
}

func ptr(s string) *string { return &s }

// ---------------------------------------------------------------------------
// Tests: reconcileStreams
// ---------------------------------------------------------------------------

func TestReconcileStreams_CreatesMissingPath(t *testing.T) {
	r, camRepo, _, _, mtx := newTestReconciler()
	camRepo.cams = []model.Camera{
		{ID: "cam-1", MediaMTXPath: "cam-cam-1", StreamURL: "rtsp://1.2.3.4/stream"},
	}
	// MTX has no paths at all

	r.reconcileStreams(mtx.pathConfigs)

	if len(mtx.addPathCalls) != 1 {
		t.Fatalf("expected 1 AddPath call, got %d", len(mtx.addPathCalls))
	}
	call := mtx.addPathCalls[0]
	if call.name != "cam-cam-1" {
		t.Errorf("path name = %q, want 'cam-cam-1'", call.name)
	}
	if call.cfg.Source != "rtsp://1.2.3.4/stream" {
		t.Errorf("source = %q", call.cfg.Source)
	}
	if !call.cfg.SourceOnDemand {
		t.Error("expected SourceOnDemand=true")
	}
	// Should also set camera status to "connecting"
	if len(camRepo.updatedIDs) != 1 || camRepo.updatedIDs[0] != "cam-1" {
		t.Errorf("expected status update for cam-1, got %v", camRepo.updatedIDs)
	}
	if camRepo.updatedStats[0] != "connecting" {
		t.Errorf("status = %q, want 'connecting'", camRepo.updatedStats[0])
	}
}

func TestReconcileStreams_SkipsExistingPathWithMatchingConfig(t *testing.T) {
	r, camRepo, _, _, mtx := newTestReconciler()
	camRepo.cams = []model.Camera{
		{ID: "cam-1", MediaMTXPath: "cam-cam-1", StreamURL: "rtsp://1.2.3.4/stream"},
	}
	mtx.pathConfigs["cam-cam-1"] = mediamtx.PathConfigItem{
		Name:           "cam-cam-1",
		Source:         "rtsp://1.2.3.4/stream",
		SourceOnDemand: true,
		Record:         false,
	}

	r.reconcileStreams(mtx.pathConfigs)

	if len(mtx.addPathCalls) != 0 {
		t.Errorf("expected 0 AddPath calls (path exists with matching config), got %d", len(mtx.addPathCalls))
	}
	if len(mtx.deletePathCalls) != 0 {
		t.Errorf("expected 0 DeletePath calls, got %d", len(mtx.deletePathCalls))
	}
	if len(camRepo.updatedIDs) != 0 {
		t.Errorf("expected no status update, got %v", camRepo.updatedIDs)
	}
}

func TestReconcileStreams_DetectsSourceDrift(t *testing.T) {
	r, camRepo, _, _, mtx := newTestReconciler()
	camRepo.cams = []model.Camera{
		{ID: "cam-1", MediaMTXPath: "cam-cam-1", StreamURL: "rtsp://new-url/stream"},
	}
	// MTX has path with OLD source
	mtx.pathConfigs["cam-cam-1"] = mediamtx.PathConfigItem{
		Name:           "cam-cam-1",
		Source:         "rtsp://old-url/stream",
		SourceOnDemand: true,
	}

	r.reconcileStreams(mtx.pathConfigs)

	// Should delete the stale path
	if len(mtx.deletePathCalls) != 1 || mtx.deletePathCalls[0] != "cam-cam-1" {
		t.Fatalf("expected 1 DeletePath call for cam-cam-1, got %v", mtx.deletePathCalls)
	}
	// Should re-add with new source
	if len(mtx.addPathCalls) != 1 {
		t.Fatalf("expected 1 AddPath call, got %d", len(mtx.addPathCalls))
	}
	if mtx.addPathCalls[0].cfg.Source != "rtsp://new-url/stream" {
		t.Errorf("source = %q, want 'rtsp://new-url/stream'", mtx.addPathCalls[0].cfg.Source)
	}
}

// TestReconcileStreams_SkipsSourceOnDemandFalse verifies that sourceOnDemand=false does NOT
// trigger drift detection. sourceOnDemand is dynamically managed by recording logic
// (false while recording, true otherwise), so reconcileStreams must only compare source URLs.
func TestReconcileStreams_SkipsSourceOnDemandFalse(t *testing.T) {
	r, camRepo, _, _, mtx := newTestReconciler()
	camRepo.cams = []model.Camera{
		{ID: "cam-1", MediaMTXPath: "cam-cam-1", StreamURL: "rtsp://1.2.3.4/stream"},
	}
	// Source matches, but SourceOnDemand=false (e.g. during recording).
	// This is NOT drift — reconcileRecording manages sourceOnDemand separately.
	mtx.pathConfigs["cam-cam-1"] = mediamtx.PathConfigItem{
		Name:           "cam-cam-1",
		Source:         "rtsp://1.2.3.4/stream",
		SourceOnDemand: false,
	}

	r.reconcileStreams(mtx.pathConfigs)

	if len(mtx.deletePathCalls) != 0 {
		t.Errorf("expected 0 DeletePath calls (sourceOnDemand drift is not checked), got %d", len(mtx.deletePathCalls))
	}
	if len(mtx.addPathCalls) != 0 {
		t.Errorf("expected 0 AddPath calls (sourceOnDemand=false is not drift), got %d", len(mtx.addPathCalls))
	}
	if len(camRepo.updatedIDs) != 0 {
		t.Errorf("expected no status update, got %v", camRepo.updatedIDs)
	}
}

func TestReconcileStreams_SkipsDisconnectedCamera(t *testing.T) {
	r, camRepo, _, _, mtx := newTestReconciler()
	camRepo.cams = []model.Camera{
		{ID: "cam-1", MediaMTXPath: "cam-cam-1", StreamURL: "rtsp://1.2.3.4/stream", Status: "disconnected"},
	}
	// MTX still has the path (Disconnect API might have failed to delete it)
	mtx.pathConfigs["cam-cam-1"] = mediamtx.PathConfigItem{
		Name:           "cam-cam-1",
		Source:         "rtsp://1.2.3.4/stream",
		SourceOnDemand: true,
	}

	r.reconcileStreams(mtx.pathConfigs)

	// Should NOT create a new path for disconnected camera
	if len(mtx.addPathCalls) != 0 {
		t.Errorf("expected 0 AddPath calls for disconnected camera, got %d", len(mtx.addPathCalls))
	}
	// Should treat the stale path as orphan and delete it
	if len(mtx.deletePathCalls) != 1 || mtx.deletePathCalls[0] != "cam-cam-1" {
		t.Errorf("expected orphan deletion of cam-cam-1, got %v", mtx.deletePathCalls)
	}
	// Should NOT update status
	if len(camRepo.updatedIDs) != 0 {
		t.Errorf("expected no status update for disconnected camera, got %v", camRepo.updatedIDs)
	}
}

func TestReconcileStreams_RemovesOrphanPath(t *testing.T) {
	r, _, _, _, mtx := newTestReconciler()
	// DB has no cameras, but MTX has orphan cam-* paths
	mtx.pathConfigs["cam-orphan"] = mediamtx.PathConfigItem{Name: "cam-orphan"}
	mtx.pathConfigs["cam-ghost"] = mediamtx.PathConfigItem{Name: "cam-ghost"}

	r.reconcileStreams(mtx.pathConfigs)

	if len(mtx.deletePathCalls) != 2 {
		t.Fatalf("expected 2 DeletePath calls, got %d", len(mtx.deletePathCalls))
	}
	deleted := map[string]bool{}
	for _, name := range mtx.deletePathCalls {
		deleted[name] = true
	}
	if !deleted["cam-orphan"] || !deleted["cam-ghost"] {
		t.Errorf("expected cam-orphan and cam-ghost deleted, got %v", mtx.deletePathCalls)
	}
}

func TestReconcileStreams_DoesNotRemoveNonCamPaths(t *testing.T) {
	r, _, _, _, mtx := newTestReconciler()
	mtx.pathConfigs["playback-test"] = mediamtx.PathConfigItem{Name: "playback-test"}
	mtx.pathConfigs["cam-orphan"] = mediamtx.PathConfigItem{Name: "cam-orphan"}

	r.reconcileStreams(mtx.pathConfigs)

	if len(mtx.deletePathCalls) != 1 {
		t.Fatalf("expected 1 DeletePath call, got %d", len(mtx.deletePathCalls))
	}
	if mtx.deletePathCalls[0] != "cam-orphan" {
		t.Errorf("expected only cam-orphan deleted, got %q", mtx.deletePathCalls[0])
	}
}

func TestReconcileStreams_HandlesFindAllError(t *testing.T) {
	r, camRepo, _, _, mtx := newTestReconciler()
	camRepo.findAllErr = errors.New("db down")

	r.reconcileStreams(mtx.pathConfigs) // should not panic

	if len(mtx.addPathCalls) != 0 {
		t.Error("expected no AddPath calls when FindAll fails")
	}
}

// ---------------------------------------------------------------------------
// Tests: reconcileRecording — schedule transitions
// ---------------------------------------------------------------------------

func TestReconcileRecording_ScheduleStart(t *testing.T) {
	r, camRepo, schRepo, recRepo, mtx := newTestReconciler()
	weekday := int(time.Now().Weekday())

	camRepo.cams = []model.Camera{
		{ID: "cam-1", MediaMTXPath: "cam-cam-1", LicenseID: 1},
	}
	schRepo.schedules = []model.RecordSchedule{
		{
			ID: "sch-1", CameraID: "cam-1", Enabled: true,
			Weekdays: weekdayStr(weekday), StartTime: "00:00", EndTime: "23:59",
			LastAction: "", // never triggered
		},
	}

	r.reconcileRecording(mtx.pathConfigs)

	// Should patch record=true
	if len(mtx.patchPathCalls) != 1 {
		t.Fatalf("expected 1 PatchPath call, got %d", len(mtx.patchPathCalls))
	}
	if mtx.patchPathCalls[0].name != "cam-cam-1" {
		t.Errorf("patch name = %q", mtx.patchPathCalls[0].name)
	}
	if v, ok := mtx.patchPathCalls[0].patch["record"]; !ok || v != true {
		t.Error("expected record=true")
	}
	// Should create a session with trigger_type=schedule
	if recRepo.lastCreatedSession == nil {
		t.Fatal("expected session created")
	}
	if recRepo.lastCreatedSession.TriggerType != "schedule" {
		t.Errorf("trigger = %q, want 'schedule'", recRepo.lastCreatedSession.TriggerType)
	}
	if recRepo.lastCreatedSession.CameraID != "cam-1" {
		t.Errorf("session camera = %q", recRepo.lastCreatedSession.CameraID)
	}
	if recRepo.lastCreatedSession.ScheduleID == nil || *recRepo.lastCreatedSession.ScheduleID != "sch-1" {
		t.Error("expected schedule_id=sch-1")
	}
	// Should update schedule LastAction=start
	if schRepo.lastUpdated == nil || schRepo.lastUpdated.LastAction != "start" {
		t.Error("expected schedule LastAction=start")
	}
}

func TestReconcileRecording_ScheduleStop(t *testing.T) {
	r, camRepo, schRepo, recRepo, mtx := newTestReconciler()
	schID := "sch-1"
	camRepo.cams = []model.Camera{
		{ID: "cam-1", MediaMTXPath: "cam-cam-1", LicenseID: 1},
	}
	schRepo.schedules = []model.RecordSchedule{
		{
			ID: schID, CameraID: "cam-1", Enabled: true,
			Weekdays:   weekdayStr(int(time.Now().Weekday())),
			StartTime:  "00:00",
			EndTime:    "00:01",
			LastAction: "start", // was started, now outside window
		},
	}
	// Active schedule session to close
	schedIDCopy := schID
	recRepo.activeSessions = []model.RecordingSession{
		{ID: "ses-1", CameraID: "cam-1", TriggerType: "schedule", ScheduleID: &schedIDCopy},
	}

	r.reconcileRecording(mtx.pathConfigs)

	// Time now is almost certainly past 00:01, so should stop
	foundStop := false
	for _, call := range mtx.patchPathCalls {
		if call.name == "cam-cam-1" {
			if v, ok := call.patch["record"]; ok && v == false {
				foundStop = true
			}
		}
	}
	if !foundStop {
		t.Error("expected record=false patch (schedule stop)")
	}
	// Should close the schedule session
	if recRepo.lastClosedSessionID != "ses-1" {
		t.Errorf("expected session ses-1 closed, got %q", recRepo.lastClosedSessionID)
	}
	// Should update schedule LastAction=stop
	if schRepo.lastUpdated == nil || schRepo.lastUpdated.LastAction != "stop" {
		t.Error("expected schedule LastAction=stop")
	}
}

func TestReconcileRecording_ScheduleStopSkippedWhenManualActive(t *testing.T) {
	r, camRepo, schRepo, recRepo, mtx := newTestReconciler()
	camRepo.cams = []model.Camera{
		{ID: "cam-1", MediaMTXPath: "cam-cam-1", LicenseID: 1},
	}
	schRepo.schedules = []model.RecordSchedule{
		{
			ID: "sch-1", CameraID: "cam-1", Enabled: true,
			Weekdays:   weekdayStr(int(time.Now().Weekday())),
			StartTime:  "00:00",
			EndTime:    "00:01",
			LastAction: "start",
		},
	}
	// Active manual session for same camera
	recRepo.activeSessions = []model.RecordingSession{
		{ID: "ses-manual", CameraID: "cam-1", TriggerType: "manual"},
	}

	r.reconcileRecording(mtx.pathConfigs)

	// Should NOT patch record=false (manual session active)
	for _, call := range mtx.patchPathCalls {
		if call.name == "cam-cam-1" {
			if v, ok := call.patch["record"]; ok && v == false {
				t.Error("should not stop recording when manual session is active")
			}
		}
	}
}

func TestReconcileRecording_ScheduleAlreadyStarted_NoNewPatch(t *testing.T) {
	r, camRepo, schRepo, _, mtx := newTestReconciler()
	camRepo.cams = []model.Camera{
		{ID: "cam-1", MediaMTXPath: "cam-cam-1", LicenseID: 1},
	}
	schRepo.schedules = []model.RecordSchedule{
		{
			ID: "sch-1", CameraID: "cam-1", Enabled: true,
			Weekdays:   weekdayStr(int(time.Now().Weekday())),
			StartTime:  "00:00",
			EndTime:    "23:59",
			LastAction: "start", // already started
		},
	}

	r.reconcileRecording(mtx.pathConfigs)

	for _, call := range mtx.patchPathCalls {
		if call.name == "cam-cam-1" && call.patch["record"] == true {
			t.Error("should not patch record=true when schedule already started and no drift")
		}
	}
}

func TestReconcileRecording_DriftRecovery(t *testing.T) {
	r, camRepo, _, recRepo, mtx := newTestReconciler()
	camRepo.cams = []model.Camera{
		{ID: "cam-1", MediaMTXPath: "cam-cam-1", LicenseID: 1},
	}
	// Active session exists but MTX record=false (drift after MTX restart)
	recRepo.activeSessions = []model.RecordingSession{
		{ID: "ses-1", CameraID: "cam-1", TriggerType: "manual"},
	}
	mtx.pathConfigs["cam-cam-1"] = mediamtx.PathConfigItem{
		Name:   "cam-cam-1",
		Record: false, // drift! session active but not recording
	}

	r.reconcileRecording(mtx.pathConfigs)

	foundRecover := false
	for _, call := range mtx.patchPathCalls {
		if call.name == "cam-cam-1" {
			if v, ok := call.patch["record"]; ok && v == true {
				foundRecover = true
			}
		}
	}
	if !foundRecover {
		t.Error("expected record=true patch for drift recovery")
	}
}

func TestReconcileRecording_NoDriftWhenAlreadyRecording(t *testing.T) {
	r, camRepo, _, recRepo, mtx := newTestReconciler()
	camRepo.cams = []model.Camera{
		{ID: "cam-1", MediaMTXPath: "cam-cam-1", LicenseID: 1},
	}
	recRepo.activeSessions = []model.RecordingSession{
		{ID: "ses-1", CameraID: "cam-1", TriggerType: "manual"},
	}
	mtx.pathConfigs["cam-cam-1"] = mediamtx.PathConfigItem{
		Name:   "cam-cam-1",
		Record: true, // already recording, no drift
	}

	r.reconcileRecording(mtx.pathConfigs)

	for _, call := range mtx.patchPathCalls {
		if call.name == "cam-cam-1" {
			t.Error("should not patch when already recording (no drift)")
		}
	}
}

// ---------------------------------------------------------------------------
// Tests: reconcile() — top-level orchestration
// ---------------------------------------------------------------------------

func TestReconcile_MTXDown_SkipsAll(t *testing.T) {
	r, camRepo, _, _, mtx := newTestReconciler()
	camRepo.cams = []model.Camera{
		{ID: "cam-1", MediaMTXPath: "cam-cam-1", StreamURL: "rtsp://1.2.3.4/stream"},
	}
	mtx.healthErr = errors.New("connection refused")

	r.reconcile()

	if len(mtx.addPathCalls) != 0 {
		t.Error("expected no AddPath calls when MTX is down")
	}
	if len(mtx.patchPathCalls) != 0 {
		t.Error("expected no PatchPath calls when MTX is down")
	}
	if !r.mtxDown {
		t.Error("expected mtxDown=true after health check failure")
	}
}

func TestReconcile_MTXRecovers_FullReconcile(t *testing.T) {
	r, camRepo, _, _, mtx := newTestReconciler()
	camRepo.cams = []model.Camera{
		{ID: "cam-1", MediaMTXPath: "cam-cam-1", StreamURL: "rtsp://1.2.3.4/stream"},
	}

	// First reconcile: MTX is down
	mtx.healthErr = errors.New("connection refused")
	r.reconcile()
	if !r.mtxDown {
		t.Fatal("expected mtxDown=true after first reconcile")
	}

	// Second reconcile: MTX recovered
	mtx.healthErr = nil
	r.reconcile()

	if len(mtx.addPathCalls) != 1 {
		t.Fatalf("expected 1 AddPath call after MTX recovery, got %d", len(mtx.addPathCalls))
	}
	if r.mtxDown {
		t.Error("expected mtxDown=false after recovery")
	}
}

// ---------------------------------------------------------------------------
// Tests: reconcileCameraStatus — status protection
// ---------------------------------------------------------------------------

func TestReconcileCameraStatus_SkipsDisconnected(t *testing.T) {
	r, camRepo, _, _, _ := newTestReconciler()
	camRepo.cams = []model.Camera{
		{ID: "cam-1", IP: "192.0.2.1", Port: 554, Status: "disconnected"},
	}

	r.reconcileCameraStatus()

	// Should not update status for disconnected camera
	if len(camRepo.updatedIDs) != 0 {
		t.Errorf("expected no status update for disconnected camera, got %v", camRepo.updatedIDs)
	}
}

func TestReconcileCameraStatus_SkipsError(t *testing.T) {
	r, camRepo, _, _, _ := newTestReconciler()
	camRepo.cams = []model.Camera{
		{ID: "cam-1", IP: "192.0.2.1", Port: 554, Status: "error"},
	}

	r.reconcileCameraStatus()

	// Should not update status for error camera
	if len(camRepo.updatedIDs) != 0 {
		t.Errorf("expected no status update for error camera, got %v", camRepo.updatedIDs)
	}
}

func TestReconcileCameraStatus_ConnectingDoesNotUpdateOffline(t *testing.T) {
	r, camRepo, _, _, _ := newTestReconciler()
	// Camera in "connecting" state, probing an unreachable IP → would return "offline"
	camRepo.cams = []model.Camera{
		{ID: "cam-1", IP: "192.0.2.1", Port: 554, Status: "connecting"},
	}

	r.reconcileCameraStatus()

	// Should NOT write "offline" for a "connecting" camera (grace period)
	if len(camRepo.updatedIDs) != 0 {
		t.Errorf("expected no status update for connecting camera probed offline, got %v", camRepo.updatedIDs)
	}
}

func TestReconcileCameraStatus_ConnectingUpdatesToOnline(t *testing.T) {
	r, camRepo, _, _, _ := newTestReconciler()
	// Start a local listener so probe returns "online"
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skipf("cannot start local listener: %v", err)
	}
	defer ln.Close()
	addr := ln.Addr().(*net.TCPAddr)

	camRepo.cams = []model.Camera{
		{ID: "cam-1", IP: "127.0.0.1", Port: addr.Port, Status: "connecting"},
	}

	r.reconcileCameraStatus()

	// Should update to "online" (connecting → online is allowed)
	if len(camRepo.updatedIDs) != 1 || camRepo.updatedIDs[0] != "cam-1" {
		t.Fatalf("expected status update to online for connecting camera, got %v", camRepo.updatedIDs)
	}
	if camRepo.updatedStats[0] != "online" {
		t.Errorf("status = %q, want 'online'", camRepo.updatedStats[0])
	}
}

func TestReconcileCameraStatus_OnlineToOfflineTransition(t *testing.T) {
	r, camRepo, _, _, _ := newTestReconciler()
	// Camera was "online", probe unreachable IP → should transition to "offline"
	camRepo.cams = []model.Camera{
		{ID: "cam-1", IP: "192.0.2.1", Port: 554, Status: "online"},
	}

	r.reconcileCameraStatus()

	if len(camRepo.updatedIDs) != 1 {
		t.Fatalf("expected 1 status update, got %d", len(camRepo.updatedIDs))
	}
	if camRepo.updatedStats[0] != "offline" {
		t.Errorf("status = %q, want 'offline'", camRepo.updatedStats[0])
	}
}

// ---------------------------------------------------------------------------
// Tests: containsWeekday
// ---------------------------------------------------------------------------

func TestContainsWeekday(t *testing.T) {
	tests := []struct {
		name     string
		weekdays string
		day      int
		want     bool
	}{
		{"single match", "1,2,3", 2, true},
		{"no match", "1,2,3", 5, false},
		{"empty weekdays", "", 1, false},
		{"with spaces", "1, 2, 3", 2, true},
		{"single day match", "0", 0, true},
		{"single day no match", "0", 1, false},
		{"all days", "0,1,2,3,4,5,6", 3, true},
		{"invalid entry ignored", "1,x,3", 3, true},
		{"trailing comma", "1,2,", 2, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := containsWeekday(tt.weekdays, tt.day)
			if got != tt.want {
				t.Errorf("containsWeekday(%q, %d) = %v, want %v", tt.weekdays, tt.day, got, tt.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Tests: probeCamera
// ---------------------------------------------------------------------------

func TestProbeCamera_OfflineForUnreachableIP(t *testing.T) {
	// 192.0.2.x is TEST-NET-1 (RFC 5737), guaranteed unroutable
	result := probeCamera("192.0.2.1", 554)
	if result != "offline" {
		t.Errorf("expected 'offline' for unroutable IP, got %q", result)
	}
}

func TestProbeCamera_DefaultPort(t *testing.T) {
	result := probeCamera("192.0.2.1", 0)
	if result != "offline" {
		t.Errorf("expected 'offline', got %q", result)
	}
}

func TestProbeCamera_OnlineForLocalListener(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skipf("cannot start local listener: %v", err)
	}
	defer ln.Close()

	addr := ln.Addr().(*net.TCPAddr)
	result := probeCamera("127.0.0.1", addr.Port)
	if result != "online" {
		t.Errorf("expected 'online' for local listener, got %q", result)
	}
}

// ---------------------------------------------------------------------------
// Tests: Stop
// ---------------------------------------------------------------------------

func TestReconcilerStop(t *testing.T) {
	r, _, _, _, _ := newTestReconciler()
	r.Stop()
	r.Stop() // double stop should not panic
}

func TestReconcilerStopIdempotent(t *testing.T) {
	r, _, _, _, _ := newTestReconciler()

	done := make(chan struct{})
	go func() {
		defer close(done)
		r.Stop()
		r.Stop()
		r.Stop()
	}()

	select {
	case <-done:
		// ok
	case <-time.After(time.Second):
		t.Fatal("Stop should not block")
	}
}

// ---------------------------------------------------------------------------
// Helper: convert weekday int to comma-separated string (for DB field)
// ---------------------------------------------------------------------------

func weekdayStr(day int) string {
	return strconv.Itoa(day)
}
