package ui_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"uitester/internal/domain"
	"uitester/internal/ui"
	"uitester/internal/usecase"
)

// ---------------------------------------------------------------------------
// fakes: an in-memory driver so the API tests exercise real Runner plumbing
// ---------------------------------------------------------------------------

type fakeDriver struct {
	texts map[string]string
}

func (f *fakeDriver) Connect(ctx context.Context) error              { return nil }
func (f *fakeDriver) Navigate(ctx context.Context, url string) error { return nil }
func (f *fakeDriver) Click(ctx context.Context, selector string) error {
	return nil
}
func (f *fakeDriver) Input(ctx context.Context, selector, text string) error {
	f.texts[selector] = text
	return nil
}
func (f *fakeDriver) WaitFor(ctx context.Context, selector string, timeout time.Duration) error {
	return nil
}
func (f *fakeDriver) GetText(ctx context.Context, selector string) (string, error) {
	return f.texts[selector], nil
}
func (f *fakeDriver) Screenshot(ctx context.Context) ([]byte, error) {
	return []byte("fake-png-bytes"), nil
}
func (f *fakeDriver) Close(ctx context.Context) error { return nil }

type fakeConnector struct{ driver *fakeDriver }

func (c fakeConnector) Name() string { return "fake" }
func (c fakeConnector) NewDriver(cfg map[string]interface{}) (domain.UIDriver, error) {
	return c.driver, nil
}

// ---------------------------------------------------------------------------
// harness
// ---------------------------------------------------------------------------

type testEnv struct {
	srv      *httptest.Server
	client   *http.Client
	scenDir  string
	cfgPath  string
	shotDir  string
	registry *usecase.ToolRegistry
}

func newEnv(t *testing.T) *testEnv {
	t.Helper()
	root := t.TempDir()
	env := &testEnv{
		scenDir: filepath.Join(root, "scenarios"),
		cfgPath: filepath.Join(root, "config.json"),
		shotDir: filepath.Join(root, "screenshots"),
	}
	if err := os.MkdirAll(env.scenDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(env.shotDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(env.cfgPath, []byte(`{"tools": {"fake": {}}}`), 0o644); err != nil {
		t.Fatal(err)
	}

	env.registry = usecase.NewToolRegistry()
	if err := env.registry.Register(fakeConnector{driver: &fakeDriver{texts: map[string]string{}}}); err != nil {
		t.Fatalf("register fake tool: %v", err)
	}
	runner := usecase.NewRunnerWithOptions(env.registry, slog.New(slog.DiscardHandler),
		usecase.Options{AutoScreenshot: true, ScreenshotDir: env.shotDir})

	srv := ui.NewServer(env.registry, runner, env.cfgPath, env.scenDir, env.shotDir, slog.New(slog.DiscardHandler))
	env.srv = httptest.NewServer(srv.Handler())
	env.client = env.srv.Client()
	t.Cleanup(env.srv.Close)
	return env
}

func (e *testEnv) do(t *testing.T, method, path string, body any) (int, []byte) {
	t.Helper()
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, e.srv.URL+path, rd)
	if err != nil {
		t.Fatal(err)
	}
	res, err := e.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	data, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	return res.StatusCode, data
}

func decode(t *testing.T, data []byte, v any) {
	t.Helper()
	if err := json.Unmarshal(data, v); err != nil {
		t.Fatalf("decoding %s: %v", string(data), err)
	}
}

func writeScenario(t *testing.T, dir, name string, steps []domain.Action) {
	t.Helper()
	sc := domain.Scenario{Name: name, Tool: "fake", Steps: steps}
	data, err := json.Marshal(sc)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name+".json"), data, 0o644); err != nil {
		t.Fatal(err)
	}
}

// ---------------------------------------------------------------------------
// scenario CRUD
// ---------------------------------------------------------------------------

