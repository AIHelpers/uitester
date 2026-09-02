package ui

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"sync"
	"time"

	"uitester/internal/domain"
	"uitester/internal/usecase"
)

// Names of the event kinds carried by RunEvent.Event.
const (
	EventStep = "step"
	EventDone = "done"
)

// maxKeptRuns bounds the in-memory run history so a long-lived UI server
// doesn't grow without limit; the oldest runs are evicted first.
const maxKeptRuns = 200

// RunEvent is one progress message about a run: a completed step (EventStep)
// or the terminal event (EventDone). Events are buffered per run and
// replayed to late subscribers, so a page refresh mid-run sees exactly the
// same sequence as watching from the start. The Step and Result payloads are
// already the JSON views the browser consumes (sensitive values masked,
// attachments carrying servable URLs).
type RunEvent struct {
	RunID      string        `json:"run_id"`
	Event      string        `json:"event"`
	StepIndex  int           `json:"step_index,omitempty"`
	Step       *stepView     `json:"step,omitempty"`
	StepStatus domain.Status `json:"step_status,omitempty"`
	StepLog    string        `json:"step_log,omitempty"`
	Status     domain.Status `json:"status,omitempty"` // overall status, on done
	Error      string        `json:"error,omitempty"`
	Result     *resultView   `json:"result,omitempty"` // full result, on done
	ElapsedMS  int64         `json:"elapsed_ms,omitempty"`
}

// RunState is the authoritative state of a single run. Always accessed
// under AsyncRunner.mu and copied out via the snapshot methods.
type RunState struct {
	ID        string
	Scenario  domain.Scenario
	ToolCfg   map[string]interface{}
	Status    string // "running" | "passed" | "failed"
	Error     string
	Result    *domain.ScenarioResult
	StartedAt time.Time
	DoneAt    time.Time
}

// runSnapshot is a point-in-time copy of a run's state plus its completed
// steps, so API responses can be built outside the lock.
type runSnapshot struct {
	State RunState
	Steps []domain.StepResult
}

// runSummary is the list-entry projection of a run.
type runSummary struct {
	ID           string
	ScenarioName string
	Tool         string
	Status       string
	StartedAt    time.Time
	DurationMS   int64
}

// viewConverter supplies the JSON projections of domain results. The Server
// implements it because attachment URLs depend on server-configured paths;
// the AsyncRunner only knows how to run and broadcast, not how to render.
type viewConverter struct {
	stepView   func(index int, sr domain.StepResult) stepView
	resultView func(runID string, res domain.ScenarioResult) resultView
}

// AsyncRunner wraps the synchronous usecase.Runner so the UI can start
// runs in the background and stream per-step progress. It keeps one handle
// per run: the state (for polling), the buffered events (for replay to
// late SSE subscribers), and the live listener channels.
type AsyncRunner struct {
	runner *usecase.Runner
	conv   viewConverter

	mu    sync.Mutex
	runs  map[string]*runHandle
	order []string // insertion order of run IDs, oldest first
}

type runHandle struct {
	state     RunState
	steps     []domain.StepResult
	events    []RunEvent
	listeners []chan RunEvent
	cancel    context.CancelFunc
}

func NewAsyncRunner(runner *usecase.Runner, conv viewConverter) *AsyncRunner {
	return &AsyncRunner{runner: runner, conv: conv, runs: make(map[string]*runHandle)}
}

// newRunID returns a random hex identifier. crypto/rand keeps it
// unguessable with no external dependency; the time-based fallback only
// triggers in the pathological case where the system entropy source is
// unavailable.
func newRunID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("run-%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b[:])
}

// Start registers a new run and executes it in a background goroutine. It
// returns immediately with the run ID; scenario validation and driver
// errors surface asynchronously in the run's state and events.
func (a *AsyncRunner) Start(scenario domain.Scenario, toolCfg map[string]interface{}) string {
	id := newRunID()
	ctx, cancel := context.WithCancel(context.Background())
	h := &runHandle{cancel: cancel}
	h.state = RunState{
		ID:        id,
		Scenario:  scenario,
		ToolCfg:   toolCfg,
		Status:    "running",
		StartedAt: time.Now(),
	}

	a.mu.Lock()
	a.runs[id] = h
	a.order = append(a.order, id)
	for len(a.order) > maxKeptRuns {
		delete(a.runs, a.order[0])
		a.order = a.order[1:]
	}
	a.mu.Unlock()

	go a.runLoop(ctx, id)
	return id
}

func (a *AsyncRunner) runLoop(ctx context.Context, runID string) {
	a.mu.Lock()
	h := a.runs[runID]
	scenario, toolCfg, startedAt := h.state.Scenario, h.state.ToolCfg, h.state.StartedAt
	a.mu.Unlock()

	result, err := a.runner.Run(ctx, scenario, toolCfg, func(index int, sr domain.StepResult) {
		a.recordStep(runID, index, sr)
	})
	a.finish(runID, result, err, startedAt)
}

// recordStep appends a completed step to the run's state and broadcasts a
// step event. Invoked from the run goroutine via the progress callback.
func (a *AsyncRunner) recordStep(runID string, index int, sr domain.StepResult) {
	srCopy := sr
	sv := a.conv.stepView(index, srCopy)
	ev := RunEvent{
		RunID:      runID,
		Event:      EventStep,
		StepIndex:  index,
		Step:       &sv,
		StepStatus: sr.Status,
		StepLog:    sr.Log,
	}

	a.mu.Lock()
	defer a.mu.Unlock()
	h, ok := a.runs[runID]
	if !ok {
		return
	}
	h.steps = append(h.steps, srCopy)
	a.broadcastLocked(h, ev)
}

