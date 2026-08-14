package usecase

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"
	"time"

	"uitester/internal/domain"
)

// defaultStepTimeout is used for wait_for/assert steps that don't specify
// their own Timeout.
const defaultStepTimeout = 10 * time.Second

// Options configures cross-cutting Runner behavior that isn't specific to
// any one scenario — currently just automatic screenshot capture.
type Options struct {
	// AutoScreenshot, when true (the default), captures a screenshot after
	// every step and attaches it to that step's result — the same "one
	// screenshot per step" behavior Allure reports are built around. Set
	// false to only capture on explicit `screenshot` steps.
	AutoScreenshot bool

	// ScreenshotDir is where auto-captured (and explicit, if the step
	// doesn't specify its own path) screenshots are written. Created if it
	// doesn't exist. Defaults to "results/screenshots".
	ScreenshotDir string
}

func (o Options) withDefaults() Options {
	if o.ScreenshotDir == "" {
		o.ScreenshotDir = "results/screenshots"
	}
	return o
}

// Runner is the application's single use case: "execute a Scenario against
// a chosen tool and report the outcome." It depends only on the domain
// ports (UIDriver, ToolConnector, Reporter) — never on chromedp, Selenium,
// JSON, or any other concrete technology, which is what makes it unit
// testable with fakes and swappable at the edges.
type Runner struct {
	registry  *ToolRegistry
	reporters []domain.Reporter
	logger    *slog.Logger
	opts      Options
}

// NewRunner builds a Runner with default Options (auto-screenshots on).
// Use NewRunnerWithOptions to customize.
func NewRunner(registry *ToolRegistry, logger *slog.Logger, reporters ...domain.Reporter) *Runner {
	return NewRunnerWithOptions(registry, logger, Options{AutoScreenshot: true}, reporters...)
}

func NewRunnerWithOptions(registry *ToolRegistry, logger *slog.Logger, opts Options, reporters ...domain.Reporter) *Runner {
	if logger == nil {
		logger = slog.Default()
	}
	return &Runner{registry: registry, reporters: reporters, logger: logger, opts: opts.withDefaults()}
}

// Run executes a single scenario end to end: resolves the tool, connects a
// driver, walks the steps, always closes the driver, and forwards the
// result to every registered Reporter.
func (r *Runner) Run(ctx context.Context, scenario domain.Scenario, toolCfg map[string]interface{}) (domain.ScenarioResult, error) {
	if err := scenario.Validate(); err != nil {
		return domain.ScenarioResult{}, fmt.Errorf("invalid scenario: %w", err)
	}

	connector, err := r.registry.Resolve(scenario.Tool)
	if err != nil {
		return domain.ScenarioResult{}, err
	}

	driver, err := connector.NewDriver(toolCfg)
	if err != nil {
		return domain.ScenarioResult{}, fmt.Errorf("building driver for tool %q: %w", scenario.Tool, err)
	}

	result := domain.ScenarioResult{
		ScenarioName: scenario.Name,
		Tool:         scenario.Tool,
		StartedAt:    time.Now(),
	}

	r.logger.Info("connecting driver", "tool", scenario.Tool, "scenario", scenario.Name)
	if err := driver.Connect(ctx); err != nil {
		result.Status = domain.StatusFailed
		result.Duration = time.Since(result.StartedAt)
		r.report(ctx, result)
		return result, fmt.Errorf("connecting driver: %w", err)
	}
	defer func() {
		closeCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if cerr := driver.Close(closeCtx); cerr != nil {
			r.logger.Warn("driver close failed", "error", cerr)
		}
	}()

	if scenario.BaseURL != "" {
		if err := driver.Navigate(ctx, scenario.BaseURL); err != nil {
			result.Steps = append(result.Steps, failedStep(domain.Action{Type: domain.ActionNavigate, Value: scenario.BaseURL}, err, time.Now()))
		}
	}

	overall := domain.StatusPassed
	for i, step := range scenario.Steps {
		stepResult := r.runStep(ctx, driver, scenario.Name, i, step)
		result.Steps = append(result.Steps, stepResult)
		if stepResult.Status == domain.StatusFailed {
			overall = domain.StatusFailed
			r.logger.Error("step failed", "type", step.Type, "selector", step.Selector, "error", stepResult.Error)
			break // fail fast: later steps usually depend on this one having succeeded
		}
	}
	result.Status = overall
	result.Duration = time.Since(result.StartedAt)

	r.report(ctx, result)
	return result, nil
}

func (r *Runner) report(ctx context.Context, result domain.ScenarioResult) {
	for _, rep := range r.reporters {
		if err := rep.Report(ctx, result); err != nil {
			r.logger.Warn("reporter failed", "error", err)
		}
	}
}

