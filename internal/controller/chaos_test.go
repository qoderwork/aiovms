package controller

import (
	"errors"
	"fmt"
	"math/rand"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"aiovms/internal/mediamtx"
	"aiovms/internal/model"
)

// ---------------------------------------------------------------------------
// Chaos test infrastructure
// ---------------------------------------------------------------------------
//
// These tests verify the two convergence invariants of the control plane
// under faults and interleavings:
//
//   I1: every camera with an active session is eventually recording
//   I2: every camera without an active session (and no in-window schedule)
//       is eventually NOT recording
//
// fakeMTX simulates MediaMTX runtime state with fault injection (write
// failures, full restart). syncActuator applies commands synchronously with
// the same upsert semantics as the real actuator, keeping the tests
// deterministic; the real actuator's retry behavior is covered separately
// in actuator_test.go.

type fakeMTX struct {
	mu         sync.Mutex
	down       bool // health check and all APIs fail
	writeFails int  // remaining writes to fail (injected transient faults)
	paths      map[string]mediamtx.PathConfigItem
}

func newFakeMTX() *fakeMTX {
	return &fakeMTX{paths: make(map[string]mediamtx.PathConfigItem)}
}

func (f *fakeMTX) HealthCheck() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.down {
		return errors.New("mediamtx down")
	}
	return nil
}

func (f *fakeMTX) ListPathConfigs() (map[string]mediamtx.PathConfigItem, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.down {
		return nil, errors.New("mediamtx down")
	}
	out := make(map[string]mediamtx.PathConfigItem, len(f.paths))
	for k, v := range f.paths {
		out[k] = v
	}
	return out, nil
}

// maybeFail consumes injected faults. Caller must hold the lock.
func (f *fakeMTX) maybeFail() error {
	if f.down {
		return errors.New("mediamtx down")
	}
	if f.writeFails > 0 {
		f.writeFails--
		return errors.New("injected write failure")
	}
	return nil
}

func (f *fakeMTX) AddPath(name string, cfg mediamtx.PathConfig) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.maybeFail(); err != nil {
		return err
	}
	if _, exists := f.paths[name]; exists {
		return &mediamtx.APIError{StatusCode: 400, Body: "path already exists"}
	}
	f.paths[name] = mediamtx.PathConfigItem{
		Name:           name,
		Source:         cfg.Source,
		SourceOnDemand: cfg.SourceOnDemand,
		Record:         cfg.Record,
	}
	return nil
}

func (f *fakeMTX) DeletePath(name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.maybeFail(); err != nil {
		return err
	}
	if _, exists := f.paths[name]; !exists {
		return &mediamtx.APIError{StatusCode: 404, Body: "path not found"}
	}
	delete(f.paths, name)
	return nil
}

func (f *fakeMTX) PatchPath(name string, patch map[string]any) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.maybeFail(); err != nil {
		return err
	}
	p, exists := f.paths[name]
	if !exists {
		return &mediamtx.APIError{StatusCode: 404, Body: "path not found"}
	}
	if v, ok := patch["record"]; ok {
		p.Record, _ = v.(bool)
	}
	if v, ok := patch["sourceOnDemand"]; ok {
		p.SourceOnDemand, _ = v.(bool)
	}
	if v, ok := patch["source"]; ok {
		p.Source, _ = v.(string)
	}
	f.paths[name] = p
	return nil
}

// restart simulates a MediaMTX restart: all runtime paths are lost.
func (f *fakeMTX) restart() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.paths = make(map[string]mediamtx.PathConfigItem)
}

func (f *fakeMTX) isRecording(name string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	p, exists := f.paths[name]
	return exists && p.Record
}

func (f *fakeMTX) hasPath(name string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, exists := f.paths[name]
	return exists
}

// syncActuator implements MTXActuator, applying commands synchronously with
// the real actuator's upsert semantics (add → on conflict patch full config).
// Injected write failures are intentionally surfaced: the reconciler's
// level-triggered retries must recover from them.
type syncActuator struct {
	mtx *fakeMTX
}

func (a *syncActuator) EnqueueEnsurePath(name string, cfg mediamtx.PathConfig) {
	err := a.mtx.AddPath(name, cfg)
	var apiErr *mediamtx.APIError
	if err != nil && errors.As(err, &apiErr) && apiErr.StatusCode == 400 {
		_ = a.mtx.PatchPath(name, map[string]any{
			"source":         cfg.Source,
			"sourceOnDemand": cfg.SourceOnDemand,
			"record":         cfg.Record,
		})
	}
}

func (a *syncActuator) EnqueueDeletePath(name string) {
	_ = a.mtx.DeletePath(name)
}

func (a *syncActuator) EnqueueSetRecord(path string, on bool) {
	_ = a.mtx.PatchPath(path, map[string]any{
		"record":         on,
		"sourceOnDemand": !on,
	})
}

// chaosRecRepo is an in-memory recording repo with REAL session semantics
// (CloseSession actually closes), unlike the recording-only mocks.
type chaosRecRepo struct {
	mu       sync.Mutex
	sessions []model.RecordingSession
}

