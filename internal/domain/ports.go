package domain

import (
	"context"
	"time"
)

// UIDriver is the single abstraction the usecase layer talks to for driving
// a UI, whether that UI is a browser tab, a native desktop window, or a
// mobile app screen. Every automation backend (chromedp, Selenium/WebDriver,
// a future Appium/WinAppDriver/robotgo backend) implements this interface,
// which is what lets Scenarios stay 100% backend-agnostic.
type UIDriver interface {
	// Connect establishes the session (launches/attaches a browser, opens a
	// remote WebDriver session, attaches to a desktop process, ...).
	Connect(ctx context.Context) error

	// Navigate opens a URL. Drivers that don't support navigation (most
	// native desktop drivers) should return ErrUnsupported.
	Navigate(ctx context.Context, url string) error

	Click(ctx context.Context, selector string) error
	Input(ctx context.Context, selector, text string) error

	// WaitFor blocks until selector is present/visible or timeout elapses.
	WaitFor(ctx context.Context, selector string, timeout time.Duration) error

	// GetText returns the visible text of the element located by selector.
	GetText(ctx context.Context, selector string) (string, error)

	// Screenshot captures the current viewport/window as PNG bytes.
	Screenshot(ctx context.Context) ([]byte, error)

	// Close tears down the session and releases any resources.
	Close(ctx context.Context) error
}

// ToolConnector is a named factory for UIDriver instances. Registering a new
// ToolConnector with the ToolRegistry is the intended extension point:
// "connect a required tool" means writing ~30 lines that satisfy this
// interface and registering it in cmd/uitester/main.go (or via config).
type ToolConnector interface {
	// Name is the key scenarios reference via `tool:` in YAML.
	Name() string

	// NewDriver builds a fresh UIDriver from free-form config (parsed from
	// the tools: section of the app config). Each connector defines and
	// documents the keys it reads.
	NewDriver(cfg map[string]interface{}) (UIDriver, error)
}

// Reporter receives a finished ScenarioResult. Multiple reporters can be
// attached to a single run (e.g. console + JSON file + CI artifact upload).
type Reporter interface {
	Report(ctx context.Context, result ScenarioResult) error
}
