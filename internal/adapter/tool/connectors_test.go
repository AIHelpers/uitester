package tool_test

import (
	"errors"
	"strings"
	"testing"

	"uitester/internal/adapter/driver/desktopdriver"
	"uitester/internal/adapter/tool"
	"uitester/internal/domain"
)

// recordingRegistrar satisfies tool's unexported registrar interface
// (structurally) and records every connector registered through it.
type recordingRegistrar struct {
	names []string
}

func (r *recordingRegistrar) Register(c domain.ToolConnector) error {
	r.names = append(r.names, c.Name())
	return nil
}

func TestDesktopConnector_Name(t *testing.T) {
	if got, want := (tool.DesktopConnector{}).Name(), "desktop"; got != want {
		t.Fatalf("expected connector name %q, got %q", want, got)
	}
}

func TestDesktopConnector_NewDriverDefaults(t *testing.T) {
	drv, err := (tool.DesktopConnector{}).NewDriver(nil)
	if err != nil {
		t.Fatalf("NewDriver(nil): %v", err)
	}
	if _, ok := drv.(*desktopdriver.Driver); !ok {
		t.Fatalf("expected *desktopdriver.Driver, got %T", drv)
	}
}

func TestDesktopConnector_NewDriverAcceptsAllConfigKeys(t *testing.T) {
	cfg := map[string]interface{}{
		"app":             `C:\Apps\MyApp.exe`,
		"window_title":    "MyApp Main Window",
		"working_dir":     `C:\Apps`,
		"startup_timeout": "45s",
		"unknown_key":     "ignored",
	}
	drv, err := (tool.DesktopConnector{}).NewDriver(cfg)
	if err != nil {
		t.Fatalf("NewDriver with full config: %v", err)
	}
	if _, ok := drv.(*desktopdriver.Driver); !ok {
		t.Fatalf("expected *desktopdriver.Driver, got %T", drv)
	}
}

func TestDesktopConnector_RejectsInvalidStartupTimeout(t *testing.T) {
	cfg := map[string]interface{}{"startup_timeout": "not-a-duration"}
	_, err := (tool.DesktopConnector{}).NewDriver(cfg)
	if err == nil {
		t.Fatal("expected error for invalid startup_timeout, got nil")
	}
	if !strings.Contains(err.Error(), "startup_timeout") {
		t.Fatalf("expected error to mention startup_timeout, got: %v", err)
	}
}

func TestDesktopConnector_IgnoresNonStringValues(t *testing.T) {
	// Wrongly-typed keys must fall back to defaults, not panic or error.
	cfg := map[string]interface{}{
		"app":             42,
		"window_title":    true,
		"working_dir":     []string{},
		"startup_timeout": 3.14,
	}
	drv, err := (tool.DesktopConnector{}).NewDriver(cfg)
	if err != nil {
		t.Fatalf("expected non-string values to fall back to defaults, got error: %v", err)
	}
	if drv == nil {
		t.Fatal("expected a driver, got nil")
	}
}

func TestRegisterDefaults_RegistersAllBuiltinConnectors(t *testing.T) {
	reg := &recordingRegistrar{}
	if err := tool.RegisterDefaults(reg); err != nil {
		t.Fatalf("RegisterDefaults: %v", err)
	}
	want := []string{"chrome", "selenium", "appium", "winappdriver", "desktop"}
	if len(reg.names) != len(want) {
		t.Fatalf("expected %d registered connectors, got %d: %v", len(want), len(reg.names), reg.names)
	}
	for _, name := range want {
		found := false
		for _, got := range reg.names {
			if got == name {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("expected connector %q to be registered; got %v", name, reg.names)
		}
	}
}

type failingRegistrar struct{}

func (failingRegistrar) Register(domain.ToolConnector) error { return errors.New("boom") }

func TestRegisterDefaults_PropagatesRegistrationError(t *testing.T) {
	if err := tool.RegisterDefaults(failingRegistrar{}); err == nil {
		t.Fatal("expected RegisterDefaults to propagate registration error, got nil")
	}
}
