package camera

import (
	"context"
	"errors"
	"os"
	"testing"

	"aiovms/internal/mediamtx"
	"aiovms/internal/model"
	"aiovms/pkg/crypto"
)

// --- mocks ---

type mockRepo struct {
	cams        map[string]*model.Camera
	createErr   error
	findByIDErr error
	updateErr   error
	deleteErr   error
	listErr     error
	findAllErr  error

	lastCreated *model.Camera
	lastUpdated *model.Camera
	lastDeleted string
}

func newMockRepo() *mockRepo {
	return &mockRepo{cams: make(map[string]*model.Camera)}
}

func (m *mockRepo) Create(cam *model.Camera) error {
	if m.createErr != nil {
		return m.createErr
	}
	m.cams[cam.ID] = cam
	m.lastCreated = cam
	return nil
}
func (m *mockRepo) Update(cam *model.Camera) error {
	if m.updateErr != nil {
		return m.updateErr
	}
	m.cams[cam.ID] = cam
	m.lastUpdated = cam
	return nil
}
func (m *mockRepo) FindByID(id string) (*model.Camera, error) {
	if m.findByIDErr != nil {
		return nil, m.findByIDErr
	}
	c, ok := m.cams[id]
	if !ok {
		return nil, errors.New("not found")
	}
	return c, nil
}
func (m *mockRepo) ListByTenant(tenantID int64, query string, offset, limit int) ([]model.Camera, int64, error) {
	if m.listErr != nil {
		return nil, 0, m.listErr
	}
	var out []model.Camera
	for _, c := range m.cams {
		if c.LicenseID == tenantID {
			out = append(out, *c)
		}
	}
	return out, int64(len(out)), nil
}
func (m *mockRepo) Delete(id string) error {
	if m.deleteErr != nil {
		return m.deleteErr
	}
	delete(m.cams, id)
	m.lastDeleted = id
	return nil
}
func (m *mockRepo) FindAllByTenant(_ int64) ([]model.Camera, error) { return nil, nil }
func (m *mockRepo) DeleteAllByTenant(_ int64) (int64, error)       { return 0, nil }
func (m *mockRepo) ExistsByIPPort(_ int64, _ string, _ int, _ string) (bool, error) {
	return false, nil
}
func (m *mockRepo) ExistsByName(_ int64, _ string, _ string) (bool, error)    { return false, nil }
func (m *mockRepo) ExistsByStreamURL(_ int64, _ string, _ string) (bool, error) { return false, nil }
func (m *mockRepo) FindAll() ([]model.Camera, error) {
	if m.findAllErr != nil {
		return nil, m.findAllErr
	}
	var out []model.Camera
	for _, c := range m.cams {
		out = append(out, *c)
	}
	return out, nil
}
func (m *mockRepo) UpdateStatus(id string, status string) error {
	if c, ok := m.cams[id]; ok {
		c.Status = status
	}
	return nil
}
func (m *mockRepo) FindByMediaMTXPath(path string) (*model.Camera, error) {
	for _, c := range m.cams {
		if c.MediaMTXPath == path {
			return c, nil
		}
	}
	return nil, errors.New("not found")
}

type mockMTX struct {
	addPathCalls    []struct{ name string; cfg mediamtx.PathConfig }
	addPathErr      error
	deletePathCalls []string
	deletePathErr   error
}

func (m *mockMTX) AddPath(name string, cfg mediamtx.PathConfig) error {
	m.addPathCalls = append(m.addPathCalls, struct {
		name string
		cfg  mediamtx.PathConfig
	}{name, cfg})
	return m.addPathErr
}
func (m *mockMTX) DeletePath(name string) error {
	m.deletePathCalls = append(m.deletePathCalls, name)
	return m.deletePathErr
}
func (m *mockMTX) SnapshotPath(name string) string { return "/snapshot/" + name }

// --- helpers ---

func initCrypto(t *testing.T) {
	t.Helper()
	os.Setenv("VMS_ENCRYPTION_KEY", "test-key-32-bytes-long-123456789")
	if err := crypto.Init(); err != nil {
		t.Fatalf("crypto.Init: %v", err)
	}
}

// --- tests ---

