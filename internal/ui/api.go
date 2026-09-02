package ui

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"uitester/internal/config"
	"uitester/internal/domain"
)

// ---------------------------------------------------------------------------
// JSON view types (what the browser actually receives)
// ---------------------------------------------------------------------------

// rawAction is domain.Action without its MarshalJSON masking. It exists for
// exactly one purpose: the scenario *editor* must round-trip file contents
// faithfully — if the GET/PUT cycle went through domain.Action's masked
// marshal, saving an untouched sensitive step would overwrite the real
// secret with "••••••" on disk. Masking stays a *display* concern here: the
// frontend renders sensitive values as dots, and every result/report view
// (stepView, resultView) uses domain.Action's automatic masking.
type rawAction domain.Action

type rawScenario struct {
	Name    string      `json:"name"`
	Tool    string      `json:"tool"`
	BaseURL string      `json:"base_url,omitempty"`
	Steps   []rawAction `json:"steps"`
}

func toRaw(s domain.Scenario) rawScenario {
	out := rawScenario{Name: s.Name, Tool: s.Tool, BaseURL: s.BaseURL, Steps: make([]rawAction, 0, len(s.Steps))}
	for _, st := range s.Steps {
		out.Steps = append(out.Steps, rawAction(st))
	}
	return out
}

func (r rawScenario) toDomain() domain.Scenario {
	out := domain.Scenario{Name: r.Name, Tool: r.Tool, BaseURL: r.BaseURL, Steps: make([]domain.Action, 0, len(r.Steps))}
	for _, st := range r.Steps {
		out.Steps = append(out.Steps, domain.Action(st))
	}
	return out
}

// scenarioView is the masked projection of a Scenario for run views. Unlike
// rawScenario, it serializes steps through domain.Action's MarshalJSON, so
// sensitive values come out as dots — the run monitor displays the pending
// plan and must never leak a secret, not even to the person who started it.
type scenarioView struct {
	Name    string          `json:"name"`
	Tool    string          `json:"tool"`
	BaseURL string          `json:"base_url,omitempty"`
	Steps   []domain.Action `json:"steps"`
}

func toScenarioView(s domain.Scenario) scenarioView {
	return scenarioView{Name: s.Name, Tool: s.Tool, BaseURL: s.BaseURL, Steps: s.Steps}
}

// actionView serializes a domain.Action through its own MarshalJSON, which
// masks Value when Sensitive is set — so no view below can leak a secret.
type actionView = domain.Action

// attachmentView turns a disk path into a URL the browser can load.
type attachmentView struct {
	Name string `json:"name"`
	URL  string `json:"url"`
}

// stepView is the browser-facing projection of a StepResult. Sensitive
// values are masked by domain.Action's MarshalJSON; attachments become
// servable URLs instead of local paths.
type stepView struct {
	Index       int              `json:"index"`
	Action      actionView       `json:"action"`
	Status      domain.Status    `json:"status"`
	Error       string           `json:"error,omitempty"`
	Output      string           `json:"output,omitempty"`
	Log         string           `json:"log,omitempty"`
	Attachments []attachmentView `json:"attachments,omitempty"`
	StartedAt   time.Time        `json:"started_at"`
	DurationMS  int64            `json:"duration_ms"`
}

// resultView is the browser-facing projection of a ScenarioResult.
type resultView struct {
	RunID      string        `json:"run_id"`
	Scenario   string        `json:"scenario_name"`
	Tool       string        `json:"tool"`
	Status     domain.Status `json:"status"`
	StartedAt  time.Time     `json:"started_at"`
	DurationMS int64         `json:"duration_ms"`
	Steps      []stepView    `json:"steps"`
}

// scenarioMeta is one row of the scenario list.
type scenarioMeta struct {
	ID        string `json:"id"` // file identifier (used in URLs)
	Name      string `json:"name"`
	Tool      string `json:"tool"`
	BaseURL   string `json:"base_url,omitempty"`
	Steps     int    `json:"steps"`
	File      string `json:"file"`
	LastRun   string `json:"last_run_status,omitempty"`
	LastAt    string `json:"last_run_at,omitempty"`
	LastRunID string `json:"last_run_id,omitempty"`
}

