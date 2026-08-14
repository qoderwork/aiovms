package recording

import (
	"context"
	"errors"
	"testing"
	"time"

	"aiovms/internal/model"
	"aiovms/pkg/apperror"
)

// --- mocks ---

type mockRepo struct {
	recs        map[string]*model.Recording
	findByIDErr error
	deleteErr   error
	findAllErr  error
	upsertErr   error

	lastDeleted  *model.Recording
	lastUpserted *model.Recording

	// session mock state
	activeSessions      []model.RecordingSession
	createSessionErr    error
	findActiveErr       error
	lastCreatedSession  *model.RecordingSession
	lastClosedSessionID string
	lastClosedAt        time.Time
}

func newMockRepo() *mockRepo {
	return &mockRepo{recs: make(map[string]*model.Recording)}
}

func (m *mockRepo) Create(rec *model.Recording) error { return nil }
func (m *mockRepo) Update(rec *model.Recording) error { return nil }
func (m *mockRepo) Upsert(rec *model.Recording) error {
	if m.upsertErr != nil {
		return m.upsertErr
	}
	m.recs[rec.ID] = rec
	m.lastUpserted = rec
	return nil
}
func (m *mockRepo) FindByID(id string) (*model.Recording, error) {
	if m.findByIDErr != nil {
		return nil, m.findByIDErr
	}
	r, ok := m.recs[id]
	if !ok {
		return nil, errors.New("not found")
	}
	return r, nil
}
func (m *mockRepo) FindByIDAndTenant(id string, tenantID int64) (*model.Recording, error) {
	if m.findByIDErr != nil {
		return nil, m.findByIDErr
	}
	r, ok := m.recs[id]
	if !ok || r.LicenseID != tenantID {
		return nil, errors.New("not found")
	}
	return r, nil
}
func (m *mockRepo) FindAll(tenantID int64, cameraID, startTime, endTime string, offset, limit int) ([]model.Recording, int64, error) {
	if m.findAllErr != nil {
		return nil, 0, m.findAllErr
	}
	var out []model.Recording
	for _, r := range m.recs {
		out = append(out, *r)
	}
	return out, int64(len(out)), nil
}
func (m *mockRepo) Delete(rec *model.Recording) error {
	if m.deleteErr != nil {
		return m.deleteErr
	}
	delete(m.recs, rec.ID)
	m.lastDeleted = rec
	return nil
}
func (m *mockRepo) DeleteByIDs(ids []string) error {
	for _, id := range ids {
		delete(m.recs, id)
	}
	return nil
}
func (m *mockRepo) FindByPath(filePath string) (*model.Recording, error)      { return nil, nil }
func (m *mockRepo) FindOlderThan(cutoff time.Time) ([]model.Recording, error) { return nil, nil }
func (m *mockRepo) FindOlderThanByStatus(cutoff time.Time, status string) ([]model.Recording, error) {
	return nil, nil
}
func (m *mockRepo) FindOldestComplete(limit int) ([]model.Recording, error) { return nil, nil }
func (m *mockRepo) FindAllSortedByTime() ([]model.Recording, error)         { return nil, nil }

// --- session mock impl ---

func (m *mockRepo) CreateSession(sess *model.RecordingSession) error {
	if m.createSessionErr != nil {
		return m.createSessionErr
	}
	m.lastCreatedSession = sess
	return nil
}
func (m *mockRepo) FindActiveSessions() ([]model.RecordingSession, error) {
	return m.activeSessions, m.findActiveErr
}
func (m *mockRepo) FindActiveSessionByCamera(cameraID string) (*model.RecordingSession, error) {
	for i := range m.activeSessions {
		if m.activeSessions[i].CameraID == cameraID {
			return &m.activeSessions[i], nil
		}
	}
	return nil, errors.New("not found")
}
func (m *mockRepo) FindActiveSessionBySchedule(scheduleID string) (*model.RecordingSession, error) {
	for i := range m.activeSessions {
		if m.activeSessions[i].ScheduleID != nil && *m.activeSessions[i].ScheduleID == scheduleID {
			return &m.activeSessions[i], nil
		}
	}
	return nil, errors.New("not found")
}
func (m *mockRepo) CloseSession(id string, endTime time.Time) error {
	m.lastClosedSessionID = id
	m.lastClosedAt = endTime
	return nil
}
func (m *mockRepo) FindSessionByCameraAndTime(cameraID string, t time.Time) (*model.RecordingSession, error) {
	return nil, errors.New("not found")
}