// finish records the terminal state of a run and broadcasts the done event.
func (a *AsyncRunner) finish(runID string, result domain.ScenarioResult, runErr error, startedAt time.Time) {
	elapsed := time.Since(startedAt)
	ev := RunEvent{RunID: runID, Event: EventDone, ElapsedMS: elapsed.Milliseconds()}

	a.mu.Lock()
	defer a.mu.Unlock()
	h, ok := a.runs[runID]
	if !ok {
		return
	}
	h.state.DoneAt = time.Now()

	if runErr != nil {
		h.state.Error = runErr.Error()
		h.state.Status = "failed"
		ev.Error = runErr.Error()
	}
	if result.Status == "" && h.state.Result == nil {
		// No result at all (e.g. the scenario failed validation): synthesize
		// a failed result so the UI has a uniform shape to render.
		result = domain.ScenarioResult{
			ScenarioName: h.state.Scenario.Name,
			Tool:         h.state.Scenario.Tool,
			Status:       domain.StatusFailed,
			StartedAt:    startedAt,
			Duration:     elapsed,
			Steps:        h.steps,
		}
	}
	if result.Status != "" {
		res := result
		h.state.Result = &res
		ev.Status = res.Status
		if runErr == nil {
			h.state.Status = string(res.Status)
		}
		rv := a.conv.resultView(runID, res)
		ev.Result = &rv
	}
	a.broadcastLocked(h, ev)
}

// broadcastLocked appends the event to the run's buffer and pushes it to
// every live listener. A stalled listener is dropped rather than allowed to
// stall the run itself; polling remains a complete fallback.
func (a *AsyncRunner) broadcastLocked(h *runHandle, ev RunEvent) {
	h.events = append(h.events, ev)
	for _, ch := range h.listeners {
		select {
		case ch <- ev:
		default:
		}
	}
}

// Get returns a snapshot of the run's current state. ok is false for
// unknown (or evicted) run IDs.
func (a *AsyncRunner) Get(runID string) (runSnapshot, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	h, ok := a.runs[runID]
	if !ok {
		return runSnapshot{}, false
	}
	steps := append([]domain.StepResult(nil), h.steps...)
	return runSnapshot{State: h.state, Steps: steps}, true
}

// Result returns the final result of a finished run; ok is false while the
// run is still running or unknown.
func (a *AsyncRunner) Result(runID string) (domain.ScenarioResult, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	h, ok := a.runs[runID]
	if !ok || h.state.Result == nil {
		return domain.ScenarioResult{}, false
	}
	return *h.state.Result, true
}

// Subscribe returns a channel that first replays every buffered event (so
// a late subscriber — e.g. a page refreshed mid-run — misses nothing) and
// then follows live events. Cancel detaches the listener; the done event
// is always part of the replay for finished runs.
func (a *AsyncRunner) Subscribe(runID string) (<-chan RunEvent, func(), bool) {
	a.mu.Lock()
	h, ok := a.runs[runID]
	if !ok {
		a.mu.Unlock()
		return nil, nil, false
	}
	ch := make(chan RunEvent, len(h.events)+64)
	for _, ev := range h.events {
		ch <- ev
	}
	h.listeners = append(h.listeners, ch)
	a.mu.Unlock()

	return ch, func() { a.removeListener(runID, ch) }, true
}

func (a *AsyncRunner) removeListener(runID string, ch chan RunEvent) {
	a.mu.Lock()
	defer a.mu.Unlock()
	h, ok := a.runs[runID]
	if !ok {
		return
	}
	for i, l := range h.listeners {
		if l == ch {
			h.listeners = append(h.listeners[:i], h.listeners[i+1:]...)
			return
		}
	}
}

// Stop cancels a running run's context: the in-flight step fails with a
// context error and the run finishes as failed. Returns false for unknown
// or already-finished runs.
func (a *AsyncRunner) Stop(runID string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	h, ok := a.runs[runID]
	if !ok || h.state.Status != "running" {
		return false
	}
	h.cancel()
	return true
}

// List returns a summary of every known run, newest first.
func (a *AsyncRunner) List() []runSummary {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]runSummary, 0, len(a.order))
	for i := len(a.order) - 1; i >= 0; i-- {
		h := a.runs[a.order[i]]
		if h == nil {
			continue
		}
		var dur int64
		switch {
		case h.state.Status == "running":
			dur = time.Since(h.state.StartedAt).Milliseconds()
		case h.state.Result != nil:
			dur = h.state.Result.Duration.Milliseconds()
		case !h.state.DoneAt.IsZero():
			dur = h.state.DoneAt.Sub(h.state.StartedAt).Milliseconds()
		}
		out = append(out, runSummary{
			ID:           h.state.ID,
			ScenarioName: h.state.Scenario.Name,
			Tool:         h.state.Scenario.Tool,
			Status:       h.state.Status,
			StartedAt:    h.state.StartedAt,
			DurationMS:   dur,
		})
	}
	return out
}

// LastForScenario returns the most recent run of the named scenario, if
// any — used for the status badge in the scenario list.
func (a *AsyncRunner) LastForScenario(scenarioName string) (runSummary, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for i := len(a.order) - 1; i >= 0; i-- {
		h := a.runs[a.order[i]]
		if h != nil && h.state.Scenario.Name == scenarioName {
			return runSummary{
				ID:        h.state.ID,
				Status:    h.state.Status,
				StartedAt: h.state.StartedAt,
			}, true
		}
	}
	return runSummary{}, false
}
