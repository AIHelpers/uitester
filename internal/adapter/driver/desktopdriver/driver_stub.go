//go:build !desktop

// Package desktopdriver provides local (non-network) desktop UI automation,
// e.g. via OS-level input synthesis and window/pixel inspection.
//
// That kind of automation typically needs CGO and platform libraries
// (X11/AT-SPI on Linux, UIAutomation on Windows, Accessibility APIs on
// macOS) which most CI/sandbox environments don't have preinstalled. To
// keep `go build ./...` reliably green out of the box, this file is the
// *default* build: a stub that explains itself instead of failing to link.
//
// For real local desktop automation, build with `-tags desktop` against
// driver_desktop.go, which wires in a library such as go-vgo/robotgo.
// For network-attached native desktop apps (Windows/macOS), prefer the
// webdriverdriver adapter pointed at WinAppDriver / Appium-Mac2Driver
// instead — no CGO required, and it's usually the better default.
package desktopdriver

import (
	"context"
	"fmt"
	"time"

	"uitester/internal/domain"
)

type Config struct{}

type Driver struct{ cfg Config }

func New(cfg Config) *Driver { return &Driver{cfg: cfg} }

func (d *Driver) Connect(ctx context.Context) error { return nil }

func (d *Driver) Navigate(ctx context.Context, url string) error { return domain.ErrUnsupported }

func (d *Driver) Click(ctx context.Context, selector string) error {
	return fmt.Errorf("desktop driver built without -tags desktop: %w", domain.ErrUnsupported)
}

func (d *Driver) Input(ctx context.Context, selector, text string) error {
	return fmt.Errorf("desktop driver built without -tags desktop: %w", domain.ErrUnsupported)
}

func (d *Driver) WaitFor(ctx context.Context, selector string, timeout time.Duration) error {
	return fmt.Errorf("desktop driver built without -tags desktop: %w", domain.ErrUnsupported)
}

func (d *Driver) GetText(ctx context.Context, selector string) (string, error) {
	return "", fmt.Errorf("desktop driver built without -tags desktop: %w", domain.ErrUnsupported)
}

func (d *Driver) Screenshot(ctx context.Context) ([]byte, error) {
	return nil, fmt.Errorf("desktop driver built without -tags desktop: %w", domain.ErrUnsupported)
}

func (d *Driver) Close(ctx context.Context) error { return nil }

var _ domain.UIDriver = (*Driver)(nil)