// runView is the response of GET /api/run/{id} and the payload of the
// "done" SSE event.
type runView struct {
	RunID       string       `json:"run_id"`
	Status      string       `json:"status"` // running | passed | failed
	Scenario    scenarioView `json:"scenario"`
	StepCount   int          `json:"step_count"`
	CurrentStep int          `json:"current_step"`
	Steps       []stepView   `json:"steps"`
	ElapsedMS   int64        `json:"elapsed_ms"`
	Error       string       `json:"error,omitempty"`
	Result      *resultView  `json:"result,omitempty"`
}

// resultSummary is one row of the results list.
type resultSummary struct {
	RunID        string `json:"run_id"`
	ScenarioName string `json:"scenario_name"`
	Tool         string `json:"tool"`
	Status       string `json:"status"`
	StartedAt    string `json:"started_at"`
	DurationMS   int64  `json:"duration_ms"`
}

// ---------------------------------------------------------------------------
// Small helpers
// ---------------------------------------------------------------------------

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(v)
}

func writeError(w http.ResponseWriter, status int, format string, args ...any) {
	writeJSON(w, status, map[string]string{"error": fmt.Sprintf(format, args...)})
}

// sanitizeFileID turns a scenario identifier into a safe filename stem:
// path separators and parent references are stripped, so an API client can
// never escape the scenarios directory.
func sanitizeFileID(id string) string {
	id = strings.Map(func(r rune) rune {
		switch r {
		case '/', '\\', ':', '*', '?', '"', '<', '>', '|':
			return '_'
		}
		return r
	}, id)
	id = strings.ReplaceAll(id, "..", "")
	id = strings.TrimSpace(id)
	id = strings.Trim(id, "._")
	if id == "" {
		id = "_"
	}
	return id
}

func (s *Server) scenarioPath(id string) string {
	return filepath.Join(s.scenarioDir, sanitizeFileID(id)+".json")
}

// loadApp reads the app config on demand, so config edits made through the
// UI take effect on the very next run without restarting the server.
func (s *Server) loadApp() (config.App, error) {
	return config.LoadApp(s.configPath)
}

func (s *Server) attachmentURL(att domain.Attachment) string {
	if att.Path == "" {
		return ""
	}
	return "/api/screenshots/" + url.PathEscape(filepath.Base(att.Path))
}

func (s *Server) makeStepView(index int, sr domain.StepResult) stepView {
	v := stepView{
		Index:      index,
		Action:     sr.Action,
		Status:     sr.Status,
		Error:      sr.Error,
		Output:     sr.Output,
		Log:        sr.Log,
		StartedAt:  sr.StartedAt,
		DurationMS: sr.Duration.Milliseconds(),
	}
	for _, att := range sr.Attachments {
		if u := s.attachmentURL(att); u != "" {
			v.Attachments = append(v.Attachments, attachmentView{Name: att.Name, URL: u})
		}
	}
	return v
}

func (s *Server) makeResultView(runID string, res domain.ScenarioResult) resultView {
	v := resultView{
		RunID:      runID,
		Scenario:   res.ScenarioName,
		Tool:       res.Tool,
		Status:     res.Status,
		StartedAt:  res.StartedAt,
		DurationMS: res.Duration.Milliseconds(),
		Steps:      make([]stepView, 0, len(res.Steps)),
	}
	for i, sr := range res.Steps {
		v.Steps = append(v.Steps, s.makeStepView(i, sr))
	}
	return v
}