type mockCameraSvc struct {
	cams   map[string]*model.Camera
	getErr error
}

func (m *mockCameraSvc) Get(ctx context.Context, tenantID int64, id string) (*model.Camera, error) {
	if m.getErr != nil {
		return nil, m.getErr
	}
	c, ok := m.cams[id]
	if !ok || c.LicenseID != tenantID {
		return nil, errors.New("not found")
	}
	return c, nil
}

type mockActuator struct {
	setRecordCalls []struct {
		path string
		on   bool
	}
}

func (m *mockActuator) EnqueueSetRecord(path string, on bool) {
	m.setRecordCalls = append(m.setRecordCalls, struct {
		path string
		on   bool
	}{path, on})
}

// --- tests ---

func TestRecordingList(t *testing.T) {
	repo := newMockRepo()
	repo.recs["r1"] = &model.Recording{ID: "r1", CameraID: "cam-1"}
	repo.recs["r2"] = &model.Recording{ID: "r2", CameraID: "cam-1"}

	svc := &service{repo: repo, camSvc: &mockCameraSvc{}, act: &mockActuator{}}
	recs, total, err := svc.List(context.Background(), 0, "", "", "", 1, 10)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if total != 2 {
		t.Errorf("expected 2, got %d", total)
	}
	if len(recs) != 2 {
		t.Errorf("expected 2 results, got %d", len(recs))
	}
}

func TestRecordingGet(t *testing.T) {
	repo := newMockRepo()
	repo.recs["r1"] = &model.Recording{
		ID: "r1", CameraID: "cam-1", MediaMTXPath: "cam-1", Filename: "test.mp4",
	}
	svc := &service{repo: repo, camSvc: &mockCameraSvc{}, act: &mockActuator{}}

	rec, playURL, err := svc.Get(context.Background(), 0, "r1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if rec.Filename != "test.mp4" {
		t.Errorf("filename = %q", rec.Filename)
	}
	if playURL != "/recordings/files/cam-1/test.mp4" {
		t.Errorf("playURL = %q", playURL)
	}
}

