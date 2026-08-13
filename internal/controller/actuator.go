package controller

import (
	"errors"
	"fmt"
	"sync"
	"time"

	"aiovms/internal/mediamtx"
	"aiovms/pkg/backoff"
	"aiovms/pkg/logger"
	"aiovms/pkg/metrics"
)

// ---------------------------------------------------------------------------
// Actuator — single-writer executor for all MediaMTX mutations
// ---------------------------------------------------------------------------
//
// Design notes:
//
//   - Intent-first: callers express desired state (ensure path, delete path,
//     set record on/off) instead of performing request/response calls. All
//     commands are executed serially by a single goroutine, so MediaMTX sees
//     exactly one writer and handler-vs-reconciler interleaving races are
//     eliminated by construction.
//
//   - Coalescing: while a command for (kind, path) is still pending, a newer
//     command for the same key replaces its payload (last-write-wins) and
//     inherits its waiters. This bounds queue growth during a sustained
//     MediaMTX outage and guarantees the freshest desired state wins.
//
//   - At-least-once + idempotent: all three operations are idempotent
//     (AddPath is upsert, PatchPath sets absolute state, DeletePath of a
//     missing path is a no-op the reconciler tolerates), so retrying failed
//     commands with tiered backoff is safe. After maxApplyAttempts a command
//     is abandoned with an error — the reconciler re-enqueues whatever is
//     still drifted on a later cycle (level-triggered safety net).
//
//   - Sync vs async: API handlers use the sync methods (wait up to
//     applyTimeout so the HTTP response reflects the outcome); the
//     reconciler uses the fire-and-forget Enqueue* variants and must never
//     block on MediaMTX availability.

const (
	actuatorQueueSize   = 256
	defaultApplyTimeout = 5 * time.Second
	maxApplyAttempts    = 10
)

type cmdKind string

const (
	cmdEnsurePath cmdKind = "ensure_path"
	cmdDeletePath cmdKind = "delete_path"
	cmdSetRecord  cmdKind = "set_record"
)

type command struct {
	kind    cmdKind
	path    string
	cfg     mediamtx.PathConfig // ensure_path only
	record  bool                // set_record only
	waiters []chan error
}

// MediaMTXWriter is the subset of the MediaMTX client the actuator needs.
// All mutations to MediaMTX in the entire codebase MUST go through the
// actuator; nothing else may call these methods directly at runtime.
type MediaMTXWriter interface {
	AddPath(name string, cfg mediamtx.PathConfig) error
	DeletePath(name string) error
	PatchPath(name string, patch map[string]any) error
}

// Actuator serializes MediaMTX mutations behind a single worker goroutine.
type Actuator struct {
	mtx MediaMTXWriter

	queue   chan *command
	mu      sync.Mutex // guards pending + command payload coalescing
	pending map[string]*command

	applyDelay   func(attempt int) time.Duration // injectable for tests
	applyTimeout time.Duration                   // sync wait budget

	stopCh   chan struct{}
	stopOnce sync.Once
	wg       sync.WaitGroup
}

func NewActuator(mtx MediaMTXWriter) *Actuator {
	return &Actuator{
		mtx:          mtx,
		queue:        make(chan *command, actuatorQueueSize),
		pending:      make(map[string]*command),
		applyDelay:   backoff.TieredBackoffWithJitter,
		applyTimeout: defaultApplyTimeout,
		stopCh:       make(chan struct{}),
	}
}

// Run starts the serial execution loop. Blocks until Stop is called.
func (a *Actuator) Run() {
	a.wg.Add(1)
	defer a.wg.Done()
	logger.Info("mediamtx actuator started")
	for {
		select {
		case cmd := <-a.queue:
			a.execute(cmd)
		case <-a.stopCh:
			logger.Info("mediamtx actuator stopped")
			return
		}
	}
}

// Stop signals the actuator to exit and waits for the loop to finish.
// Pending commands are abandoned; the reconciler re-derives desired state on
// the next cycle, so no intent is permanently lost.
func (a *Actuator) Stop() {
	a.stopOnce.Do(func() { close(a.stopCh) })
	a.wg.Wait()
}

// --- Async (fire-and-forget) API — used by the reconciler ------------------

// EnqueueEnsurePath asynchronously creates or converges a path config.
func (a *Actuator) EnqueueEnsurePath(name string, cfg mediamtx.PathConfig) {
	a.enqueue(&command{kind: cmdEnsurePath, path: name, cfg: cfg})
}

// EnqueueDeletePath asynchronously removes a path.
func (a *Actuator) EnqueueDeletePath(name string) {
	a.enqueue(&command{kind: cmdDeletePath, path: name})
}

// EnqueueSetRecord asynchronously toggles recording (and sourceOnDemand).
func (a *Actuator) EnqueueSetRecord(path string, on bool) {
	a.enqueue(&command{kind: cmdSetRecord, path: path, record: on})
}

// --- Sync API — used by request handlers -----------------------------------

