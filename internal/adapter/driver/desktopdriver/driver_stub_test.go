//go:build !windows

// Unit tests for the non-Windows desktop driver stub. The stub's contract:
// it builds cleanly on every platform, but every operation reports
// domain.ErrUnsupported so scenarios fail fast with a clear reason instead of
// mysteriously doing nothing.
package desktopdriver

import (
	"context"
	"errors"
	"testing"
	"time"

	"uitester/internal/domain"
)

func TestStub_AllOperationsUnsupported(t *testing.T) {
	d := New(Config{App: "some-app", WindowTitle: "some window"})
	ctx := context.Background()

	if err := d.Connect(ctx); !errors.Is(err, domain.ErrUnsupported) {
		t.Errorf("Connect: expected ErrUnsupported, got %v", err)
	}
	if err := d.Navigate(ctx, "https://example.com"); !errors.Is(err, domain.ErrUnsupported) {
		t.Errorf("Navigate: expected ErrUnsupported, got %v", err)
	}
	if err := d.Click(ctx, "100,100"); !errors.Is(err, domain.ErrUnsupported) {
		t.Errorf("Click: expected ErrUnsupported, got %v", err)
	}
	if err := d.Input(ctx, "100,100", "text"); !errors.Is(err, domain.ErrUnsupported) {
		t.Errorf("Input: expected ErrUnsupported, got %v", err)
	}
	if err := d.WaitFor(ctx, "title", time.Second); !errors.Is(err, domain.ErrUnsupported) {
		t.Errorf("WaitFor: expected ErrUnsupported, got %v", err)
	}
	if _, err := d.GetText(ctx, "title"); !errors.Is(err, domain.ErrUnsupported) {
		t.Errorf("GetText: expected ErrUnsupported, got %v", err)
	}
	if _, err := d.Screenshot(ctx); !errors.Is(err, domain.ErrUnsupported) {
		t.Errorf("Screenshot: expected ErrUnsupported, got %v", err)
	}
}

func TestStub_ImplementsUIDriver(t *testing.T) {
	var _ domain.UIDriver = New(Config{})
}

func TestStub_CloseIsNoop(t *testing.T) {
	d := New(Config{})
	if err := d.Close(context.Background()); err != nil {
		t.Fatalf("Close on the stub: %v", err)
	}
}
