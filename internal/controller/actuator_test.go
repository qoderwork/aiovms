package controller

import (
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"aiovms/internal/mediamtx"
)

// --- mock MediaMTX writer ---

type mockMTXWriter struct {
	mu           sync.Mutex
	addPathCalls []struct {
		name string
		cfg  mediamtx.PathConfig
	}
	deletePathCalls []string
	patchPathCalls  []struct {
		name  string
		patch map[string]any
	}
	addErr    error
	deleteErr error
	patchErr  error
	// patchFailN makes the first N PatchPath calls fail (retry tests).
	patchCalls int
	patchFailN int
}

func (m *mockMTXWriter) AddPath(name string, cfg mediamtx.PathConfig) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.addPathCalls = append(m.addPathCalls, struct {
		name string
		cfg  mediamtx.PathConfig
	}{name, cfg})
	return m.addErr
}

func (m *mockMTXWriter) DeletePath(name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.deletePathCalls = append(m.deletePathCalls, name)
	return m.deleteErr
}

func (m *mockMTXWriter) PatchPath(name string, patch map[string]any) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.patchCalls++
	m.patchPathCalls = append(m.patchPathCalls, struct {
		name  string
		patch map[string]any
	}{name, patch})
	if m.patchFailN > 0 && m.patchCalls <= m.patchFailN {
		return errors.New("transient failure")
	}
	return m.patchErr
}

func (m *mockMTXWriter) counts() (add, del, patch int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.addPathCalls), len(m.deletePathCalls), len(m.patchPathCalls)
}

func newTestActuator(w *mockMTXWriter) *Actuator {
	a := NewActuator(w)
	// Fast retries for tests.
	a.applyDelay = func(int) time.Duration { return time.Millisecond }
	return a
}

// --- tests ---

func TestActuatorExecutesCommands(t *testing.T) {
	w := &mockMTXWriter{}
	a := newTestActuator(w)
	go a.Run()
	defer a.Stop()

	if err := a.SetRecord("cam-1", true); err != nil {
		t.Fatalf("SetRecord: %v", err)
	}
	if err := a.EnsurePath("cam-2", mediamtx.PathConfig{Source: "rtsp://1.2.3.4/s"}); err != nil {
		t.Fatalf("EnsurePath: %v", err)
	}
	if err := a.DeletePath("cam-3"); err != nil {
		t.Fatalf("DeletePath: %v", err)
	}

	add, del, patch := w.counts()
	if add != 1 || del != 1 || patch != 1 {
		t.Fatalf("expected 1 add, 1 delete, 1 patch; got %d/%d/%d", add, del, patch)
	}

	w.mu.Lock()
	defer w.mu.Unlock()
	p := w.patchPathCalls[0].patch
	if p["record"] != true || p["sourceOnDemand"] != false {
		t.Errorf("set_record on: patch = %v, want record=true sourceOnDemand=false", p)
	}
}

func TestActuatorSetRecordOffInvertsSourceOnDemand(t *testing.T) {
	w := &mockMTXWriter{}
	a := newTestActuator(w)
	go a.Run()
	defer a.Stop()

	if err := a.SetRecord("cam-1", false); err != nil {
		t.Fatalf("SetRecord: %v", err)
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	p := w.patchPathCalls[0].patch
	if p["record"] != false || p["sourceOnDemand"] != true {
		t.Errorf("set_record off: patch = %v, want record=false sourceOnDemand=true", p)
	}
}

// TestActuatorCoalescesPendingCommands verifies last-write-wins coalescing:
// while the worker hasn't started, two SetRecord commands for the same path
// merge into ONE patch carrying the latest payload, and all waiters are
// notified. This is what bounds queue growth during a MediaMTX outage.
func TestActuatorCoalescesPendingCommands(t *testing.T) {
	w := &mockMTXWriter{}
	a := newTestActuator(w)
	// Deliberately NOT starting Run yet — commands stay pending.

	done1 := make(chan error, 1)
	done2 := make(chan error, 1)
	a.enqueue(&command{kind: cmdSetRecord, path: "cam-1", record: true, waiters: []chan error{done1}})
	a.enqueue(&command{kind: cmdSetRecord, path: "cam-1", record: false, waiters: []chan error{done2}})

	go a.Run()
	defer a.Stop()

	for _, done := range []chan error{done1, done2} {
		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("waiter error: %v", err)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("waiter not notified")
		}
	}

	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.patchPathCalls) != 1 {
		t.Fatalf("expected coalesced single patch, got %d", len(w.patchPathCalls))
	}
	if w.patchPathCalls[0].patch["record"] != false {
		t.Errorf("expected last-write-wins record=false, got %v", w.patchPathCalls[0].patch)
	}
}

// TestActuatorRetriesUntilSuccess verifies retry-with-backoff on transient
// failures (at-least-once execution; idempotent operations make it safe).
func TestActuatorRetriesUntilSuccess(t *testing.T) {
	w := &mockMTXWriter{patchFailN: 2}
	a := newTestActuator(w)
	go a.Run()
	defer a.Stop()

	if err := a.SetRecord("cam-1", true); err != nil {
		t.Fatalf("SetRecord: %v", err)
	}
	if _, _, patch := w.counts(); patch != 3 {
		t.Errorf("expected 3 attempts (2 failures + 1 success), got %d", patch)
	}
}