func (s *Server) makeRunView(snap runSnapshot) runView {
	v := runView{
		RunID:       snap.State.ID,
		Status:      snap.State.Status,
		Scenario:    toScenarioView(snap.State.Scenario),
		StepCount:   len(snap.State.Scenario.Steps),
		CurrentStep: len(snap.Steps),
		Steps:       make([]stepView, 0, len(snap.Steps)),
		Error:       snap.State.Error,
	}
	if snap.State.Status == "running" {
		v.ElapsedMS = time.Since(snap.State.StartedAt).Milliseconds()
	} else if snap.State.Result != nil {
		v.ElapsedMS = snap.State.Result.Duration.Milliseconds()
		v.Result = new(resultView)
		*v.Result = s.makeResultView(snap.State.ID, *snap.State.Result)
	}
	for i, sr := range snap.Steps {
		v.Steps = append(v.Steps, s.makeStepView(i, sr))
	}
	return v
}

// ---------------------------------------------------------------------------
// Scenario handlers
// ---------------------------------------------------------------------------

// handleListScenarios GET /api/scenarios
func (s *Server) handleListScenarios(w http.ResponseWriter, r *http.Request) {
	entries, err := os.ReadDir(s.scenarioDir)
	if err != nil && !os.IsNotExist(err) {
		writeError(w, http.StatusInternalServerError, "reading scenarios dir: %v", err)
		return
	}
	metas := make([]scenarioMeta, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		sc, err := config.LoadScenario(filepath.Join(s.scenarioDir, e.Name()))
		if err != nil {
			continue // unparseable files are skipped, not fatal
		}
		meta := scenarioMeta{
			ID:      strings.TrimSuffix(e.Name(), ".json"),
			Name:    sc.Name,
			Tool:    sc.Tool,
			BaseURL: sc.BaseURL,
			Steps:   len(sc.Steps),
			File:    e.Name(),
		}
		if last, ok := s.async.LastForScenario(sc.Name); ok {
			meta.LastRun = last.Status
			meta.LastRunID = last.ID
			meta.LastAt = last.StartedAt.Format(time.RFC3339)
		}
		metas = append(metas, meta)
	}
	sort.Slice(metas, func(i, j int) bool { return metas[i].Name < metas[j].Name })
	writeJSON(w, http.StatusOK, metas)
}

// handleGetScenario GET /api/scenarios/{id}
func (s *Server) handleGetScenario(w http.ResponseWriter, r *http.Request) {
	sc, err := config.LoadScenario(s.scenarioPath(r.PathValue("id")))
	if err != nil {
		writeError(w, http.StatusNotFound, "scenario not found: %v", err)
		return
	}
	writeJSON(w, http.StatusOK, toRaw(sc))
}

// handleSaveScenario POST /api/scenarios (create) and PUT /api/scenarios/{id} (update).
// The body is a rawScenario; the file is named after the scenario's own
// name field, sanitized.
func (s *Server) handleSaveScenario(w http.ResponseWriter, r *http.Request) {
	var body rawScenario
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body: %v", err)
		return
	}
	sc := body.toDomain()
	if err := sc.Validate(); err != nil {
		writeError(w, http.StatusBadRequest, "%v", err)
		return
	}
	if id := r.PathValue("id"); id != "" && sanitizeFileID(id) != sanitizeFileID(sc.Name) {
		// Rename: delete the old file after writing the new one.
		oldPath := s.scenarioPath(id)
		newPath := s.scenarioPath(sc.Name)
		if err := s.writeScenarioFile(newPath, sc); err != nil {
			writeError(w, http.StatusInternalServerError, "saving scenario: %v", err)
			return
		}
		if oldPath != newPath {
			if err := os.Remove(oldPath); err != nil && !os.IsNotExist(err) {
				s.logger.Warn("removing old scenario file after rename", "error", err)
			}
		}
		writeJSON(w, http.StatusOK, map[string]string{"id": sanitizeFileID(sc.Name)})
		return
	}
	if err := s.writeScenarioFile(s.scenarioPath(sc.Name), sc); err != nil {
		writeError(w, http.StatusInternalServerError, "saving scenario: %v", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"id": sanitizeFileID(sc.Name)})
}

