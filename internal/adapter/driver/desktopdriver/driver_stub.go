//go:build !windows

// Package desktopdriver provides local (non-network) desktop UI automation.
//
// On Windows this package ships a pure-Go Win32 driver (driver_windows.go)
// that needs no CGO and no external automation server: it launches the app,
// finds its window, synthesizes real input events, and captures screenshots.
//
// On other platforms this file is the build: a stub that explains itself
// instead of failing to link, since real automation there needs CGO and
// platform libraries (X11/AT-SPI, Accessibility APIs) that CI/sandbox
// environments rarely have preinstalled. For network-attached native apps,
// prefer the webdriverdriver adapter pointed at WinAppDriver /
// Appium-Mac2Driver instead.
package desktopdriver

import (
	"context"
	"fmt"
	"time"

	"uitester/internal/domain"
)

type Driver struct{ cfg Config }

func New(cfg Config) *Driver { return &Driver{cfg: cfg} }

func (d *Driver) Connect(ctx context.Context) error {
	return fmt.Errorf("desktop driver is only implemented on Windows: %w", domain.ErrUnsupported)
}

func (d *Driver) Navigate(ctx context.Context, url string) error { return domain.ErrUnsupported }

func (d *Driver) Click(ctx context.Context, selector string) error {
	return fmt.Errorf("desktop driver is only implemented on Windows: %w", domain.ErrUnsupported)
}

func (d *Driver) Input(ctx context.Context, selector, text string) error {
	return fmt.Errorf("desktop driver is only implemented on Windows: %w", domain.ErrUnsupported)
}

func (d *Driver) WaitFor(ctx context.Context, selector string, timeout time.Duration) error {
	return fmt.Errorf("desktop driver is only implemented on Windows: %w", domain.ErrUnsupported)
}

func (d *Driver) GetText(ctx context.Context, selector string) (string, error) {
	return "", fmt.Errorf("desktop driver is only implemented on Windows: %w", domain.ErrUnsupported)
}

func (d *Driver) Screenshot(ctx context.Context) ([]byte, error) {
	return nil, fmt.Errorf("desktop driver is only implemented on Windows: %w", domain.ErrUnsupported)
}

func (d *Driver) Close(ctx context.Context) error { return nil }

var _ domain.UIDriver = (*Driver)(nil)
