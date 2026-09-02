//go:build windows

// Behavioral verification that DesktopConnector forwards each recognized
// config key into the desktop driver. The driver keeps its configuration
// private, so the mapping is asserted through Connect: every value below is
// chosen to be unable to succeed (a missing app, a nonexistent window), and
// the resulting error message must echo the configured value — proving the
// key actually reached the driver.
package tool_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"uitester/internal/adapter/tool"
)

func TestDesktopConnector_ForwardsWindowTitleAndStartupTimeout(t *testing.T) {
	title := "uitester-connector-test-no-such-window"
	drv, err := (tool.DesktopConnector{}).NewDriver(map[string]interface{}{
		"window_title":    title,
		"startup_timeout": "600ms",
	})
	if err != nil {
		t.Fatalf("NewDriver: %v", err)
	}

	start := time.Now()
	connErr := drv.Connect(context.Background())
	if connErr == nil {
		t.Fatal("expected Connect to time out waiting for a nonexistent window, got nil")
	}
	if el := time.Since(start); el > 10*time.Second {
		t.Fatalf("Connect waited far longer than the configured 600ms: %v", el)
	}
	if !strings.Contains(connErr.Error(), "600ms") {
		t.Fatalf("expected Connect error to echo the configured 600ms timeout, got: %v", connErr)
	}
	if !strings.Contains(connErr.Error(), title) {
		t.Fatalf("expected Connect error to echo the configured window title, got: %v", connErr)
	}
	if cerr := drv.Close(context.Background()); cerr != nil {
		t.Fatalf("Close: %v", cerr)
	}
}

func TestDesktopConnector_ForwardsApp(t *testing.T) {
	missing := t.TempDir() + `\definitely-missing-app.exe`
	drv, err := (tool.DesktopConnector{}).NewDriver(map[string]interface{}{"app": missing})
	if err != nil {
		t.Fatalf("NewDriver: %v", err)
	}

	connErr := drv.Connect(context.Background())
	if connErr == nil {
		t.Fatal("expected Connect to fail for a missing app, got nil")
	}
	if !strings.Contains(connErr.Error(), missing) {
		t.Fatalf("expected Connect error to name the missing app %q, got: %v", missing, connErr)
	}
	if !strings.Contains(connErr.Error(), "not found") {
		t.Fatalf("expected Connect error to be a 'not found' error, got: %v", connErr)
	}
}