func (s *Server) writeScenarioFile(path string, sc domain.Scenario) error {
	if err := os.MkdirAll(s.scenarioDir, 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(toRaw(sc), "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o644)
}

// handleDeleteScenario DELETE /api/scenarios/{id}
func (s *Server) handleDeleteScenario(w http.ResponseWriter, r *http.Request) {
	if err := os.Remove(s.scenarioPath(r.PathValue("id"))); err != nil {
		if os.IsNotExist(err) {
			writeError(w, http.StatusNotFound, "scenario not found")
		} else {
			writeError(w, http.StatusInternalServerError, "deleting scenario: %v", err)
		}
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---------------------------------------------------------------------------
// Config + tools handlers
// ---------------------------------------------------------------------------

// handleGetConfig GET /api/config
func (s *Server) handleGetConfig(w http.ResponseWriter, r *http.Request) {
	cfg, err := s.loadApp()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "loading config: %v", err)
		return
	}
	writeJSON(w, http.StatusOK, cfg)
}

// handlePutConfig PUT /api/config — replaces the whole app config file.
func (s *Server) handlePutConfig(w http.ResponseWriter, r *http.Request) {
	raw := json.RawMessage{}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&raw); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body: %v", err)
		return
	}
	var cfg config.App // validate it parses before touching disk
	if err := json.Unmarshal(raw, &cfg); err != nil {
		writeError(w, http.StatusBadRequest, "invalid config: %v", err)
		return
	}
	pretty, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		writeError(w, http.StatusInternalServerError, "encoding config: %v", err)
		return
	}
	if err := os.WriteFile(s.configPath, append(pretty, '\n'), 0o644); err != nil {
		writeError(w, http.StatusInternalServerError, "writing config: %v", err)
		return
	}
	writeJSON(w, http.StatusOK, cfg)
}

// handleListTools GET /api/tools
func (s *Server) handleListTools(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"tools": s.registry.Names()})
}

// ---------------------------------------------------------------------------
// Run handlers
// ---------------------------------------------------------------------------