// TestActuatorGivesUpAfterMaxAttempts verifies that a permanently failing
// command is abandoned (not retried forever) and waiters receive the error.
func TestActuatorGivesUpAfterMaxAttempts(t *testing.T) {
	w := &mockMTXWriter{patchErr: errors.New("permanent")}
	a := newTestActuator(w)
	go a.Run()
	defer a.Stop()

	err := a.SetRecord("cam-1", true)
	if err == nil {
		t.Fatal("expected error after retries exhausted")
	}
	if _, _, patch := w.counts(); patch != maxApplyAttempts {
		t.Errorf("expected %d attempts, got %d", maxApplyAttempts, patch)
	}
}

// TestActuatorSyncTimeout verifies the sync wait budget: when execution takes
// longer than applyTimeout, the caller gets a timeout error while the command
// stays queued.
func TestActuatorSyncTimeout(t *testing.T) {
	w := &mockMTXWriter{patchErr: errors.New("down")}
	a := newTestActuator(w)
	a.applyTimeout = 30 * time.Millisecond
	a.applyDelay = func(int) time.Duration { return time.Hour } // keep it stuck retrying
	go a.Run()
	defer a.Stop()

	start := time.Now()
	err := a.SetRecord("cam-1", true)
	if err == nil {
		t.Fatal("expected timeout error")
	}
	if time.Since(start) > 2*time.Second {
		t.Errorf("waited too long: %v", time.Since(start))
	}
}

// TestActuatorQueueFullDrops verifies backpressure: when the queue is full,
// new commands are dropped and their waiters are notified with an error.
func TestActuatorQueueFullDrops(t *testing.T) {
	w := &mockMTXWriter{}
	a := newTestActuator(w)
	// Worker not started — queue fills up.

	for i := 0; i < actuatorQueueSize; i++ {
		a.enqueue(&command{kind: cmdDeletePath, path: fmt.Sprintf("path-%d", i)})
	}

	done := make(chan error, 1)
	a.enqueue(&command{kind: cmdSetRecord, path: "cam-overflow", waiters: []chan error{done}})
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected drop error, got nil")
		}
	case <-time.After(time.Second):
		t.Fatal("dropped command waiter not notified")
	}
}

// --- Error classification tests --------------------------------------------

// TestActuatorDeterministic4xxGivesUpImmediately verifies that non-404 4xx
// errors fail fast without consuming the backoff window (they are
// deterministic — retrying just wastes ~25s).
func TestActuatorDeterministic4xxGivesUpImmediately(t *testing.T) {
	w := &mockMTXWriter{patchErr: &mediamtx.APIError{StatusCode: 400, Body: "bad request"}}
	a := newTestActuator(w)
	go a.Run()
	defer a.Stop()

	err := a.SetRecord("cam-1", true)
	if err == nil {
		t.Fatal("expected deterministic error")
	}
	if _, _, patch := w.counts(); patch != 1 {
		t.Errorf("expected exactly 1 attempt (no retry on 4xx), got %d", patch)
	}
}

// TestActuatorOffOnMissingPathIsTrivialSuccess: record-off against a missing
// path (404) means the desired state ("not recording") already holds.
func TestActuatorOffOnMissingPathIsTrivialSuccess(t *testing.T) {
	w := &mockMTXWriter{patchErr: &mediamtx.APIError{StatusCode: 404, Body: "not found"}}
	a := newTestActuator(w)
	go a.Run()
	defer a.Stop()

	if err := a.SetRecord("cam-gone", false); err != nil {
		t.Fatalf("record-off on missing path must be trivial success, got: %v", err)
	}
	if _, _, patch := w.counts(); patch != 1 {
		t.Errorf("expected exactly 1 attempt, got %d", patch)
	}
}

// TestActuatorOnOnMissingPathIsDeterministicFailure: record-on against a
// missing path is a real precondition failure — give up fast; the reconciler
// re-drives the intent after reconcileStreams recreates the path.
func TestActuatorOnOnMissingPathIsDeterministicFailure(t *testing.T) {
	w := &mockMTXWriter{patchErr: &mediamtx.APIError{StatusCode: 404, Body: "not found"}}
	a := newTestActuator(w)
	go a.Run()
	defer a.Stop()

	err := a.SetRecord("cam-gone", true)
	if err == nil {
		t.Fatal("expected deterministic failure for record-on on missing path")
	}
	if _, _, patch := w.counts(); patch != 1 {
		t.Errorf("expected exactly 1 attempt (no retry on 404), got %d", patch)
	}
}

// TestActuatorDeleteMissingPathIsTrivialSuccess: deleting a path that does
// not exist means the desired state ("path gone") already holds.
func TestActuatorDeleteMissingPathIsTrivialSuccess(t *testing.T) {
	w := &mockMTXWriter{deleteErr: &mediamtx.APIError{StatusCode: 404, Body: "not found"}}
	a := newTestActuator(w)
	go a.Run()
	defer a.Stop()

	if err := a.DeletePath("cam-gone"); err != nil {
		t.Fatalf("delete of missing path must be trivial success, got: %v", err)
	}
	if _, del, _ := w.counts(); del != 1 {
		t.Errorf("expected exactly 1 attempt, got %d", del)
	}
}
