package usecase_test

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"uitester/internal/domain"
	"uitester/internal/usecase"
)

// fakeDriver is an in-memory domain.UIDriver used to test Runner's
// orchestration logic without touching a real browser or WebDriver server.
type fakeDriver struct {
	connected   bool
	closed      bool
	texts       map[string]string
	failClick   string // selector that Click() should fail on, if any
	navigateErr error
}

func newFakeDriver() *fakeDriver {
	return &fakeDriver{texts: map[string]string{}}
}

func (f *fakeDriver) Connect(ctx context.Context) error              { f.connected = true; return nil }
func (f *fakeDriver) Navigate(ctx context.Context, url string) error { return f.navigateErr }
func (f *fakeDriver) Click(ctx context.Context, selector string) error {
	if selector == f.failClick {
		return errors.New("simulated click failure")
	}
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
func (f *fakeDriver) Screenshot(ctx context.Context) ([]byte, error) { return []byte("png"), nil }
func (f *fakeDriver) Close(ctx context.Context) error                { f.closed = true; return nil }

type fakeConnector struct {
	name   string
	driver *fakeDriver
}

func (c fakeConnector) Name() string { return c.name }
func (c fakeConnector) NewDriver(cfg map[string]interface{}) (domain.UIDriver, error) {
	return c.driver, nil
}

type fakeReporter struct {
	results []domain.ScenarioResult
}

func (r *fakeReporter) Report(ctx context.Context, result domain.ScenarioResult) error {
	r.results = append(r.results, result)
	return nil
}

func newRunnerWithFake(t *testing.T, driver *fakeDriver, reporter *fakeReporter) *usecase.Runner {
	t.Helper()
	registry := usecase.NewToolRegistry()
	if err := registry.Register(fakeConnector{name: "fake", driver: driver}); err != nil {
		t.Fatalf("register: %v", err)
	}
	// Auto-screenshot is exercised here (screenshots land in a per-test
	// temp dir, not the repo) so TestRunner_PassesAllSteps also asserts
	// attachments end up on StepResult.
	opts := usecase.Options{AutoScreenshot: true, ScreenshotDir: t.TempDir()}
	return usecase.NewRunnerWithOptions(registry, nil, opts, reporter)
}

func TestRunner_PassesAllSteps(t *testing.T) {
	driver := newFakeDriver()
	reporter := &fakeReporter{}
	runner := newRunnerWithFake(t, driver, reporter)

	scenario := domain.Scenario{
		Name: "happy path",
		Tool: "fake",
		Steps: []domain.Action{
			{Type: domain.ActionInput, Selector: "#name", Value: "Ada"},
			{Type: domain.ActionAssertText, Selector: "#name", Value: "Ada"},
		},
	}

	result, err := runner.Run(context.Background(), scenario, nil)
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if result.Status != domain.StatusPassed {
		t.Fatalf("expected StatusPassed, got %s", result.Status)
	}
	if !driver.connected || !driver.closed {
		t.Fatalf("expected driver to be connected and closed, got connected=%v closed=%v", driver.connected, driver.closed)
	}
	if len(reporter.results) != 1 {
		t.Fatalf("expected exactly one reported result, got %d", len(reporter.results))
	}
	for _, step := range result.Steps {
		if len(step.Attachments) != 1 {
			t.Fatalf("expected 1 auto-captured screenshot attachment per step, got %d", len(step.Attachments))
		}
		if _, err := os.Stat(step.Attachments[0].Path); err != nil {
			t.Fatalf("expected screenshot file to exist on disk: %v", err)
		}
	}
}

func TestRunner_MasksSensitiveValueInLog(t *testing.T) {
	driver := newFakeDriver()
	reporter := &fakeReporter{}
	runner := newRunnerWithFake(t, driver, reporter)

	scenario := domain.Scenario{
		Name: "sensitive input",
		Tool: "fake",
		Steps: []domain.Action{
			{Type: domain.ActionInput, Selector: "#password", Value: "super-secret", Sensitive: true},
		},
	}

	result, err := runner.Run(context.Background(), scenario, nil)
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	step := result.Steps[0]
	if strings.Contains(step.Log, "super-secret") {
		t.Fatalf("expected sensitive value to be masked in log line, got: %s", step.Log)
	}
	// The raw value must still reach the driver (masking is report-only).
	if driver.texts["#password"] != "super-secret" {
		t.Fatalf("expected driver to receive the real value, got %q", driver.texts["#password"])
	}
}

func TestRunner_StopsOnFirstFailure(t *testing.T) {
	driver := newFakeDriver()
	driver.failClick = "#submit"
	reporter := &fakeReporter{}
	runner := newRunnerWithFake(t, driver, reporter)

	scenario := domain.Scenario{
		Name: "click fails",
		Tool: "fake",
		Steps: []domain.Action{
			{Type: domain.ActionClick, Selector: "#submit"},
			{Type: domain.ActionInput, Selector: "#name", Value: "should not run"},
		},
	}

	result, err := runner.Run(context.Background(), scenario, nil)
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if result.Status != domain.StatusFailed {
		t.Fatalf("expected StatusFailed, got %s", result.Status)
	}
	if len(result.Steps) != 1 {
		t.Fatalf("expected exactly 1 step result (fail-fast), got %d", len(result.Steps))
	}
	if _, ok := driver.texts["#name"]; ok {
		t.Fatalf("expected second step to be skipped after failure, but Input ran")
	}
}

func TestRunner_UnknownTool(t *testing.T) {
	registry := usecase.NewToolRegistry()
	runner := usecase.NewRunner(registry, nil)

	scenario := domain.Scenario{
		Name:  "missing tool",
		Tool:  "does-not-exist",
		Steps: []domain.Action{{Type: domain.ActionClick, Selector: "#x"}},
	}

	_, err := runner.Run(context.Background(), scenario, nil)
	if !errors.Is(err, domain.ErrToolNotRegistered) {
		t.Fatalf("expected ErrToolNotRegistered, got %v", err)
	}
}

func TestRunner_InvalidScenarioRejected(t *testing.T) {
	registry := usecase.NewToolRegistry()
	runner := usecase.NewRunner(registry, nil)

	_, err := runner.Run(context.Background(), domain.Scenario{}, nil)
	if err == nil {
		t.Fatal("expected validation error for empty scenario, got nil")
	}
}