func (r *chaosRecRepo) FindActiveSessions() ([]model.RecordingSession, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []model.RecordingSession
	for _, s := range r.sessions {
		if s.EndTime == nil {
			out = append(out, s)
		}
	}
	return out, nil
}

func (r *chaosRecRepo) FindActiveSessionByCamera(cameraID string) (*model.RecordingSession, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i := len(r.sessions) - 1; i >= 0; i-- {
		if r.sessions[i].CameraID == cameraID && r.sessions[i].EndTime == nil {
			s := r.sessions[i]
			return &s, nil
		}
	}
	return nil, errors.New("no active session")
}

func (r *chaosRecRepo) FindActiveSessionBySchedule(scheduleID string) (*model.RecordingSession, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i := len(r.sessions) - 1; i >= 0; i-- {
		s := r.sessions[i]
		if s.ScheduleID != nil && *s.ScheduleID == scheduleID && s.EndTime == nil {
			return &s, nil
		}
	}
	return nil, errors.New("no active session")
}

func (r *chaosRecRepo) CreateSession(sess *model.RecordingSession) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sessions = append(r.sessions, *sess)
	return nil
}

func (r *chaosRecRepo) CloseSession(id string, endTime time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i := range r.sessions {
		if r.sessions[i].ID == id && r.sessions[i].EndTime == nil {
			r.sessions[i].EndTime = &endTime
			r.sessions[i].UpdatedAt = endTime
		}
	}
	return nil
}

// --- intent operators -------------------------------------------------------
//
// startManual / stopManual mirror the ordering contracts of
// recording.Service.StartManual / StopManual (which are pinned by their own
// unit tests): start patches record on then opens the session; stop closes
// the session FIRST, then patches record off.

func startManual(rec *chaosRecRepo, act MTXActuator, cam model.Camera) {
	act.EnqueueSetRecord(cam.MediaMTXPath, true)
	_ = rec.CreateSession(&model.RecordingSession{
		ID:          uuid.NewString(),
		CameraID:    cam.ID,
		TriggerType: "manual",
		StartTime:   time.Now(),
		LicenseID:   cam.LicenseID,
	})
}

func stopManual(rec *chaosRecRepo, act MTXActuator, cam model.Camera) {
	if sess, err := rec.FindActiveSessionByCamera(cam.ID); err == nil {
		_ = rec.CloseSession(sess.ID, time.Now())
	}
	act.EnqueueSetRecord(cam.MediaMTXPath, false)
}

func newChaosReconciler(cams []model.Camera) (*Reconciler, *fakeMTX, *chaosRecRepo, *mockCameraRepo) {
	camRepo := &mockCameraRepo{cams: cams}
	recRepo := &chaosRecRepo{}
	mtx := newFakeMTX()
	r := NewReconciler(camRepo, &mockScheduleRepo{}, recRepo, mtx, &syncActuator{mtx: mtx},
		"/recordings", "1m", "")
	return r, mtx, recRepo, camRepo
}

// testCameras uses localhost with a closed port so status probes fail fast
// (connection refused) instead of burning the 3s dial timeout.
func testCameras(n int) []model.Camera {
	cams := make([]model.Camera, n)
	for i := range cams {
		cams[i] = model.Camera{
			ID:           uuid.NewString(),
			IP:           "127.0.0.1",
			Port:         1, // closed port → immediate refusal
			LicenseID:    1,
			StreamURL:    "rtsp://192.0.2.10/stream",
			MediaMTXPath: "cam-test-" + string(rune('a'+i)),
		}
	}
	return cams
}

// assertInvariants checks I1/I2 for every camera after the system is given
// enough clean cycles to converge.
func assertInvariants(t *testing.T, r *Reconciler, mtx *fakeMTX, recRepo *chaosRecRepo, cams []model.Camera) {
	t.Helper()
	for i := 0; i < 12; i++ {
		r.reconcile()
	}
	for _, cam := range cams {
		active := false
		if _, err := recRepo.FindActiveSessionByCamera(cam.ID); err == nil {
			active = true
		}
		recording := mtx.isRecording(cam.MediaMTXPath)
		if active && !recording {
			t.Errorf("invariant I1 violated: camera %s has an active session but is not recording", cam.ID)
		}
		if !active && recording {
			t.Errorf("invariant I2 violated: camera %s has no active session but is recording", cam.ID)
		}
		if !mtx.hasPath(cam.MediaMTXPath) {
			t.Errorf("camera %s has no path on mediamtx after convergence", cam.ID)
		}
	}
}

// ---------------------------------------------------------------------------
// Scenario tests
// ---------------------------------------------------------------------------