// handleStartRun POST /api/run — body: {"scenario": "<id or name>", "tool": "<optional override>"}
func (s *Server) handleStartRun(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Scenario string `json:"scenario"`
		Tool     string `json:"tool,omitempty"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body: %v", err)
		return
	}
	sc, err := s.resolveScenario(body.Scenario)
	if err != nil {
		writeError(w, http.StatusNotFound, "%v", err)
		return
	}
	if body.Tool != "" {
		sc.Tool = body.Tool
	}
	appCfg, err := s.loadApp()
	if err != nil {
		s.logger.Warn("loading app config for run; proceeding with defaults", "error", err)
		appCfg = config.App{}
	}
	if appCfg.Tools == nil {
		appCfg.Tools = map[string]map[string]interface{}{}
	}
	if _, ok := appCfg.Tools[sc.Tool]; !ok {
		appCfg.Tools[sc.Tool] = map[string]interface{}{}
	}
	runID := s.async.Start(sc, appCfg.Tools[sc.Tool])
	writeJSON(w, http.StatusCreated, map[string]string{"run_id": runID})
}

// resolveScenario finds a scenario by file id first, then by its display
// name, so the frontend can pass whichever it has.
func (s *Server) resolveScenario(ref string) (domain.Scenario, error) {
	if sc, err := config.LoadScenario(s.scenarioPath(ref)); err == nil {
		return sc, nil
	}
	entries, err := os.ReadDir(s.scenarioDir)
	if err != nil {
		return domain.Scenario{}, fmt.Errorf("scenario %q not found", ref)
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		sc, err := config.LoadScenario(filepath.Join(s.scenarioDir, e.Name()))
		if err == nil && sc.Name == ref {
			return sc, nil
		}
	}
	return domain.Scenario{}, fmt.Errorf("scenario %q not found", ref)
}

// handleGetRun GET /api/run/{id}
func (s *Server) handleGetRun(w http.ResponseWriter, r *http.Request) {
	snap, ok := s.async.Get(r.PathValue("id"))
	if !ok {
		writeError(w, http.StatusNotFound, "run not found")
		return
	}
	writeJSON(w, http.StatusOK, s.makeRunView(snap))
}

// handleStopRun POST /api/run/{id}/stop
func (s *Server) handleStopRun(w http.ResponseWriter, r *http.Request) {
	if !s.async.Stop(r.PathValue("id")) {
		writeError(w, http.StatusConflict, "run is not running")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "stopping"})
}

// handleRunEvents GET /api/run/{id}/events — Server-Sent Events stream.
// Buffered events are replayed first, so a page opened mid-run (or
// refreshed) sees the full progress history, then live events until the
// terminal "done" event.
func (s *Server) handleRunEvents(w http.ResponseWriter, r *http.Request) {
	runID := r.PathValue("id")
	events, cancel, ok := s.async.Subscribe(runID)
	if !ok {
		writeError(w, http.StatusNotFound, "run not found")
		return
	}
	defer cancel()

	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming unsupported")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	send := func(ev RunEvent) bool {
		data, err := json.Marshal(ev)
		if err != nil {
			return true
		}
		if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", ev.Event, data); err != nil {
			return false
		}
		flusher.Flush()
		return true
	}

	for {
		select {
		case <-r.Context().Done():
			return
		case ev, ok := <-events:
			if !ok || !send(ev) {
				return
			}
			if ev.Event == EventDone {
				return
			}
		}
	}
}

// ---------------------------------------------------------------------------
// Results + screenshots + report handlers
// ---------------------------------------------------------------------------

// handleListResults GET /api/results — in-memory run history, newest first,
// plus the last on-disk JSON report entry when present (runs from previous
// CLI invocations).
func (s *Server) handleListResults(w http.ResponseWriter, r *http.Request) {
	runs := s.async.List()
	out := make([]resultSummary, 0, len(runs)+1)
	for _, run := range runs {
		out = append(out, resultSummary{
			RunID:        run.ID,
			ScenarioName: run.ScenarioName,
			Tool:         run.Tool,
			Status:       run.Status,
			StartedAt:    run.StartedAt.Format(time.RFC3339),
			DurationMS:   run.DurationMS,
		})
	}
	writeJSON(w, http.StatusOK, out)
}

// handleGetResult GET /api/results/{id}
func (s *Server) handleGetResult(w http.ResponseWriter, r *http.Request) {
	res, ok := s.async.Result(r.PathValue("id"))
	if !ok {
		writeError(w, http.StatusNotFound, "result not found (still running?)")
		return
	}
	writeJSON(w, http.StatusOK, s.makeResultView(r.PathValue("id"), res))
}

// handleScreenshot GET /api/screenshots/{name} — serves a PNG from the
// screenshots dir. The name is sanitized to a bare filename, so path
// traversal ("..", separators) can never escape the directory.
func (s *Server) handleScreenshot(w http.ResponseWriter, r *http.Request) {
	name := filepath.Base(filepath.FromSlash(r.PathValue("name")))
	if name == "." || name == ".." || strings.ContainsAny(name, `\/`) {
		writeError(w, http.StatusBadRequest, "invalid screenshot name")
		return
	}
	path := filepath.Join(s.screenshotDir, name)
	data, err := os.ReadFile(path)
	if err != nil {
		writeError(w, http.StatusNotFound, "screenshot not found")
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "no-cache")
	_, _ = w.Write(data)
}

// handleReport GET /api/report — raw content of the JSON report file, if
// the config points at one.
func (s *Server) handleReport(w http.ResponseWriter, r *http.Request) {
	cfg, err := s.loadApp()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "loading config: %v", err)
		return
	}
	if cfg.Reporters.JSONPath == "" {
		writeError(w, http.StatusNotFound, "no json_path reporter configured")
		return
	}
	data, err := os.ReadFile(cfg.Reporters.JSONPath)
	if err != nil {
		writeError(w, http.StatusNotFound, "report file not found: %v", err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(data)
}