func TestRecordingGetNotFound(t *testing.T) {
	repo := newMockRepo()
	svc := &service{repo: repo, camSvc: &mockCameraSvc{}, act: &mockActuator{}}
	_, _, err := svc.Get(context.Background(), 0, "no-such-id")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

// TestRecordingGetCrossTenantForbidden verifies that reading another tenant's
// recording returns 403 (design doc: 越权返回 403).
func TestRecordingGetCrossTenantForbidden(t *testing.T) {
	repo := newMockRepo()
	repo.recs["r1"] = &model.Recording{ID: "r1", CameraID: "cam-1", LicenseID: 100}
	svc := &service{repo: repo, camSvc: &mockCameraSvc{}, act: &mockActuator{}}
	_, _, err := svc.Get(context.Background(), 200, "r1")
	if err == nil {
		t.Fatal("expected forbidden error, got nil")
	}
	appErr, ok := err.(*apperror.AppError)
	if !ok || appErr.StatusCode != 403 {
		t.Errorf("expected 403 AppError, got %v", err)
	}
}

func TestRecordingDelete(t *testing.T) {
	repo := newMockRepo()
	repo.recs["r1"] = &model.Recording{ID: "r1", CameraID: "cam-1", FilePath: "/tmp/test.mp4"}
	svc := &service{repo: repo, camSvc: &mockCameraSvc{}, act: &mockActuator{}}

	err := svc.Delete(context.Background(), 0, "r1")
	if err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if repo.lastDeleted == nil {
		t.Fatal("expected recording deleted from repo")
	}
	if repo.lastDeleted.ID != "r1" {
		t.Errorf("deleted id = %q", repo.lastDeleted.ID)
	}
}

func TestRecordingDeleteNotFound(t *testing.T) {
	repo := newMockRepo()
	svc := &service{repo: repo, camSvc: &mockCameraSvc{}, act: &mockActuator{}}
	err := svc.Delete(context.Background(), 0, "no-such-id")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

// TestRecordingDeleteCrossTenantForbidden verifies that deleting another
// tenant's recording returns 403 and leaves the record intact.
func TestRecordingDeleteCrossTenantForbidden(t *testing.T) {
	repo := newMockRepo()
	repo.recs["r1"] = &model.Recording{ID: "r1", CameraID: "cam-1", LicenseID: 100}
	svc := &service{repo: repo, camSvc: &mockCameraSvc{}, act: &mockActuator{}}
	err := svc.Delete(context.Background(), 200, "r1")
	if err == nil {
		t.Fatal("expected forbidden error, got nil")
	}
	if _, exists := repo.recs["r1"]; !exists {
		t.Error("recording was deleted across tenants")
	}
}

func TestRecordingStartManual(t *testing.T) {
	repo := newMockRepo()
	camSvc := &mockCameraSvc{
		cams: map[string]*model.Camera{
			"cam-1": {ID: "cam-1", MediaMTXPath: "cam-cam-1", LicenseID: 1},
		},
	}
	act := &mockActuator{}
	svc := &service{repo: repo, camSvc: camSvc, act: act}

	err := svc.StartManual(context.Background(), 1, "cam-1")
	if err != nil {
		t.Fatalf("StartManual: %v", err)
	}
	if len(act.setRecordCalls) != 1 {
		t.Fatalf("expected 1 SetRecord call, got %d", len(act.setRecordCalls))
	}
	call := act.setRecordCalls[0]
	if call.path != "cam-cam-1" {
		t.Errorf("path = %q, want 'cam-cam-1'", call.path)
	}
	if !call.on {
		t.Error("expected SetRecord(on=true)")
	}
	// Session should be created with trigger_type=manual and no end_time.
	if repo.lastCreatedSession == nil {
		t.Fatal("expected session created")
	}
	if repo.lastCreatedSession.TriggerType != "manual" {
		t.Errorf("trigger = %q", repo.lastCreatedSession.TriggerType)
	}
	if repo.lastCreatedSession.EndTime != nil {
		t.Error("expected end_time nil for active session")
	}
}

func TestRecordingStartManualCameraNotFound(t *testing.T) {
	repo := newMockRepo()
	camSvc := &mockCameraSvc{cams: make(map[string]*model.Camera)}
	svc := &service{repo: repo, camSvc: camSvc, act: &mockActuator{}}

	err := svc.StartManual(context.Background(), 1, "cam-1")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

// TestRecordingStartManualCrossTenant verifies that starting a recording on
// another tenant's camera is rejected.
func TestRecordingStartManualCrossTenant(t *testing.T) {
	repo := newMockRepo()
	camSvc := &mockCameraSvc{
		cams: map[string]*model.Camera{
			"cam-1": {ID: "cam-1", MediaMTXPath: "cam-cam-1", LicenseID: 1},
		},
	}
	act := &mockActuator{}
	svc := &service{repo: repo, camSvc: camSvc, act: act}

	err := svc.StartManual(context.Background(), 2, "cam-1")
	if err == nil {
		t.Fatal("expected error for cross-tenant camera, got nil")
	}
	if len(act.setRecordCalls) != 0 {
		t.Errorf("expected no SetRecord call on cross-tenant start, got %d", len(act.setRecordCalls))
	}
	if repo.lastCreatedSession != nil {
		t.Error("expected no session created on cross-tenant start")
	}
}

func TestRecordingStopManual(t *testing.T) {
	repo := newMockRepo()
	repo.activeSessions = []model.RecordingSession{
		{ID: "ses-1", CameraID: "cam-1", TriggerType: "manual"},
	}
	camSvc := &mockCameraSvc{
		cams: map[string]*model.Camera{
			"cam-1": {ID: "cam-1", MediaMTXPath: "cam-cam-1"},
		},
	}
	act := &mockActuator{}
	svc := &service{repo: repo, camSvc: camSvc, act: act}

	err := svc.StopManual(context.Background(), 0, "cam-1")
	if err != nil {
		t.Fatalf("StopManual: %v", err)
	}
	if len(act.setRecordCalls) != 1 {
		t.Fatalf("expected 1 SetRecord call, got %d", len(act.setRecordCalls))
	}
	if act.setRecordCalls[0].on {
		t.Error("expected SetRecord(on=false)")
	}
	if repo.lastClosedSessionID != "ses-1" {
		t.Errorf("expected session ses-1 closed, got %q", repo.lastClosedSessionID)
	}
}

// TestRecordingStopManualIntentCommit pins the intent-commit contract: the
// stop succeeds once the session is closed. The MediaMTX apply is async and
// its outcome never surfaces as an API error — convergence is the
// reconciler's job (orphan repair), observable via vms_drift_events_total.
// This replaced the old "error when patch fails" test: that behavior was a
// semantic bug (spurious "stop failed" while the stop was converging).
func TestRecordingStopManualIntentCommit(t *testing.T) {
	repo := newMockRepo()
	repo.activeSessions = []model.RecordingSession{
		{ID: "ses-1", CameraID: "cam-1", TriggerType: "manual"},
	}
	camSvc := &mockCameraSvc{
		cams: map[string]*model.Camera{
			"cam-1": {ID: "cam-1", MediaMTXPath: "cam-cam-1"},
		},
	}
	act := &mockActuator{}
	svc := &service{repo: repo, camSvc: camSvc, act: act}

	if err := svc.StopManual(context.Background(), 0, "cam-1"); err != nil {
		t.Fatalf("StopManual must not fail on MTX apply issues: %v", err)
	}
	// Session closed = stop committed.
	if repo.lastClosedSessionID != "ses-1" {
		t.Errorf("expected session ses-1 closed, got %q", repo.lastClosedSessionID)
	}
	// Stop command still enqueued for MTX convergence.
	if len(act.setRecordCalls) != 1 || act.setRecordCalls[0].on {
		t.Errorf("expected record-off enqueued, got %+v", act.setRecordCalls)
	}
}

// TestRecordingStopManualNoSessionStillPatches verifies that stopping when no
// active session exists still patches MediaMTX (idempotent cleanup of a
// possible orphan recording state).
func TestRecordingStopManualNoSessionStillPatches(t *testing.T) {
	repo := newMockRepo()
	camSvc := &mockCameraSvc{
		cams: map[string]*model.Camera{
			"cam-1": {ID: "cam-1", MediaMTXPath: "cam-cam-1"},
		},
	}
	act := &mockActuator{}
	svc := &service{repo: repo, camSvc: camSvc, act: act}

	if err := svc.StopManual(context.Background(), 0, "cam-1"); err != nil {
		t.Fatalf("StopManual: %v", err)
	}
	if len(act.setRecordCalls) != 1 {
		t.Fatalf("expected 1 SetRecord call, got %d", len(act.setRecordCalls))
	}
	if act.setRecordCalls[0].on {
		t.Error("expected SetRecord(on=false)")
	}
}

func TestRecordingUpsert(t *testing.T) {
	repo := newMockRepo()
	svc := &service{repo: repo, camSvc: &mockCameraSvc{}, act: &mockActuator{}}
	rec := &model.Recording{
		CameraID:  "cam-1",
		Filename:  "2024-01-01_10-00-00.mp4",
		FilePath:  "/recordings/cam-1/2024-01-01_10-00-00.mp4",
		StartTime: time.Now(),
	}
	err := svc.Upsert(context.Background(), rec)
	if err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if repo.lastUpserted == nil {
		t.Fatal("expected recording upserted")
	}
	if repo.lastUpserted.ID == "" {
		t.Error("expected non-empty ID")
	}
	if repo.lastUpserted.Filename != rec.Filename {
		t.Errorf("filename = %q", repo.lastUpserted.Filename)
	}
}