// TestChaos_MTXRestartRestoresRecording: MediaMTX restarts mid-recording.
// Paths are wiped; the reconciler must re-register the path AND re-enable
// recording because the session is still open.
func TestChaos_MTXRestartRestoresRecording(t *testing.T) {
	cams := testCameras(1)
	r, mtx, recRepo, _ := newChaosReconciler(cams)

	r.reconcile() // register path
	startManual(recRepo, &syncActuator{mtx: mtx}, cams[0])
	r.reconcile() // steady state: recording
	if !mtx.isRecording(cams[0].MediaMTXPath) {
		t.Fatal("setup failed: expected recording before restart")
	}

	mtx.restart()

	assertInvariants(t, r, mtx, recRepo, cams)
}

// TestChaos_StopWhileWritesFailing: a stop whose MediaMTX write fails after
// the session was closed. Once writes recover, the orphan repair must stop
// the recording (this is the exact race the stop reordering + reverse
// repair were designed for).
func TestChaos_StopWhileWritesFailing(t *testing.T) {
	cams := testCameras(1)
	r, mtx, recRepo, _ := newChaosReconciler(cams)

	r.reconcile()
	startManual(recRepo, &syncActuator{mtx: mtx}, cams[0])
	r.reconcile()
	if !mtx.isRecording(cams[0].MediaMTXPath) {
		t.Fatal("setup failed: expected recording before stop")
	}

	// Inject a sustained write outage, then stop: session closes, but the
	// record-off patch fails.
	mtx.writeFails = 100
	stopManual(recRepo, &syncActuator{mtx: mtx}, cams[0])
	if !mtx.isRecording(cams[0].MediaMTXPath) {
		t.Fatal("test premise broken: write failure was not injected")
	}

	// Writes recover; convergence must stop the orphan recording.
	mtx.writeFails = 0
	assertInvariants(t, r, mtx, recRepo, cams)
}

// TestChaos_StartGapHysteresis: record:true is applied a few milliseconds
// before the session row exists (the start ordering gap). A reconcile tick
// landing in that gap must NOT stop the recording; the hysteresis and the
// session created right after must protect it.
func TestChaos_StartGapHysteresis(t *testing.T) {
	cams := testCameras(1)
	r, mtx, recRepo, _ := newChaosReconciler(cams)

	r.reconcile()
	act := &syncActuator{mtx: mtx}

	// Half-start: record on, session not yet created.
	act.EnqueueSetRecord(cams[0].MediaMTXPath, true)
	r.reconcile() // lands in the gap: suspicion 1, must not stop

	// Complete the start.
	_ = recRepo.CreateSession(&model.RecordingSession{
		ID:          uuid.NewString(),
		CameraID:    cams[0].ID,
		TriggerType: "manual",
		StartTime:   time.Now(),
		LicenseID:   1,
	})

	assertInvariants(t, r, mtx, recRepo, cams)
}

// TestChaos_MTDownThenUp: MediaMTX fully unreachable for several cycles,
// then recovers. Desired state (sessions) survives in the DB and recording
// resumes without user action.
func TestChaos_MTDownThenUp(t *testing.T) {
	cams := testCameras(1)
	r, mtx, recRepo, _ := newChaosReconciler(cams)

	r.reconcile()
	startManual(recRepo, &syncActuator{mtx: mtx}, cams[0])
	r.reconcile()

	mtx.down = true
	for i := 0; i < 5; i++ {
		r.reconcile() // all skipped (health check fails)
	}
	mtx.down = false
	// MTX "restarted" while down: paths gone.
	mtx.restart()

	assertInvariants(t, r, mtx, recRepo, cams)
}

// TestChaos_RandomizedOpsConverge: seeded random interleaving of starts,
// stops, reconcile ticks, MTX restarts and transient write failures. After
// the noise, clean cycles must converge to both invariants for all cameras.
// Runs across multiple seeds — chaos tests must not depend on one lucky draw.
func TestChaos_RandomizedOpsConverge(t *testing.T) {
	for _, seed := range []int64{42, 1337, 20260813, 7, 999} {
		t.Run(fmt.Sprintf("seed-%d", seed), func(t *testing.T) {
			runRandomizedChaos(t, seed)
		})
	}
}

func runRandomizedChaos(t *testing.T, seed int64) {
	cams := testCameras(3)
	r, mtx, recRepo, _ := newChaosReconciler(cams)
	act := &syncActuator{mtx: mtx}

	rng := rand.New(rand.NewSource(seed))

	for i := 0; i < 80; i++ {
		cam := cams[rng.Intn(len(cams))]
		switch rng.Intn(6) {
		case 0, 1: // reconcile tick (most common)
			r.reconcile()
		case 2: // start manual (if not already active)
			if _, err := recRepo.FindActiveSessionByCamera(cam.ID); err != nil {
				startManual(recRepo, act, cam)
			}
		case 3: // stop manual (if active)
			if _, err := recRepo.FindActiveSessionByCamera(cam.ID); err == nil {
				stopManual(recRepo, act, cam)
			}
		case 4: // MTX restart (paths wiped)
			mtx.restart()
		case 5: // transient write outage for the next few writes
			mtx.writeFails = 1 + rng.Intn(3)
		}
	}

	// Clear any residual injected failures, then converge and check.
	mtx.writeFails = 0
	mtx.down = false
	assertInvariants(t, r, mtx, recRepo, cams)
}