func TestAPI_ScenarioCRUD(t *testing.T) {
	env := newEnv(t)

	// Empty list.
	code, body := env.do(t, "GET", "/api/scenarios", nil)
	if code != 200 {
		t.Fatalf("list: status %d: %s", code, body)
	}
	var list []map[string]any
	decode(t, body, &list)
	if len(list) != 0 {
		t.Fatalf("expected empty list, got %v", list)
	}

	// Create via POST.
	sc := map[string]any{
		"name": "login",
		"tool": "fake",
		"steps": []map[string]any{
			{"type": "input", "selector": "#user", "value": "Ada"},
			{"type": "click", "selector": "#go"},
		},
	}
	code, body = env.do(t, "POST", "/api/scenarios", sc)
	if code != 200 {
		t.Fatalf("create: status %d: %s", code, body)
	}
	var created map[string]string
	decode(t, body, &created)
	if created["id"] != "login" {
		t.Fatalf("expected id login, got %v", created)
	}
	if _, err := os.Stat(filepath.Join(env.scenDir, "login.json")); err != nil {
		t.Fatalf("scenario file missing: %v", err)
	}

	// List shows one entry.
	_, body = env.do(t, "GET", "/api/scenarios", nil)
	decode(t, body, &list)
	if len(list) != 1 || list[0]["name"] != "login" {
		t.Fatalf("unexpected list: %v", list)
	}

	// GET one.
	code, body = env.do(t, "GET", "/api/scenarios/login", nil)
	if code != 200 {
		t.Fatalf("get: status %d: %s", code, body)
	}
	var got map[string]any
	decode(t, body, &got)
	if got["name"] != "login" {
		t.Fatalf("unexpected scenario: %v", got)
	}

	// PUT rename: file is rewritten under the new name, old file removed.
	sc["name"] = "login-renamed"
	code, body = env.do(t, "PUT", "/api/scenarios/login", sc)
	if code != 200 {
		t.Fatalf("rename: status %d: %s", code, body)
	}
	if _, err := os.Stat(filepath.Join(env.scenDir, "login-renamed.json")); err != nil {
		t.Fatalf("renamed file missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(env.scenDir, "login.json")); !os.IsNotExist(err) {
		t.Fatalf("old file should be gone after rename, stat err=%v", err)
	}

	// DELETE.
	code, _ = env.do(t, "DELETE", "/api/scenarios/login-renamed", nil)
	if code != 204 {
		t.Fatalf("delete: status %d", code)
	}
	code, _ = env.do(t, "GET", "/api/scenarios/login-renamed", nil)
	if code != 404 {
		t.Fatalf("expected 404 after delete, got %d", code)
	}
}

func TestAPI_SaveScenarioValidation(t *testing.T) {
	env := newEnv(t)

	// Missing name.
	code, body := env.do(t, "POST", "/api/scenarios", map[string]any{
		"tool": "fake", "steps": []map[string]any{{"type": "click", "selector": "#x"}},
	})
	if code != 400 {
		t.Fatalf("expected 400 for nameless scenario, got %d: %s", code, body)
	}

	// No steps.
	code, _ = env.do(t, "POST", "/api/scenarios", map[string]any{
		"name": "empty", "tool": "fake", "steps": []map[string]any{},
	})
	if code != 400 {
		t.Fatalf("expected 400 for stepless scenario, got %d", code)
	}

	// Broken JSON.
	req, err := http.NewRequest("POST", env.srv.URL+"/api/scenarios", strings.NewReader("{nope"))
	if err != nil {
		t.Fatal(err)
	}
	res, err := env.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != 400 {
		t.Fatalf("expected 400 for broken JSON, got %d", res.StatusCode)
	}
}

// The editor must round-trip sensitive values verbatim (the file keeps the
// real secret; masking is purely a frontend display concern), while every
// result-oriented view must mask them.
func TestAPI_SensitiveRoundTripAndMasking(t *testing.T) {
	env := newEnv(t)

	sc := map[string]any{
		"name": "secret-flow",
		"tool": "fake",
		"steps": []map[string]any{
			{"type": "input", "selector": "#pw", "value": "hunter2", "sensitive": true},
		},
	}
	code, body := env.do(t, "POST", "/api/scenarios", sc)
	if code != 200 {
		t.Fatalf("create: status %d: %s", code, body)
	}

	// Editor view: the secret is returned as-is so a no-op save doesn't
	// destroy the stored value.
	_, body = env.do(t, "GET", "/api/scenarios/secret-flow", nil)
	if !strings.Contains(string(body), "hunter2") {
		t.Fatalf("editor view must round-trip the sensitive value verbatim, got: %s", body)
	}

	// Run it, then check every result surface masks the value.
	_, body = env.do(t, "POST", "/api/run", map[string]string{"scenario": "secret-flow"})
	var started map[string]string
	decode(t, body, &started)
	runID := started["run_id"]

	var runView map[string]any
	for i := 0; i < 50; i++ {
		_, body = env.do(t, "GET", "/api/run/"+runID, nil)
		decode(t, body, &runView)
		if runView["status"] != "running" {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if runView["status"] == "running" {
		t.Fatal("run did not finish in time")
	}
	// runView carries the scenario for the *plan* (pending steps), which is
	// editor-like data — but its Steps results must mask.
	if bytes.Contains(body, []byte("hunter2")) {
		t.Fatalf("run view leaked sensitive value: %s", body)
	}

	_, body = env.do(t, "GET", "/api/results/"+runID, nil)
	if bytes.Contains(body, []byte("hunter2")) {
		t.Fatalf("result view leaked sensitive value: %s", body)
	}
	if !strings.Contains(string(body), "••••••") {
		t.Fatalf("result view should contain the mask, got: %s", body)
	}
}

// ---------------------------------------------------------------------------
// config + tools
// ---------------------------------------------------------------------------

func TestAPI_ConfigGetPut(t *testing.T) {
	env := newEnv(t)

	code, body := env.do(t, "GET", "/api/config", nil)
	if code != 200 {
		t.Fatalf("get config: status %d: %s", code, body)
	}

	newCfg := map[string]any{
		"tools":     map[string]any{"fake": map[string]any{"headless": true}},
		"reporters": map[string]any{"json_path": "results/report.json"},
	}
	code, body = env.do(t, "PUT", "/api/config", newCfg)
	if code != 200 {
		t.Fatalf("put config: status %d: %s", code, body)
	}
	onDisk, err := os.ReadFile(env.cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(onDisk), "headless") {
		t.Fatalf("config not persisted: %s", onDisk)
	}

	// Invalid config is rejected before touching disk.
	code, _ = env.do(t, "PUT", "/api/config", map[string]any{"tools": "not-a-map"})
	if code != 400 {
		t.Fatalf("expected 400 for invalid config, got %d", code)
	}
}

func TestAPI_ToolsList(t *testing.T) {
	env := newEnv(t)
	code, body := env.do(t, "GET", "/api/tools", nil)
	if code != 200 {
		t.Fatalf("tools: status %d: %s", code, body)
	}
	var res map[string]any
	decode(t, body, &res)
	tools, _ := res["tools"].([]any)
	if len(tools) != 1 || tools[0] != "fake" {
		t.Fatalf("expected [fake], got %v", tools)
	}
}

// ---------------------------------------------------------------------------
// run lifecycle
// ---------------------------------------------------------------------------

func TestAPI_RunLifecycle(t *testing.T) {
	env := newEnv(t)
	writeScenario(t, env.scenDir, "smoke", []domain.Action{
		{Type: domain.ActionInput, Selector: "#user", Value: "Ada"},
		{Type: domain.ActionClick, Selector: "#go"},
	})

	// Unknown scenario -> 404.
	code, body := env.do(t, "POST", "/api/run", map[string]string{"scenario": "nope"})
	if code != 404 {
		t.Fatalf("expected 404 for unknown scenario, got %d: %s", code, body)
	}

	// Start by file id.
	code, body = env.do(t, "POST", "/api/run", map[string]string{"scenario": "smoke"})
	if code != 201 {
		t.Fatalf("start run: status %d: %s", code, body)
	}
	var started map[string]string
	decode(t, body, &started)
	runID := started["run_id"]
	if runID == "" {
		t.Fatal("empty run_id")
	}

	// Poll until finished and assert the view shape.
	var run map[string]any
	deadline := time.Now().Add(5 * time.Second)
	for {
		_, body = env.do(t, "GET", "/api/run/"+runID, nil)
		decode(t, body, &run)
		if run["status"] != "running" || time.Now().After(deadline) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if run["status"] != "passed" {
		t.Fatalf("expected passed run, got: %s", body)
	}
	if run["step_count"].(float64) != 2 {
		t.Fatalf("expected step_count 2, got %v", run["step_count"])
	}
	steps := run["steps"].([]any)
	if len(steps) != 2 {
		t.Fatalf("expected 2 step views, got %d", len(steps))
	}
	for _, s := range steps {
		sv := s.(map[string]any)
		if sv["status"] != "passed" {
			t.Fatalf("step not passed: %v", sv)
		}
		atts, _ := sv["attachments"].([]any)
		if len(atts) != 1 {
			t.Fatalf("expected auto-screenshot attachment, got %v", atts)
		}
		att := atts[0].(map[string]any)
		url := att["url"].(string)
		if !strings.HasPrefix(url, "/api/screenshots/") {
			t.Fatalf("attachment url should be servable, got %q", url)
		}
		// The attachment URL must actually serve the PNG.
		code, body = env.do(t, "GET", url, nil)
		if code != 200 {
			t.Fatalf("serving %s: status %d", url, code)
		}
	}

	// Results list contains the run, newest first.
	_, body = env.do(t, "GET", "/api/results", nil)
	var results []map[string]any
	decode(t, body, &results)
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0]["scenario_name"] != "smoke" || results[0]["status"] != "passed" {
		t.Fatalf("unexpected result summary: %v", results[0])
	}

	// Result detail.
	_, body = env.do(t, "GET", "/api/results/"+runID, nil)
	var detail map[string]any
	decode(t, body, &detail)
	if detail["scenario_name"] != "smoke" {
		t.Fatalf("unexpected result detail: %v", detail)
	}

	// Scenario list now carries the last-run badge.
	_, body = env.do(t, "GET", "/api/scenarios", nil)
	var metas []map[string]any
	decode(t, body, &metas)
	if metas[0]["last_run_status"] != "passed" {
		t.Fatalf("expected last_run_status badge, got %v", metas[0])
	}

	// Stop on a finished run conflicts.
	code, _ = env.do(t, "POST", "/api/run/"+runID+"/stop", nil)
	if code != 409 {
		t.Fatalf("expected 409 stopping a finished run, got %d", code)
	}

	// Unknown run id -> 404.
	code, _ = env.do(t, "GET", "/api/run/deadbeef", nil)
	if code != 404 {
		t.Fatalf("expected 404 for unknown run, got %d", code)
	}
}

// Start by display name (not file id) must also work — the frontend sends
// whichever identifier it has.
func TestAPI_RunStartsByName(t *testing.T) {
	env := newEnv(t)
	// File id "file-name", display name "Display Name" inside the JSON.
	sc := domain.Scenario{
		Name:  "Display Name",
		Tool:  "fake",
		Steps: []domain.Action{{Type: domain.ActionClick, Selector: "#go"}},
	}
	data, err := json.Marshal(sc)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(env.scenDir, "file-name.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}

	code, body := env.do(t, "POST", "/api/run", map[string]string{"scenario": "Display Name"})
	if code != 201 {
		t.Fatalf("start by name: status %d: %s", code, body)
	}
}

func TestAPI_RunMissingConfigUsesDefaults(t *testing.T) {
	env := newEnv(t)
	// Point the server at a config path that doesn't exist; the run must
	// still start with empty (default) tool options instead of panicking.
	runner := usecase.NewRunnerWithOptions(env.registry, slog.New(slog.DiscardHandler),
		usecase.Options{AutoScreenshot: false})
	srv := ui.NewServer(env.registry, runner, filepath.Join(t.TempDir(), "missing.json"), env.scenDir, env.shotDir, nil)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	writeScenario(t, env.scenDir, "cfgless", []domain.Action{
		{Type: domain.ActionClick, Selector: "#go"},
	})
	res, err := ts.Client().Post(ts.URL+"/api/run", "application/json",
		strings.NewReader(`{"scenario":"cfgless"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != 201 {
		b, _ := io.ReadAll(res.Body)
		t.Fatalf("run without config file: status %d: %s", res.StatusCode, b)
	}
}

// ---------------------------------------------------------------------------
// SSE
// ---------------------------------------------------------------------------

func TestAPI_RunEventsStream(t *testing.T) {
	env := newEnv(t)
	writeScenario(t, env.scenDir, "streamy", []domain.Action{
		{Type: domain.ActionClick, Selector: "#a"},
		{Type: domain.ActionClick, Selector: "#b"},
	})

	_, body := env.do(t, "POST", "/api/run", map[string]string{"scenario": "streamy"})
	var started map[string]string
	decode(t, body, &started)
	runID := started["run_id"]

	// Subscribe while the run may already be over: the replayed buffer must
	// still deliver every step plus the terminal done event.
	req, err := http.NewRequest("GET", env.srv.URL+"/api/run/"+runID+"/events", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Accept", "text/event-stream")
	res, err := env.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if ct := res.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("expected event-stream content type, got %q", ct)
	}

	sawSteps, sawDone := 0, false
	scan := bufio.NewScanner(res.Body)
	scan.Buffer(make([]byte, 64*1024), 1024*1024)
	for scan.Scan() {
		line := scan.Text()
		switch {
		case strings.HasPrefix(line, "event: step"):
			sawSteps++
		case strings.HasPrefix(line, "event: done"):
			sawDone = true
		}
	}
	if err := scan.Err(); err != nil {
		t.Fatal(err)
	}
	if sawSteps != 2 {
		t.Fatalf("expected 2 step events, got %d", sawSteps)
	}
	if !sawDone {
		t.Fatal("stream closed without a done event")
	}

	// Unknown run -> 404, no stream.
	code, _ := env.do(t, "GET", "/api/run/none/events", nil)
	if code != 404 {
		t.Fatalf("expected 404 subscribing to unknown run, got %d", code)
	}
}

// ---------------------------------------------------------------------------
// screenshots + security
// ---------------------------------------------------------------------------

func TestAPI_ScreenshotTraversalBlocked(t *testing.T) {
	env := newEnv(t)
	if err := os.WriteFile(filepath.Join(env.shotDir, "ok.png"), []byte("png"), 0o644); err != nil {
		t.Fatal(err)
	}

	code, _ := env.do(t, "GET", "/api/screenshots/ok.png", nil)
	if code != 200 {
		t.Fatalf("serving ok.png: status %d", code)
	}

	secret := filepath.Join(t.TempDir(), "secret.txt")
	if err := os.WriteFile(secret, []byte("gotcha"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, evil := range []string{"..%2F..%2Fsecret.txt", "..\\secret.txt", ".."} {
		code, body := env.do(t, "GET", "/api/screenshots/"+evil, nil)
		if code == 200 && bytes.Contains(body, []byte("gotcha")) {
			t.Fatalf("traversal %q escaped the screenshots dir!", evil)
		}
	}
}

func TestAPI_ScenarioIDSanitized(t *testing.T) {
	env := newEnv(t)

	// A scenario "name" that would try to escape the dir gets sanitized
	// into a safe file name inside the scenarios dir.
	sc := map[string]any{
		"name":  "../../evil",
		"tool":  "fake",
		"steps": []map[string]any{{"type": "click", "selector": "#x"}},
	}
	code, body := env.do(t, "POST", "/api/scenarios", sc)
	if code != 200 {
		t.Fatalf("create: status %d: %s", code, body)
	}
	var created map[string]string
	decode(t, body, &created)
	if created["id"] != "evil" {
		t.Fatalf("expected sanitized id 'evil', got %q", created["id"])
	}
	// The file must live inside the scenario dir, nowhere else.
	if _, err := os.Stat(filepath.Join(env.scenDir, "evil.json")); err != nil {
		t.Fatalf("sanitized scenario file missing: %v", err)
	}
}

// ---------------------------------------------------------------------------
// SPA
// ---------------------------------------------------------------------------

func TestSPA_ServesIndexAndFallback(t *testing.T) {
	env := newEnv(t)

	code, body := env.do(t, "GET", "/", nil)
	if code != 200 {
		t.Fatalf("index: status %d", code)
	}
	if !bytes.Contains(body, []byte("<!DOCTYPE html>")) {
		t.Fatal("index.html not served at /")
	}

	// Client-side route falls back to the SPA shell.
	code, body = env.do(t, "GET", "/some/deep/route", nil)
	if code != 200 || !bytes.Contains(body, []byte("<!DOCTYPE html>")) {
		t.Fatalf("SPA fallback broken: status %d", code)
	}

	// Static assets are served.
	code, body = env.do(t, "GET", "/styles.css", nil)
	if code != 200 || !bytes.Contains(body, []byte("--accent")) {
		t.Fatalf("styles.css not served: status %d", code)
	}
	code, body = env.do(t, "GET", "/app.js", nil)
	if code != 200 || !bytes.Contains(body, []byte("renderRoute")) {
		t.Fatalf("app.js not served: status %d", code)
	}
}

// ---------------------------------------------------------------------------
// unit: sanitizeFileID via the exported surface (indirect, through paths)
// ---------------------------------------------------------------------------

func TestAPI_ReportEndpoint(t *testing.T) {
	env := newEnv(t)

	// No reporter configured yet.
	code, _ := env.do(t, "GET", "/api/report", nil)
	if code != 404 {
		t.Fatalf("expected 404 with no json_path reporter, got %d", code)
	}

	reportPath := filepath.Join(t.TempDir(), "report.json")
	if err := os.WriteFile(reportPath, []byte(`{"status":"passed"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := map[string]any{"reporters": map[string]any{"json_path": reportPath}}
	code, body := env.do(t, "PUT", "/api/config", cfg)
	if code != 200 {
		t.Fatalf("config save failed: %d %s", code, body)
	}

	code, body = env.do(t, "GET", "/api/report", nil)
	if code != 200 {
		t.Fatalf("report: status %d", code)
	}
	if !bytes.Contains(body, []byte("passed")) {
		t.Fatalf("unexpected report body: %s", body)
	}
}