// runStep dispatches a single Action to the appropriate UIDriver method,
// logs what happened (values masked when Action.Sensitive is set), captures
// a screenshot for the step's record, and wraps it all as a StepResult.
// This is the one place that knows the mapping from domain.ActionType to
// driver calls.
func (r *Runner) runStep(ctx context.Context, driver domain.UIDriver, scenarioName string, index int, step domain.Action) domain.StepResult {
	started := time.Now()
	var (
		output string
		err    error
	)

	switch step.Type {
	case domain.ActionNavigate:
		err = driver.Navigate(ctx, step.Value)
	case domain.ActionClick:
		err = driver.Click(ctx, step.Selector)
	case domain.ActionInput:
		err = driver.Input(ctx, step.Selector, step.Value)
	case domain.ActionWaitFor:
		err = driver.WaitFor(ctx, step.Selector, step.EffectiveTimeout(defaultStepTimeout))
	case domain.ActionAssertExist:
		err = driver.WaitFor(ctx, step.Selector, step.EffectiveTimeout(defaultStepTimeout))
	case domain.ActionAssertText:
		var got string
		got, err = driver.GetText(ctx, step.Selector)
		if err == nil && got != step.Value {
			err = fmt.Errorf("assert_text: selector %q: expected %q, got %q", step.Selector, step.Value, got)
		}
		output = got
	case domain.ActionScreenshot:
		var data []byte
		data, err = driver.Screenshot(ctx)
		if err == nil {
			path := step.Value
			if path == "" {
				path = r.screenshotPath(scenarioName, index, "screenshot")
			}
			if werr := writeFile(path, data); werr != nil {
				err = werr
			} else {
				output = path
			}
		}
	case domain.ActionSleep:
		timer := time.NewTimer(step.EffectiveTimeout(time.Second))
		select {
		case <-timer.C:
		case <-ctx.Done():
			timer.Stop()
			err = ctx.Err()
		}
	default:
		err = fmt.Errorf("unknown action type %q", step.Type)
	}

	status := domain.StatusPassed
	errMsg := ""
	if err != nil {
		status = domain.StatusFailed
		errMsg = err.Error()
	}

	logLine := buildLogLine(step, output, status, errMsg)
	r.logger.Info("step", "scenario", scenarioName, "index", index, "type", step.Type, "selector", step.Selector, "value", step.DisplayValue(), "status", status)

	var attachments []domain.Attachment
	// Auto-capture a screenshot for this step's record, in addition to
	// whatever the step itself did. Skip when the step already produced one
	// (explicit `screenshot` action) to avoid a redundant duplicate, and
	// don't let a driver that can't screenshot (e.g. the default desktop
	// stub) fail an otherwise-successful step.
	if r.opts.AutoScreenshot && step.Type != domain.ActionScreenshot {
		if path, serr := r.captureAuto(ctx, driver, scenarioName, index, step.Type); serr == nil {
			attachments = append(attachments, domain.Attachment{
				Name:     "screenshot",
				Path:     path,
				MimeType: "image/png",
			})
		} else if !isUnsupported(serr) {
			r.logger.Warn("auto-screenshot failed", "scenario", scenarioName, "index", index, "error", serr)
		}
	} else if step.Type == domain.ActionScreenshot && output != "" {
		attachments = append(attachments, domain.Attachment{Name: "screenshot", Path: output, MimeType: "image/png"})
	}

	return domain.StepResult{
		Action:      step,
		Status:      status,
		Error:       errMsg,
		Output:      output,
		Log:         logLine,
		Attachments: attachments,
		StartedAt:   started,
		Duration:    time.Since(started),
	}
}

func (r *Runner) captureAuto(ctx context.Context, driver domain.UIDriver, scenarioName string, index int, actionType domain.ActionType) (string, error) {
	data, err := driver.Screenshot(ctx)
	if err != nil {
		return "", err
	}
	path := r.screenshotPath(scenarioName, index, string(actionType))
	if err := writeFile(path, data); err != nil {
		return "", err
	}
	return path, nil
}

func (r *Runner) screenshotPath(scenarioName string, index int, actionType string) string {
	name := fmt.Sprintf("%s-%02d-%s-%d.png", sanitizeFilename(scenarioName), index, actionType, time.Now().UnixNano())
	return filepath.Join(r.opts.ScreenshotDir, name)
}

// buildLogLine renders a single human-readable line describing a step,
// with sensitive values masked, suitable for console output, the JSON
// report's Log field, and (via the Allure reporter) a step's own log
// attachment.
func buildLogLine(step domain.Action, output string, status domain.Status, errMsg string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "[%s]", step.Type)
	if step.Selector != "" {
		fmt.Fprintf(&b, " selector=%q", step.Selector)
	}
	if step.Value != "" {
		fmt.Fprintf(&b, " value=%q", step.DisplayValue())
	}
	if output != "" {
		fmt.Fprintf(&b, " output=%q", output)
	}
	fmt.Fprintf(&b, " status=%s", status)
	if errMsg != "" {
		fmt.Fprintf(&b, " error=%q", errMsg)
	}
	return b.String()
}

func sanitizeFilename(s string) string {
	replacer := strings.NewReplacer(" ", "_", "/", "_", "\\", "_", ":", "_")
	return replacer.Replace(strings.ToLower(s))
}

func isUnsupported(err error) bool {
	return err != nil && (err == domain.ErrUnsupported || strings.Contains(err.Error(), domain.ErrUnsupported.Error()))
}

func failedStep(action domain.Action, err error, started time.Time) domain.StepResult {
	return domain.StepResult{
		Action:    action,
		Status:    domain.StatusFailed,
		Error:     err.Error(),
		Log:       buildLogLine(action, "", domain.StatusFailed, err.Error()),
		StartedAt: started,
		Duration:  time.Since(started),
	}
}