func TestServiceList(t *testing.T) {
	repo := newMockRepo()
	repo.cams["a"] = &model.Camera{ID: "a", Name: "cam-a", LicenseID: 100}
	repo.cams["b"] = &model.Camera{ID: "b", Name: "cam-b", LicenseID: 100}
	repo.cams["c"] = &model.Camera{ID: "c", Name: "cam-c", LicenseID: 200}

	svc := &service{repo: repo, mtx: &mockMTX{}}
	cams, total, err := svc.List(context.Background(), 100, "", 1, 10)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if total != 2 {
		t.Errorf("expected 2, got %d", total)
	}
	if len(cams) != 2 {
		t.Errorf("expected 2 results, got %d", len(cams))
	}
}

func TestServiceGetFound(t *testing.T) {
	repo := newMockRepo()
	repo.cams["id-1"] = &model.Camera{ID: "id-1", Name: "test-cam"}
	svc := &service{repo: repo, mtx: &mockMTX{}}
	cam, err := svc.Get(context.Background(), "id-1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if cam.Name != "test-cam" {
		t.Errorf("name = %q, want 'test-cam'", cam.Name)
	}
}

func TestServiceGetNotFound(t *testing.T) {
	repo := newMockRepo()
	svc := &service{repo: repo, mtx: &mockMTX{}}
	_, err := svc.Get(context.Background(), "no-such-id")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestServiceCreate(t *testing.T) {
	initCrypto(t)
	repo := newMockRepo()
	mtx := &mockMTX{}
	svc := &service{repo: repo, mtx: mtx}

	err := svc.Create(context.Background(), &model.Camera{
		Name:     "new-cam",
		IP:       "192.168.1.100",
		Port:     554,
		Protocol: "RTSP",
		StreamURL: "rtsp://192.168.1.100/stream",
		Password: "secret123",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if repo.lastCreated == nil {
		t.Fatal("expected camera created")
	}
	if repo.lastCreated.Name != "new-cam" {
		t.Errorf("name = %q", repo.lastCreated.Name)
	}
	if repo.lastCreated.ID == "" {
		t.Error("expected non-empty ID")
	}
	if repo.lastCreated.MediaMTXPath == "" {
		t.Error("expected non-empty MediaMTXPath")
	}
	if repo.lastCreated.PasswordEnc == "" {
		t.Error("expected encrypted password")
	}
	if repo.lastCreated.Status != "connecting" {
		t.Errorf("status = %q, want 'connecting'", repo.lastCreated.Status)
	}
	if len(mtx.addPathCalls) != 1 {
		t.Errorf("expected 1 AddPath call, got %d", len(mtx.addPathCalls))
	}
}

func TestServiceUpdate(t *testing.T) {
	initCrypto(t)
	repo := newMockRepo()
	repo.cams["id-1"] = &model.Camera{
		ID: "id-1", Name: "old-name", IP: "10.0.0.1",
		MediaMTXPath: "cam-id-1",
	}
	svc := &service{repo: repo, mtx: &mockMTX{}}

	err := svc.Update(context.Background(), "id-1", &model.Camera{
		Name:     "new-name",
		IP:       "10.0.0.2",
		Port:     554,
		Protocol: "RTSP",
		StreamURL: "rtsp://10.0.0.2/stream",
		Password: "newpass",
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if repo.lastUpdated == nil {
		t.Fatal("expected camera updated")
	}
	if repo.lastUpdated.Name != "new-name" {
		t.Errorf("name = %q, want 'new-name'", repo.lastUpdated.Name)
	}
	if repo.lastUpdated.PasswordEnc == "" {
		t.Error("expected re-encrypted password")
	}
}

func TestServiceUpdateNotFound(t *testing.T) {
	repo := newMockRepo()
	svc := &service{repo: repo, mtx: &mockMTX{}}
	err := svc.Update(context.Background(), "no-such-id", &model.Camera{Name: "x"})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestServiceDelete(t *testing.T) {
	repo := newMockRepo()
	repo.cams["id-1"] = &model.Camera{ID: "id-1", MediaMTXPath: "cam-id-1"}
	mtx := &mockMTX{}
	svc := &service{repo: repo, mtx: mtx}

	err := svc.Delete(context.Background(), "id-1")
	if err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if repo.lastDeleted != "id-1" {
		t.Errorf("deleted = %q, want 'id-1'", repo.lastDeleted)
	}
	if len(mtx.deletePathCalls) != 1 {
		t.Errorf("expected 1 DeletePath call, got %d", len(mtx.deletePathCalls))
	}
}

func TestServiceDeleteNotFound(t *testing.T) {
	repo := newMockRepo()
	svc := &service{repo: repo, mtx: &mockMTX{}}
	err := svc.Delete(context.Background(), "no-such-id")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestServiceConnect(t *testing.T) {
	repo := newMockRepo()
	repo.cams["id-1"] = &model.Camera{
		ID: "id-1", MediaMTXPath: "cam-id-1",
		StreamURL: "rtsp://10.0.0.1/stream",
	}
	svc := &service{repo: repo, mtx: &mockMTX{}}

	err := svc.Connect(context.Background(), "id-1")
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	cam, _ := repo.FindByID("id-1")
	if cam.Status != "connecting" {
		t.Errorf("status = %q, want 'connecting'", cam.Status)
	}
}

func TestServiceDisconnect(t *testing.T) {
	repo := newMockRepo()
	repo.cams["id-1"] = &model.Camera{
		ID: "id-1", MediaMTXPath: "cam-id-1", Status: "online",
	}
	mtx := &mockMTX{}
	svc := &service{repo: repo, mtx: mtx}

	err := svc.Disconnect(context.Background(), "id-1")
	if err != nil {
		t.Fatalf("Disconnect: %v", err)
	}
	cam, _ := repo.FindByID("id-1")
	if cam.Status != "disconnected" {
		t.Errorf("status = %q, want 'disconnected'", cam.Status)
	}
}

func TestServiceGetStreamURLs(t *testing.T) {
	repo := newMockRepo()
	repo.cams["id-1"] = &model.Camera{ID: "id-1", MediaMTXPath: "cam-id-1"}
	svc := &service{repo: repo, mtx: &mockMTX{}}

	urls, err := svc.GetStreamURLs(context.Background(), "id-1")
	if err != nil {
		t.Fatalf("GetStreamURLs: %v", err)
	}
	if urls.FLV != "/live/cam-id-1/stream.flv" {
		t.Errorf("FLV = %q", urls.FLV)
	}
	if urls.HLS != "/stream/cam-id-1/index.m3u8" {
		t.Errorf("HLS = %q", urls.HLS)
	}
	if urls.WebRTC != "/stream/cam-id-1/" {
		t.Errorf("WebRTC = %q", urls.WebRTC)
	}
}

func TestServiceSnapshot(t *testing.T) {
	repo := newMockRepo()
	repo.cams["id-1"] = &model.Camera{ID: "id-1", MediaMTXPath: "cam-id-1"}
	svc := &service{repo: repo, mtx: &mockMTX{}}

	result, err := svc.Snapshot(context.Background(), "id-1")
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if result.CameraID != "id-1" {
		t.Errorf("CameraID = %q", result.CameraID)
	}
}

func TestServiceListStatuses(t *testing.T) {
	repo := newMockRepo()
	repo.cams["a"] = &model.Camera{ID: "a", Status: "online", LicenseID: 100}
	repo.cams["b"] = &model.Camera{ID: "b", Status: "offline", LicenseID: 200}
	svc := &service{repo: repo, mtx: &mockMTX{}}

	statuses, err := svc.ListStatuses(context.Background())
	if err != nil {
		t.Fatalf("ListStatuses: %v", err)
	}
	if len(statuses) != 2 {
		t.Errorf("expected 2 statuses, got %d", len(statuses))
	}
}

func TestServiceConnectNotFound(t *testing.T) {
	repo := newMockRepo()
	svc := &service{repo: repo, mtx: &mockMTX{}}
	err := svc.Connect(context.Background(), "no-such-id")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestServiceDisconnectNotFound(t *testing.T) {
	repo := newMockRepo()
	svc := &service{repo: repo, mtx: &mockMTX{}}
	err := svc.Disconnect(context.Background(), "no-such-id")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}
