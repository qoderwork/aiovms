package schedule

import (
	"context"
	"errors"
	"testing"

	"aiovms/internal/model"
)

func TestValidateTimeRange(t *testing.T) {
	tests := []struct {
		name      string
		start     string
		end       string
		wantError bool
	}{
		{"valid range", "08:00", "20:00", false},
		{"start equals end", "08:00", "08:00", true},
		{"start after end", "20:00", "08:00", true},
		{"empty both", "", "", false},
		{"empty start", "", "20:00", false},
		{"empty end", "08:00", "", false},
		{"midnight range", "00:00", "23:59", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateTimeRange(tt.start, tt.end)
			hasErr := err != nil
			if hasErr != tt.wantError {
				t.Errorf("validateTimeRange(%q, %q) error = %v, wantError = %v", tt.start, tt.end, err, tt.wantError)
			}
		})
	}
}

// --- Service test mocks ---

type mockRepo struct {
	schedules   map[string]*model.RecordSchedule
	createErr   error
	findByIDErr error
	updateErr   error
	deleteErr   error

	lastCreated *model.RecordSchedule
	lastUpdated *model.RecordSchedule
	lastDeleted string
}

func newMockRepo() *mockRepo {
	return &mockRepo{schedules: make(map[string]*model.RecordSchedule)}
}

func (m *mockRepo) Create(sch *model.RecordSchedule) error {
	if m.createErr != nil {
		return m.createErr
	}
	m.schedules[sch.ID] = sch
	m.lastCreated = sch
	return nil
}
func (m *mockRepo) Update(sch *model.RecordSchedule) error {
	if m.updateErr != nil {
		return m.updateErr
	}
	m.schedules[sch.ID] = sch
	m.lastUpdated = sch
	return nil
}
func (m *mockRepo) FindAll(tenantID int64, cameraID string) ([]model.RecordSchedule, error) {
	var out []model.RecordSchedule
	for _, s := range m.schedules {
		out = append(out, *s)
	}
	return out, nil
}
func (m *mockRepo) FindByID(id string) (*model.RecordSchedule, error) {
	if m.findByIDErr != nil {
		return nil, m.findByIDErr
	}
	s, ok := m.schedules[id]
	if !ok {
		return nil, errors.New("not found")
	}
	return s, nil
}
func (m *mockRepo) Delete(id string) error {
	if m.deleteErr != nil {
		return m.deleteErr
	}
	delete(m.schedules, id)
	m.lastDeleted = id
	return nil
}
func (m *mockRepo) FindAllEnabled() ([]model.RecordSchedule, error) { return nil, nil }

func newTestService() (*service, *mockRepo) {
	repo := newMockRepo()
	svc := &service{repo: repo}
	return svc, repo
}

// --- Service tests ---

func TestScheduleGet(t *testing.T) {
	svc, repo := newTestService()
	repo.schedules["s1"] = &model.RecordSchedule{
		ID: "s1", Name: "mon-fri", Enabled: true,
	}

	sch, err := svc.Get(context.Background(), "s1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if sch.Name != "mon-fri" {
		t.Errorf("name = %q", sch.Name)
	}
}

func TestScheduleGetNotFound(t *testing.T) {
	svc, _ := newTestService()
	_, err := svc.Get(context.Background(), "no-such-id")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestScheduleCreate(t *testing.T) {
	svc, repo := newTestService()

	err := svc.Create(context.Background(), &model.RecordSchedule{
		CameraID:  "cam-1",
		Name:      "work-hours",
		StartTime: "09:00",
		EndTime:   "18:00",
		Weekdays:  "1,2,3,4,5",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if repo.lastCreated == nil {
		t.Fatal("expected schedule created")
	}
	if repo.lastCreated.ID == "" {
		t.Error("expected non-empty ID")
	}
	if repo.lastCreated.Name != "work-hours" {
		t.Errorf("name = %q", repo.lastCreated.Name)
	}
}

func TestScheduleCreateInvalidRange(t *testing.T) {
	svc, _ := newTestService()

	err := svc.Create(context.Background(), &model.RecordSchedule{
		Name:      "bad-range",
		StartTime: "18:00",
		EndTime:   "09:00",
	})
	if err == nil {
		t.Fatal("expected error for start > end, got nil")
	}
}

func TestScheduleUpdate(t *testing.T) {
	svc, repo := newTestService()
	repo.schedules["s1"] = &model.RecordSchedule{
		ID: "s1", Name: "old", StartTime: "00:00", EndTime: "23:59",
	}

	err := svc.Update(context.Background(), "s1", &model.RecordSchedule{
		Name:      "new-name",
		Enabled:   false,
		StartTime: "09:00",
		EndTime:   "18:00",
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if repo.lastUpdated.Name != "new-name" {
		t.Errorf("name = %q", repo.lastUpdated.Name)
	}
	if repo.lastUpdated.Enabled {
		t.Error("expected disabled")
	}
}

func TestScheduleUpdateNotFound(t *testing.T) {
	svc, _ := newTestService()
	err := svc.Update(context.Background(), "no-such-id", &model.RecordSchedule{
		Name: "x", StartTime: "00:00", EndTime: "23:59",
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestScheduleDelete(t *testing.T) {
	svc, repo := newTestService()
	repo.schedules["s1"] = &model.RecordSchedule{ID: "s1"}

	err := svc.Delete(context.Background(), "s1")
	if err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if repo.lastDeleted != "s1" {
		t.Errorf("deleted = %q, want 's1'", repo.lastDeleted)
	}
}

func TestScheduleDeleteNotFound(t *testing.T) {
	svc, _ := newTestService()
	err := svc.Delete(context.Background(), "no-such-id")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestScheduleToggle(t *testing.T) {
	svc, repo := newTestService()
	repo.schedules["s1"] = &model.RecordSchedule{
		ID: "s1", Name: "test", Enabled: true,
	}

	sch, err := svc.Toggle(context.Background(), "s1")
	if err != nil {
		t.Fatalf("Toggle: %v", err)
	}
	if sch.Enabled {
		t.Error("expected disabled after toggle")
	}

	// toggle back
	sch, err = svc.Toggle(context.Background(), "s1")
	if err != nil {
		t.Fatalf("Toggle again: %v", err)
	}
	if !sch.Enabled {
		t.Error("expected enabled after second toggle")
	}
}

func TestScheduleToggleNotFound(t *testing.T) {
	svc, _ := newTestService()
	_, err := svc.Toggle(context.Background(), "no-such-id")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestScheduleList(t *testing.T) {
	svc, repo := newTestService()
	repo.schedules["s1"] = &model.RecordSchedule{ID: "s1", Name: "a"}
	repo.schedules["s2"] = &model.RecordSchedule{ID: "s2", Name: "b"}

	schedules, err := svc.List(context.Background(), 0, "")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(schedules) != 2 {
		t.Errorf("expected 2 schedules, got %d", len(schedules))
	}
}
