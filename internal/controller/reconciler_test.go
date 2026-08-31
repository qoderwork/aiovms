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

func (m *mockScheduleRepo) DeleteByCamera(cameraID string) error {
	out := m.schedules[:0]
	for _, s := range m.schedules {
		if s.CameraID != cameraID {
			out = append(out, s)
		}
	}
	m.schedules = out
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

func (m *mockRecRepo) FindActiveManualSessionByCamera(cameraID string) (*model.RecordingSession, error) {
	for i := range m.activeSessions {
		if m.activeSessions[i].CameraID == cameraID && m.activeSessions[i].TriggerType == model.TriggerManual {
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

func (m *mockRecRepo) CloseActiveSessionsByCamera(cameraID string, endTime time.Time) error {
	return nil
}

// mockMTXReader implements MTXReader (read-only MediaMTX surface).
type mockMTXReader struct {
	pathConfigs     map[string]mediamtx.PathConfigItem
	listConfigsErr  error
	healthErr       error
	healthCallCount int
}

func (m *mockMTXReader) ListPathConfigs() (map[string]mediamtx.PathConfigItem, error) {
	return m.pathConfigs, m.listConfigsErr
}

func (m *mockMTXReader) HealthCheck() error {
	m.healthCallCount++
	return m.healthErr
}

// mockActuator implements MTXActuator (write surface; records enqueued commands).
type mockActuator struct {
	ensurePathCalls []struct {
		name string
		cfg  mediamtx.PathConfig
	}
	deletePathCalls []string
	setRecordCalls  []struct {
		path string
		on   bool
	}
}

func (m *mockActuator) EnqueueEnsurePath(name string, cfg mediamtx.PathConfig) {
	m.ensurePathCalls = append(m.ensurePathCalls, struct {
		name string
		cfg  mediamtx.PathConfig
	}{name, cfg})
}

func (m *mockActuator) EnqueueDeletePath(name string) {
	m.deletePathCalls = append(m.deletePathCalls, name)
}

func (m *mockActuator) EnqueueSetRecord(path string, on bool) {
	m.setRecordCalls = append(m.setRecordCalls, struct {
		path string
		on   bool
	}{path, on})
}

// ---------------------------------------------------------------------------
// Helper: build a reconciler with mocks
// ---------------------------------------------------------------------------

func newTestReconciler() (*Reconciler, *mockCameraRepo, *mockScheduleRepo, *mockRecRepo, *mockMTXReader, *mockActuator) {
	camRepo := &mockCameraRepo{}
	schRepo := &mockScheduleRepo{}
	recRepo := &mockRecRepo{}
	reader := &mockMTXReader{
		pathConfigs: make(map[string]mediamtx.PathConfigItem),
	}
	act := &mockActuator{}
	r := NewReconciler(camRepo, schRepo, recRepo, reader, act, "/recordings", "1h", "")
	return r, camRepo, schRepo, recRepo, reader, act
}

func ptr(s string) *string { return &s }

// ---------------------------------------------------------------------------
// Tests: reconcileStreams
// ---------------------------------------------------------------------------

func TestReconcileStreams_CreatesMissingPath(t *testing.T) {
	r, camRepo, _, _, reader, act := newTestReconciler()
	camRepo.cams = []model.Camera{
		{ID: "cam-1", MediaMTXPath: "cam-cam-1", StreamURL: "rtsp://1.2.3.4/stream"},
	}
	// MTX has no paths at all

	r.reconcileStreams(reader.pathConfigs)

	if len(act.ensurePathCalls) != 1 {
		t.Fatalf("expected 1 EnsurePath call, got %d", len(act.ensurePathCalls))
	}
	call := act.ensurePathCalls[0]
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
	r, camRepo, _, _, reader, act := newTestReconciler()
	camRepo.cams = []model.Camera{
		{ID: "cam-1", MediaMTXPath: "cam-cam-1", StreamURL: "rtsp://1.2.3.4/stream"},
	}
	reader.pathConfigs["cam-cam-1"] = mediamtx.PathConfigItem{
		Name:           "cam-cam-1",
		Source:         "rtsp://1.2.3.4/stream",
		SourceOnDemand: true,
		Record:         false,
	}

	r.reconcileStreams(reader.pathConfigs)

	if len(act.ensurePathCalls) != 0 {
		t.Errorf("expected 0 EnsurePath calls (path exists with matching config), got %d", len(act.ensurePathCalls))
	}
	if len(act.deletePathCalls) != 0 {
		t.Errorf("expected 0 DeletePath calls, got %d", len(act.deletePathCalls))
	}
	if len(camRepo.updatedIDs) != 0 {
		t.Errorf("expected no status update, got %v", camRepo.updatedIDs)
	}
}

func TestReconcileStreams_RecoversErrorCameraWithHealthyPath(t *testing.T) {
	r, camRepo, _, _, reader, act := newTestReconciler()
	camRepo.cams = []model.Camera{
		{ID: "cam-1", MediaMTXPath: "cam-cam-1", StreamURL: "rtsp://1.2.3.4/stream", Status: "error"},
	}
	// Path exists with matching config (e.g. a transient registration failure
	// whose async apply later succeeded) — camera still stuck in error.
	reader.pathConfigs["cam-cam-1"] = mediamtx.PathConfigItem{
		Name:           "cam-cam-1",
		Source:         "rtsp://1.2.3.4/stream",
		SourceOnDemand: true,
		Record:         false,
	}

	r.reconcileStreams(reader.pathConfigs)

	// Path is healthy, so no path operations are needed.
	if len(act.ensurePathCalls) != 0 {
		t.Errorf("expected 0 EnsurePath calls, got %d", len(act.ensurePathCalls))
	}
	if len(act.deletePathCalls) != 0 {
		t.Errorf("expected 0 DeletePath calls, got %d", len(act.deletePathCalls))
	}
	// But the stuck error status must be recovered to connecting.
	if len(camRepo.updatedIDs) != 1 || camRepo.updatedIDs[0] != "cam-1" {
		t.Fatalf("expected status update for cam-1, got %v", camRepo.updatedIDs)
	}
	if camRepo.updatedStats[0] != "connecting" {
		t.Errorf("status = %q, want 'connecting'", camRepo.updatedStats[0])
	}
}

func TestReconcileStreams_DetectsSourceDrift(t *testing.T) {
	r, camRepo, _, _, reader, act := newTestReconciler()
	camRepo.cams = []model.Camera{
		{ID: "cam-1", MediaMTXPath: "cam-cam-1", StreamURL: "rtsp://new-url/stream"},
	}
	// MTX has path with OLD source
	reader.pathConfigs["cam-cam-1"] = mediamtx.PathConfigItem{
		Name:           "cam-cam-1",
		Source:         "rtsp://old-url/stream",
		SourceOnDemand: true,
	}

	r.reconcileStreams(reader.pathConfigs)

	// Should delete the stale path
	if len(act.deletePathCalls) != 1 || act.deletePathCalls[0] != "cam-cam-1" {
		t.Fatalf("expected 1 DeletePath call for cam-cam-1, got %v", act.deletePathCalls)
	}
	// Should re-add with new source
	if len(act.ensurePathCalls) != 1 {
		t.Fatalf("expected 1 EnsurePath call, got %d", len(act.ensurePathCalls))
	}
	if act.ensurePathCalls[0].cfg.Source != "rtsp://new-url/stream" {
		t.Errorf("source = %q, want 'rtsp://new-url/stream'", act.ensurePathCalls[0].cfg.Source)
	}
}

// TestReconcileStreams_SkipsSourceOnDemandFalse verifies that sourceOnDemand=false does NOT
// trigger drift detection. sourceOnDemand is dynamically managed by recording logic
// (false while recording, true otherwise), so reconcileStreams must only compare source URLs.
func TestReconcileStreams_SkipsSourceOnDemandFalse(t *testing.T) {
	r, camRepo, _, _, reader, act := newTestReconciler()
	camRepo.cams = []model.Camera{
		{ID: "cam-1", MediaMTXPath: "cam-cam-1", StreamURL: "rtsp://1.2.3.4/stream"},
	}
	// Source matches, but SourceOnDemand=false (e.g. during recording).
	// This is NOT drift — reconcileRecording manages sourceOnDemand separately.
	reader.pathConfigs["cam-cam-1"] = mediamtx.PathConfigItem{
		Name:           "cam-cam-1",
		Source:         "rtsp://1.2.3.4/stream",
		SourceOnDemand: false,
	}

	r.reconcileStreams(reader.pathConfigs)

	if len(act.deletePathCalls) != 0 {
		t.Errorf("expected 0 DeletePath calls (sourceOnDemand drift is not checked), got %d", len(act.deletePathCalls))
	}
	if len(act.ensurePathCalls) != 0 {
		t.Errorf("expected 0 EnsurePath calls (sourceOnDemand=false is not drift), got %d", len(act.ensurePathCalls))
	}
	if len(camRepo.updatedIDs) != 0 {
		t.Errorf("expected no status update, got %v", camRepo.updatedIDs)
	}
}

func TestReconcileStreams_SkipsDisconnectedCamera(t *testing.T) {
	r, camRepo, _, _, reader, act := newTestReconciler()
	camRepo.cams = []model.Camera{
		{ID: "cam-1", MediaMTXPath: "cam-cam-1", StreamURL: "rtsp://1.2.3.4/stream", Status: "disconnected"},
	}
	// MTX still has the path (Disconnect API might have failed to delete it)
	reader.pathConfigs["cam-cam-1"] = mediamtx.PathConfigItem{
		Name:           "cam-cam-1",
		Source:         "rtsp://1.2.3.4/stream",
		SourceOnDemand: true,
	}

	r.reconcileStreams(reader.pathConfigs)

	// Should NOT create a new path for disconnected camera
	if len(act.ensurePathCalls) != 0 {
		t.Errorf("expected 0 EnsurePath calls for disconnected camera, got %d", len(act.ensurePathCalls))
	}
	// Should treat the stale path as orphan and delete it
	if len(act.deletePathCalls) != 1 || act.deletePathCalls[0] != "cam-cam-1" {
		t.Errorf("expected orphan deletion of cam-cam-1, got %v", act.deletePathCalls)
	}
	// Should NOT update status
	if len(camRepo.updatedIDs) != 0 {
		t.Errorf("expected no status update for disconnected camera, got %v", camRepo.updatedIDs)
	}
}

func TestReconcileStreams_RemovesOrphanPath(t *testing.T) {
	r, _, _, _, reader, act := newTestReconciler()
	// DB has no cameras, but MTX has orphan cam-* paths
	reader.pathConfigs["cam-orphan"] = mediamtx.PathConfigItem{Name: "cam-orphan"}
	reader.pathConfigs["cam-ghost"] = mediamtx.PathConfigItem{Name: "cam-ghost"}

	r.reconcileStreams(reader.pathConfigs)

	if len(act.deletePathCalls) != 2 {
		t.Fatalf("expected 2 DeletePath calls, got %d", len(act.deletePathCalls))
	}
	deleted := map[string]bool{}
	for _, name := range act.deletePathCalls {
		deleted[name] = true
	}
	if !deleted["cam-orphan"] || !deleted["cam-ghost"] {
		t.Errorf("expected cam-orphan and cam-ghost deleted, got %v", act.deletePathCalls)
	}
}

func TestReconcileStreams_DoesNotRemoveNonCamPaths(t *testing.T) {
	r, _, _, _, reader, act := newTestReconciler()
	reader.pathConfigs["playback-test"] = mediamtx.PathConfigItem{Name: "playback-test"}
	reader.pathConfigs["cam-orphan"] = mediamtx.PathConfigItem{Name: "cam-orphan"}

	r.reconcileStreams(reader.pathConfigs)

	if len(act.deletePathCalls) != 1 {
		t.Fatalf("expected 1 DeletePath call, got %d", len(act.deletePathCalls))
	}
	if act.deletePathCalls[0] != "cam-orphan" {
		t.Errorf("expected only cam-orphan deleted, got %q", act.deletePathCalls[0])
	}
}

func TestReconcileStreams_HandlesFindAllError(t *testing.T) {
	r, camRepo, _, _, reader, act := newTestReconciler()
	camRepo.findAllErr = errors.New("db down")

	r.reconcileStreams(reader.pathConfigs) // should not panic

	if len(act.ensurePathCalls) != 0 {
		t.Error("expected no EnsurePath calls when FindAll fails")
	}
}

// ---------------------------------------------------------------------------
// Tests: reconcileRecording — schedule transitions
// ---------------------------------------------------------------------------

func TestReconcileRecording_ScheduleStart(t *testing.T) {
	r, camRepo, schRepo, recRepo, _, act := newTestReconciler()
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

	r.reconcileRecording(emptyPathConfigs())

	// Should enqueue record on
	if len(act.setRecordCalls) != 1 {
		t.Fatalf("expected 1 SetRecord call, got %d", len(act.setRecordCalls))
	}
	if act.setRecordCalls[0].path != "cam-cam-1" {
		t.Errorf("path = %q", act.setRecordCalls[0].path)
	}
	if !act.setRecordCalls[0].on {
		t.Error("expected SetRecord(on=true)")
	}
	// Should create a session with trigger_type=schedule
	if recRepo.lastCreatedSession == nil {
		t.Fatal("expected session created")
	}
	if recRepo.lastCreatedSession.TriggerType != model.TriggerSchedule {
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
	r, camRepo, schRepo, recRepo, _, act := newTestReconciler()
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
		{ID: "ses-1", CameraID: "cam-1", TriggerType: model.TriggerSchedule, ScheduleID: &schedIDCopy},
	}

	r.reconcileRecording(emptyPathConfigs())

	// Time now is almost certainly past 00:01, so should stop
	foundStop := false
	for _, call := range act.setRecordCalls {
		if call.path == "cam-cam-1" && !call.on {
			foundStop = true
		}
	}
	if !foundStop {
		t.Error("expected SetRecord(on=false) for schedule stop")
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
	r, camRepo, schRepo, recRepo, _, act := newTestReconciler()
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
		{ID: "ses-manual", CameraID: "cam-1", TriggerType: model.TriggerManual},
	}

	r.reconcileRecording(emptyPathConfigs())

	// Should NOT stop recording (manual session active)
	for _, call := range act.setRecordCalls {
		if call.path == "cam-cam-1" && !call.on {
			t.Error("should not stop recording when manual session is active")
		}
	}
}

func TestReconcileRecording_ScheduleAlreadyStarted_NoNewPatch(t *testing.T) {
	r, camRepo, schRepo, _, _, act := newTestReconciler()
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

	r.reconcileRecording(emptyPathConfigs())

	for _, call := range act.setRecordCalls {
		if call.path == "cam-cam-1" && call.on {
			t.Error("should not enable recording when schedule already started and no drift")
		}
	}
}

func TestReconcileRecording_DriftRecovery(t *testing.T) {
	r, camRepo, _, recRepo, reader, act := newTestReconciler()
	camRepo.cams = []model.Camera{
		{ID: "cam-1", MediaMTXPath: "cam-cam-1", LicenseID: 1},
	}
	// Active session exists but MTX record=false (drift after MTX restart)
	recRepo.activeSessions = []model.RecordingSession{
		{ID: "ses-1", CameraID: "cam-1", TriggerType: model.TriggerManual},
	}
	reader.pathConfigs["cam-cam-1"] = mediamtx.PathConfigItem{
		Name:   "cam-cam-1",
		Record: false, // drift! session active but not recording
	}

	r.reconcileRecording(reader.pathConfigs)

	foundRecover := false
	for _, call := range act.setRecordCalls {
		if call.path == "cam-cam-1" && call.on {
			foundRecover = true
		}
	}
	if !foundRecover {
		t.Error("expected SetRecord(on=true) for drift recovery")
	}
}

func TestReconcileRecording_NoDriftWhenAlreadyRecording(t *testing.T) {
	r, camRepo, _, recRepo, reader, act := newTestReconciler()
	camRepo.cams = []model.Camera{
		{ID: "cam-1", MediaMTXPath: "cam-cam-1", LicenseID: 1},
	}
	recRepo.activeSessions = []model.RecordingSession{
		{ID: "ses-1", CameraID: "cam-1", TriggerType: model.TriggerManual},
	}
	reader.pathConfigs["cam-cam-1"] = mediamtx.PathConfigItem{
		Name:   "cam-cam-1",
		Record: true, // already recording, no drift
	}

	r.reconcileRecording(reader.pathConfigs)

	for _, call := range act.setRecordCalls {
		if call.path == "cam-cam-1" {
			t.Error("should not enqueue when already recording (no drift)")
		}
	}
}

// TestReconcileRecording_OrphanRecordingStoppedAfterHysteresis verifies the
// reverse-direction repair: a path that is recording (record=true) without any
// active session or in-window schedule is stopped after being observed for
// orphanRecordConfirmTicks consecutive cycles. This is the safety net that
// completes a stop whose MediaMTX apply failed after the session was closed.
func TestReconcileRecording_OrphanRecordingStoppedAfterHysteresis(t *testing.T) {
	r, camRepo, _, _, reader, act := newTestReconciler()
	camRepo.cams = []model.Camera{
		{ID: "cam-1", MediaMTXPath: "cam-cam-1", LicenseID: 1},
	}
	// No sessions, no schedules — but MediaMTX is recording.
	reader.pathConfigs["cam-cam-1"] = mediamtx.PathConfigItem{
		Name:   "cam-cam-1",
		Record: true,
	}

	// First observation: suspected, but not yet acted upon (hysteresis).
	r.reconcileRecording(reader.pathConfigs)
	for _, call := range act.setRecordCalls {
		if call.path == "cam-cam-1" && !call.on {
			t.Fatal("should not stop orphan recording on first observation")
		}
	}

	// Second consecutive observation: force stop.
	r.reconcileRecording(reader.pathConfigs)
	foundStop := false
	for _, call := range act.setRecordCalls {
		if call.path == "cam-cam-1" && !call.on {
			foundStop = true
		}
	}
	if !foundStop {
		t.Error("expected SetRecord(on=false) for orphan recording after hysteresis")
	}
}

// TestReconcileRecording_OrphanNotStoppedWhenSessionActive verifies that a
// recording backed by an active session is never treated as orphan, no matter
// how many cycles pass.
func TestReconcileRecording_OrphanNotStoppedWhenSessionActive(t *testing.T) {
	r, camRepo, _, recRepo, reader, act := newTestReconciler()
	camRepo.cams = []model.Camera{
		{ID: "cam-1", MediaMTXPath: "cam-cam-1", LicenseID: 1},
	}
	recRepo.activeSessions = []model.RecordingSession{
		{ID: "ses-1", CameraID: "cam-1", TriggerType: model.TriggerManual},
	}
	reader.pathConfigs["cam-cam-1"] = mediamtx.PathConfigItem{
		Name:   "cam-cam-1",
		Record: true,
	}

	for i := 0; i < 3; i++ {
		r.reconcileRecording(reader.pathConfigs)
	}
	for _, call := range act.setRecordCalls {
		if call.path == "cam-cam-1" && !call.on {
			t.Error("must not stop recording that has an active session")
		}
	}
}

// ---------------------------------------------------------------------------
// Tests: reconcile() — top-level orchestration
// ---------------------------------------------------------------------------

func TestReconcile_MTXDown_SkipsAll(t *testing.T) {
	r, camRepo, _, _, reader, act := newTestReconciler()
	camRepo.cams = []model.Camera{
		{ID: "cam-1", MediaMTXPath: "cam-cam-1", StreamURL: "rtsp://1.2.3.4/stream"},
	}
	reader.healthErr = errors.New("connection refused")

	r.reconcile()

	if len(act.ensurePathCalls) != 0 {
		t.Error("expected no EnsurePath calls when MTX is down")
	}
	if len(act.setRecordCalls) != 0 {
		t.Error("expected no SetRecord calls when MTX is down")
	}
	if !r.mtxDown {
		t.Error("expected mtxDown=true after health check failure")
	}
}

func TestReconcile_MTXRecovers_FullReconcile(t *testing.T) {
	r, camRepo, _, _, reader, act := newTestReconciler()
	camRepo.cams = []model.Camera{
		{ID: "cam-1", MediaMTXPath: "cam-cam-1", StreamURL: "rtsp://1.2.3.4/stream"},
	}

	// First reconcile: MTX is down
	reader.healthErr = errors.New("connection refused")
	r.reconcile()
	if !r.mtxDown {
		t.Fatal("expected mtxDown=true after first reconcile")
	}

	// Second reconcile: MTX recovered
	reader.healthErr = nil
	r.reconcile()

	if len(act.ensurePathCalls) != 1 {
		t.Fatalf("expected 1 EnsurePath call after MTX recovery, got %d", len(act.ensurePathCalls))
	}
	if r.mtxDown {
		t.Error("expected mtxDown=false after recovery")
	}
}

// ---------------------------------------------------------------------------
// Tests: reconcileCameraStatus — status protection
// ---------------------------------------------------------------------------

func TestReconcileCameraStatus_SkipsDisconnected(t *testing.T) {
	r, camRepo, _, _, _, _ := newTestReconciler()
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
	r, camRepo, _, _, _, _ := newTestReconciler()
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
	r, camRepo, _, _, _, _ := newTestReconciler()
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
	r, camRepo, _, _, _, _ := newTestReconciler()
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
	r, camRepo, _, _, _, _ := newTestReconciler()
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
	r, _, _, _, _, _ := newTestReconciler()
	r.Stop()
	r.Stop() // double stop should not panic
}

func TestReconcilerStopIdempotent(t *testing.T) {
	r, _, _, _, _, _ := newTestReconciler()

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

// emptyPathConfigs returns an empty path-config map for tests that only
// exercise schedule/session logic.
func emptyPathConfigs() map[string]mediamtx.PathConfigItem {
	return make(map[string]mediamtx.PathConfigItem)
}
