// Package chromedpdriver implements domain.UIDriver on top of chromedp,
// driving Chrome/Chromium directly via the DevTools protocol. It needs no
// external server (unlike Selenium/Appium) — either it launches a local
// browser binary, or it attaches to a remote one over a WebSocket
// DevTools URL, which makes it a good default for CI browser testing.
package chromedpdriver

import (
	"context"
	"fmt"
	"time"

	"github.com/chromedp/chromedp"

	"uitester/internal/domain"
)

// Config are the keys this driver reads from a tool's config map (see
// configs/config.example.yaml, tools.chrome).
type Config struct {
	Headless   bool   // run without a visible window
	RemoteURL  string // optional: ws:// DevTools endpoint of an already-running Chrome
	NoSandbox  bool   // useful inside containers without user namespaces
	WindowSize string // "WIDTHxHEIGHT", e.g. "1280x800"
}

// Driver adapts chromedp's context-based API to the request/response style
// domain.UIDriver expects.
type Driver struct {
	cfg Config

	allocCtx    context.Context
	allocCancel context.CancelFunc
	ctx         context.Context
	cancel      context.CancelFunc
}

func New(cfg Config) *Driver {
	return &Driver{cfg: cfg}
}

func (d *Driver) Connect(ctx context.Context) error {
	if d.cfg.RemoteURL != "" {
		d.allocCtx, d.allocCancel = chromedp.NewRemoteAllocator(ctx, d.cfg.RemoteURL)
	} else {
		opts := append([]chromedp.ExecAllocatorOption{}, chromedp.DefaultExecAllocatorOptions[:]...)
		opts = append(opts, chromedp.Flag("headless", d.cfg.Headless))
		if d.cfg.NoSandbox {
			opts = append(opts, chromedp.Flag("no-sandbox", true))
		}
		d.allocCtx, d.allocCancel = chromedp.NewExecAllocator(ctx, opts...)
	}

	d.ctx, d.cancel = chromedp.NewContext(d.allocCtx)

	// Actually starting the browser process happens lazily on first Run, so
	// force it now with a no-op action; this surfaces launch failures at
	// Connect() time rather than on the first real step.
	if err := chromedp.Run(d.ctx); err != nil {
		return fmt.Errorf("starting browser: %w", err)
	}
	return nil
}

func (d *Driver) Navigate(ctx context.Context, url string) error {
	return chromedp.Run(d.ctx, chromedp.Navigate(url))
}

func (d *Driver) Click(ctx context.Context, selector string) error {
	return chromedp.Run(d.ctx, chromedp.Click(selector, chromedp.ByQuery))
}

func (d *Driver) Input(ctx context.Context, selector, text string) error {
	return chromedp.Run(d.ctx,
		chromedp.WaitVisible(selector, chromedp.ByQuery),
		chromedp.SendKeys(selector, text, chromedp.ByQuery),
	)
}

func (d *Driver) WaitFor(ctx context.Context, selector string, timeout time.Duration) error {
	waitCtx, cancel := context.WithTimeout(d.ctx, timeout)
	defer cancel()
	return chromedp.Run(waitCtx, chromedp.WaitVisible(selector, chromedp.ByQuery))
}

func (d *Driver) GetText(ctx context.Context, selector string) (string, error) {
	var text string
	if err := chromedp.Run(d.ctx, chromedp.Text(selector, &text, chromedp.ByQuery)); err != nil {
		return "", err
	}
	return text, nil
}

func (d *Driver) Screenshot(ctx context.Context) ([]byte, error) {
	var buf []byte
	if err := chromedp.Run(d.ctx, chromedp.FullScreenshot(&buf, 90)); err != nil {
		return nil, err
	}
	return buf, nil
}

func (d *Driver) Close(ctx context.Context) error {
	if d.cancel != nil {
		d.cancel()
	}
	if d.allocCancel != nil {
		d.allocCancel()
	}
	return nil
}

var _ domain.UIDriver = (*Driver)(nil)