// SetRecord toggles recording and waits for the outcome up to applyTimeout.
// On timeout the command REMAINS queued and will still be applied later;
// callers treat timeout as a soft failure (state converges asynchronously).
func (a *Actuator) SetRecord(path string, on bool) error {
	return a.enqueueAndWait(&command{kind: cmdSetRecord, path: path, record: on})
}

// EnsurePath creates or converges a path config and waits for the outcome.
func (a *Actuator) EnsurePath(name string, cfg mediamtx.PathConfig) error {
	return a.enqueueAndWait(&command{kind: cmdEnsurePath, path: name, cfg: cfg})
}

// DeletePath removes a path and waits for the outcome.
func (a *Actuator) DeletePath(name string) error {
	return a.enqueueAndWait(&command{kind: cmdDeletePath, path: name})
}

func (a *Actuator) enqueueAndWait(cmd *command) error {
	done := make(chan error, 1)
	cmd.waiters = []chan error{done}
	a.enqueue(cmd)
	select {
	case err := <-done:
		return err
	case <-time.After(a.applyTimeout):
		return fmt.Errorf("actuator: %s for %s not applied within %s (still queued)",
			cmd.kind, cmd.path, a.applyTimeout)
	}
}

// --- Internals --------------------------------------------------------------

func commandKey(kind cmdKind, path string) string {
	return string(kind) + "|" + path
}

func (a *Actuator) enqueue(cmd *command) {
	key := commandKey(cmd.kind, cmd.path)

	a.mu.Lock()
	if prev, ok := a.pending[key]; ok {
		// Coalesce: last-write-wins; inherit the new command's waiters so
		// every caller eventually gets notified.
		prev.cfg = cmd.cfg
		prev.record = cmd.record
		if len(cmd.waiters) > 0 {
			prev.waiters = append(prev.waiters, cmd.waiters...)
		}
		a.mu.Unlock()
		return
	}
	a.pending[key] = cmd
	a.mu.Unlock()

	select {
	case a.queue <- cmd:
		metrics.ActuatorQueueDepth.Inc()
	default:
		// Queue full — drop rather than block the caller. The reconciler
		// re-enqueues whatever is still drifted on a later cycle.
		a.mu.Lock()
		delete(a.pending, key)
		a.mu.Unlock()
		metrics.ActuatorCommandsTotal.WithLabelValues(string(cmd.kind), "dropped").Inc()
		logger.Errorf("actuator: queue full, dropped %s for %s", cmd.kind, cmd.path)
		notifyWaiters(cmd.waiters, errors.New("actuator queue full"))
	}
}

func (a *Actuator) execute(cmd *command) {
	metrics.ActuatorQueueDepth.Dec()

	var waiters []chan error
	for attempt := 0; ; attempt++ {
		// Snapshot the payload under the lock: coalescing may have updated
		// it while the command was queued, and late waiters may have been
		// appended after a previous failed attempt.
		a.mu.Lock()
		kind, path, cfg, record := cmd.kind, cmd.path, cmd.cfg, cmd.record
		if len(cmd.waiters) > 0 {
			waiters = append(waiters, cmd.waiters...)
			cmd.waiters = nil
		}
		if attempt == 0 {
			delete(a.pending, commandKey(cmd.kind, cmd.path))
		}
		a.mu.Unlock()

		err := a.apply(kind, path, cfg, record)
		if err == nil {
			metrics.ActuatorCommandsTotal.WithLabelValues(string(kind), "ok").Inc()
			notifyWaiters(waiters, nil)
			return
		}

		if attempt >= maxApplyAttempts-1 {
			metrics.ActuatorCommandsTotal.WithLabelValues(string(kind), "failed").Inc()
			logger.Errorf("actuator: giving up on %s for %s after %d attempts: %v",
				kind, path, attempt+1, err)
			notifyWaiters(waiters, err)
			return
		}

		logger.Warnf("actuator: %s for %s failed (attempt %d/%d): %v",
			kind, path, attempt+1, maxApplyAttempts, err)
		select {
		case <-time.After(a.applyDelay(attempt)):
		case <-a.stopCh:
			notifyWaiters(waiters, errors.New("actuator stopped"))
			return
		}
	}
}

func (a *Actuator) apply(kind cmdKind, path string, cfg mediamtx.PathConfig, record bool) error {
	switch kind {
	case cmdEnsurePath:
		return a.mtx.AddPath(path, cfg)
	case cmdDeletePath:
		return a.mtx.DeletePath(path)
	case cmdSetRecord:
		// Recording on  → sourceOnDemand off (pull the stream actively).
		// Recording off → sourceOnDemand on  (stop pulling when nobody watches).
		return a.mtx.PatchPath(path, map[string]any{
			"record":         record,
			"sourceOnDemand": !record,
		})
	default:
		return fmt.Errorf("unknown command kind %q", kind)
	}
}

func notifyWaiters(waiters []chan error, err error) {
	for _, w := range waiters {
		w <- err
	}
}
