// Package tool contains domain.ToolConnector implementations: the glue that
// turns free-form config (parsed from YAML) into a concrete domain.UIDriver.
// This is the intended place to add support for a new automation backend —
// implement one small struct here, register it once in main.go.
package tool

import (
	"fmt"

	"uitester/internal/adapter/driver/chromedpdriver"
	"uitester/internal/adapter/driver/desktopdriver"
	"uitester/internal/adapter/driver/webdriverdriver"
	"uitester/internal/domain"
)

// ChromeConnector launches/attaches Chrome directly via chromedp.
// Recognized cfg keys: headless (bool), remote_url (string), no_sandbox (bool).
type ChromeConnector struct{}

func (ChromeConnector) Name() string { return "chrome" }

func (ChromeConnector) NewDriver(cfg map[string]interface{}) (domain.UIDriver, error) {
	c := chromedpdriver.Config{
		Headless:  boolOr(cfg, "headless", true),
		RemoteURL: stringOr(cfg, "remote_url", ""),
		NoSandbox: boolOr(cfg, "no_sandbox", false),
	}
	return chromedpdriver.New(c), nil
}

// WebDriverConnector talks the standard WebDriver protocol. Registered under
// several names (selenium/appium/winappdriver) so scenario authors can pick
// the name that documents intent, even though they all share one adapter.
// Recognized cfg keys: remote_url (string, required), capabilities (map).
type WebDriverConnector struct {
	ConnectorName string
}

func (c WebDriverConnector) Name() string { return c.ConnectorName }

func (c WebDriverConnector) NewDriver(cfg map[string]interface{}) (domain.UIDriver, error) {
	remoteURL := stringOr(cfg, "remote_url", "")
	if remoteURL == "" {
		return nil, fmt.Errorf("tool %q: remote_url is required", c.ConnectorName)
	}
	caps, _ := cfg["capabilities"].(map[string]interface{})
	return webdriverdriver.New(webdriverdriver.Config{
		RemoteURL:    remoteURL,
		Capabilities: caps,
	}), nil
}

// DesktopConnector drives the local desktop directly (no remote server).
// See internal/adapter/driver/desktopdriver for the -tags desktop opt-in.
type DesktopConnector struct{}

func (DesktopConnector) Name() string { return "desktop" }

func (DesktopConnector) NewDriver(cfg map[string]interface{}) (domain.UIDriver, error) {
	return desktopdriver.New(desktopdriver.Config{}), nil
}

// RegisterDefaults registers every built-in connector. Call it once from
// the composition root (main.go). Additional/custom connectors can be
// registered alongside it without touching this file.
func RegisterDefaults(registry registrar) error {
	connectors := []domain.ToolConnector{
		ChromeConnector{},
		WebDriverConnector{ConnectorName: "selenium"},
		WebDriverConnector{ConnectorName: "appium"},
		WebDriverConnector{ConnectorName: "winappdriver"},
		DesktopConnector{},
	}
	for _, c := range connectors {
		if err := registry.Register(c); err != nil {
			return err
		}
	}
	return nil
}

// registrar is the minimal surface RegisterDefaults needs from
// usecase.ToolRegistry, kept as a local interface so this package doesn't
// import usecase (adapters depend inward on domain, not sideways on usecase).
type registrar interface {
	Register(c domain.ToolConnector) error
}

func stringOr(m map[string]interface{}, key, def string) string {
	if v, ok := m[key].(string); ok {
		return v
	}
	return def
}

func boolOr(m map[string]interface{}, key string, def bool) bool {
	if v, ok := m[key].(bool); ok {
		return v
	}
	return def
}
