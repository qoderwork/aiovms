package recording

import (
	"context"
	"errors"
	"testing"
	"time"

	"aiovms/internal/model"
)

// --- mocks ---

type mockRepo struct {
	recs        map[string]*model.Recording
	findByIDErr error
	deleteErr   error
	findAllErr  error
	upsertErr   error

	lastDeleted *model.Recording
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

func (m *mockRepo) Create(rec *model.Recording) error                 { return nil }
func (m *mockRepo) Update(rec *model.Recording) error                 { return nil }
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
func (m *mockRepo) FindByPath(filePath string) (*model.Recording, error) { return nil, nil }
func (m *mockRepo) FindOlderThan(cutoff time.Time) ([]model.Recording, error) { return nil, nil }
func (m *mockRepo) FindAllSortedByTime() ([]model.Recording, error) { return nil, nil }

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
func (m *mockRepo) CloseSession(id string, endTime time.Time) error {
	m.lastClosedSessionID = id
	m.lastClosedAt = endTime
	return nil
}
func (m *mockRepo) FindSessionByCameraAndTime(cameraID string, t time.Time) (*model.RecordingSession, error) {
	return nil, errors.New("not found")
}

type mockCameraSvc struct {
	cams  map[string]*model.Camera
	getErr error
}

func (m *mockCameraSvc) Get(ctx context.Context, id string) (*model.Camera, error) {
	if m.getErr != nil {
		return nil, m.getErr
	}
	c, ok := m.cams[id]
	if !ok {
		return nil, errors.New("not found")
	}
	return c, nil
}

type mockMTX struct {
	patchPathCalls []struct{ name string; patch map[string]any }
	patchPathErr   error
}

func (m *mockMTX) PatchPath(name string, patch map[string]any) error {
	m.patchPathCalls = append(m.patchPathCalls, struct {
		name  string
		patch map[string]any
	}{name, patch})
	return m.patchPathErr
}

// --- tests ---

func TestRecordingList(t *testing.T) {
	repo := newMockRepo()
	repo.recs["r1"] = &model.Recording{ID: "r1", CameraID: "cam-1"}
	repo.recs["r2"] = &model.Recording{ID: "r2", CameraID: "cam-1"}

	svc := &service{repo: repo, camSvc: &mockCameraSvc{}, mtx: &mockMTX{}}
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
		ID: "r1", CameraID: "cam-1", Filename: "test.mp4",
	}
	svc := &service{repo: repo, camSvc: &mockCameraSvc{}, mtx: &mockMTX{}}

	rec, playURL, err := svc.Get(context.Background(), "r1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if rec.Filename != "test.mp4" {
		t.Errorf("filename = %q", rec.Filename)
	}
	if playURL != "/recordings/cam-1/test.mp4" {
		t.Errorf("playURL = %q", playURL)
	}
}

func TestRecordingGetNotFound(t *testing.T) {
	repo := newMockRepo()
	svc := &service{repo: repo, camSvc: &mockCameraSvc{}, mtx: &mockMTX{}}
	_, _, err := svc.Get(context.Background(), "no-such-id")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestRecordingDelete(t *testing.T) {
	repo := newMockRepo()
	repo.recs["r1"] = &model.Recording{ID: "r1", CameraID: "cam-1", FilePath: "/tmp/test.mp4"}
	svc := &service{repo: repo, camSvc: &mockCameraSvc{}, mtx: &mockMTX{}}

	err := svc.Delete(context.Background(), "r1")
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
	svc := &service{repo: repo, camSvc: &mockCameraSvc{}, mtx: &mockMTX{}}
	err := svc.Delete(context.Background(), "no-such-id")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestRecordingStartManual(t *testing.T) {
	repo := newMockRepo()
	camSvc := &mockCameraSvc{
		cams: map[string]*model.Camera{
			"cam-1": {ID: "cam-1", MediaMTXPath: "cam-cam-1", LicenseID: 1},
		},
	}
	mtx := &mockMTX{}
	svc := &service{repo: repo, camSvc: camSvc, mtx: mtx}

	err := svc.StartManual(context.Background(), "cam-1")
	if err != nil {
		t.Fatalf("StartManual: %v", err)
	}
	if len(mtx.patchPathCalls) != 1 {
		t.Fatalf("expected 1 PatchPath call, got %d", len(mtx.patchPathCalls))
	}
	call := mtx.patchPathCalls[0]
	if call.name != "cam-cam-1" {
		t.Errorf("patch name = %q, want 'cam-cam-1'", call.name)
	}
	if v, ok := call.patch["record"]; !ok || v != true {
		t.Error("expected patch record=true")
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
	svc := &service{repo: repo, camSvc: camSvc, mtx: &mockMTX{}}

	err := svc.StartManual(context.Background(), "cam-1")
	if err == nil {
		t.Fatal("expected error, got nil")
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
	mtx := &mockMTX{}
	svc := &service{repo: repo, camSvc: camSvc, mtx: mtx}

	err := svc.StopManual(context.Background(), "cam-1")
	if err != nil {
		t.Fatalf("StopManual: %v", err)
	}
	if len(mtx.patchPathCalls) != 1 {
		t.Fatalf("expected 1 PatchPath call, got %d", len(mtx.patchPathCalls))
	}
	if v, ok := mtx.patchPathCalls[0].patch["record"]; !ok || v != false {
		t.Error("expected patch record=false")
	}
	if repo.lastClosedSessionID != "ses-1" {
		t.Errorf("expected session ses-1 closed, got %q", repo.lastClosedSessionID)
	}
}

// TestRecordingRecover verifies that RecoverRecording re-applies record:true
// for all active sessions (end_time IS NULL) — the core of MTX restart recovery.
func TestRecordingRecover(t *testing.T) {
	repo := newMockRepo()
	repo.activeSessions = []model.RecordingSession{
		{ID: "ses-1", CameraID: "cam-a"},
		{ID: "ses-2", CameraID: "cam-b"},
	}
	camSvc := &mockCameraSvc{
		cams: map[string]*model.Camera{
			"cam-a": {ID: "cam-a", MediaMTXPath: "path-a"},
			"cam-b": {ID: "cam-b", MediaMTXPath: "path-b"},
		},
	}
	mtx := &mockMTX{}
	svc := &service{repo: repo, camSvc: camSvc, mtx: mtx}

	restored, failed := svc.RecoverRecording(context.Background())
	if restored != 2 || failed != 0 {
		t.Fatalf("expected (2,0), got (%d,%d)", restored, failed)
	}
	if len(mtx.patchPathCalls) != 2 {
		t.Fatalf("expected 2 PatchPath calls, got %d", len(mtx.patchPathCalls))
	}
	for _, c := range mtx.patchPathCalls {
		if v, ok := c.patch["record"]; !ok || v != true {
			t.Errorf("expected record=true for %s", c.name)
		}
	}
}

func TestRecordingUpsert(t *testing.T) {
	repo := newMockRepo()
	svc := &service{repo: repo, camSvc: &mockCameraSvc{}, mtx: &mockMTX{}}
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
