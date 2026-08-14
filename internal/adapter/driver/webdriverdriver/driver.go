// Package webdriverdriver implements domain.UIDriver against the standard
// W3C WebDriver wire protocol using only the Go standard library's
// net/http and encoding/json. Deliberately dependency-free: this is the
// "connect any required tool" adapter, and every W3C-compliant automation
// server speaks the same handful of HTTP endpoints implemented below —
// Selenium Grid (browsers), Appium (Android/iOS apps), WinAppDriver
// (native Windows desktop apps) and Appium-Mac2Driver (native macOS
// desktop apps). Only RemoteURL and Capabilities change between them.
package webdriverdriver

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"uitester/internal/domain"
)

// Config points the driver at whichever WebDriver-compatible server the
// job needs.
//
// Examples:
//   - Selenium Grid (browser):     RemoteURL "http://grid:4444/wd/hub", Capabilities{"browserName":"chrome"}
//   - Appium (mobile app):         RemoteURL "http://localhost:4723/wd/hub", Capabilities{"platformName":"Android","appPackage":"..."}
//   - WinAppDriver (Windows app):  RemoteURL "http://127.0.0.1:4723", Capabilities{"app":"C:\\Path\\App.exe"}
type Config struct {
	RemoteURL    string
	Capabilities map[string]interface{}
	HTTPClient   *http.Client // optional; a sane default is used if nil
}

type Driver struct {
	cfg       Config
	client    *http.Client
	sessionID string
}

func New(cfg Config) *Driver {
	client := cfg.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	return &Driver{cfg: cfg, client: client}
}

func (d *Driver) Connect(ctx context.Context) error {
	if d.cfg.RemoteURL == "" {
		return fmt.Errorf("webdriver: remote_url is required")
	}
	body := map[string]interface{}{
		"capabilities": map[string]interface{}{
			"alwaysMatch": d.cfg.Capabilities,
		},
	}
	var resp struct {
		Value struct {
			SessionID string `json:"sessionId"`
		} `json:"value"`
	}
	if err := d.do(ctx, http.MethodPost, "/session", body, &resp); err != nil {
		return fmt.Errorf("opening webdriver session at %s: %w", d.cfg.RemoteURL, err)
	}
	if resp.Value.SessionID == "" {
		return fmt.Errorf("webdriver session at %s returned no sessionId", d.cfg.RemoteURL)
	}
	d.sessionID = resp.Value.SessionID
	return nil
}

func (d *Driver) Navigate(ctx context.Context, url string) error {
	err := d.do(ctx, http.MethodPost, d.sessionPath("/url"), map[string]string{"url": url}, nil)
	if err != nil {
		return fmt.Errorf("navigate: %w (native-desktop WebDriver servers usually don't support this: %v)", err, domain.ErrUnsupported)
	}
	return nil
}

func (d *Driver) Click(ctx context.Context, selector string) error {
	el, err := d.findElement(ctx, selector)
	if err != nil {
		return err
	}
	return d.do(ctx, http.MethodPost, d.sessionPath("/element/"+el+"/click"), map[string]interface{}{}, nil)
}

func (d *Driver) Input(ctx context.Context, selector, text string) error {
	el, err := d.findElement(ctx, selector)
	if err != nil {
		return err
	}
	// best-effort clear; not all backends support it, so ignore failures
	_ = d.do(ctx, http.MethodPost, d.sessionPath("/element/"+el+"/clear"), map[string]interface{}{}, nil)
	return d.do(ctx, http.MethodPost, d.sessionPath("/element/"+el+"/value"), map[string]interface{}{"text": text}, nil)
}

func (d *Driver) WaitFor(ctx context.Context, selector string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	var lastErr error
	for time.Now().Before(deadline) {
		if _, err := d.findElement(ctx, selector); err == nil {
			return nil
		} else {
			lastErr = err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(200 * time.Millisecond):
		}
	}
	return fmt.Errorf("timed out after %s waiting for %q: %w", timeout, selector, lastErr)
}

func (d *Driver) GetText(ctx context.Context, selector string) (string, error) {
	el, err := d.findElement(ctx, selector)
	if err != nil {
		return "", err
	}
	var resp struct {
		Value string `json:"value"`
	}
	if err := d.do(ctx, http.MethodGet, d.sessionPath("/element/"+el+"/text"), nil, &resp); err != nil {
		return "", err
	}
	return resp.Value, nil
}

func (d *Driver) Screenshot(ctx context.Context) ([]byte, error) {
	var resp struct {
		Value string `json:"value"` // base64 PNG
	}
	if err := d.do(ctx, http.MethodGet, d.sessionPath("/screenshot"), nil, &resp); err != nil {
		return nil, err
	}
	return base64.StdEncoding.DecodeString(resp.Value)
}

func (d *Driver) Close(ctx context.Context) error {
	if d.sessionID == "" {
		return nil
	}
	return d.do(ctx, http.MethodDelete, d.sessionPath(""), nil, nil)
}

// findElement resolves a CSS selector to a W3C element reference. Native
// app WebDriver servers (WinAppDriver, Appium) additionally support
// "accessibility id" / "id" strategies via their own selector prefixes;
// this stays CSS-only to keep the adapter simple. Prefix a selector with
// "id=" or "name=" and extend the `using` mapping below if a target app
// needs those instead.
func (d *Driver) findElement(ctx context.Context, selector string) (string, error) {
	using, value := "css selector", selector
	if rest, ok := strings.CutPrefix(selector, "id="); ok {
		using, value = "id", rest
	} else if rest, ok := strings.CutPrefix(selector, "name="); ok {
		using, value = "accessibility id", rest
	}

	var resp struct {
		Value map[string]string `json:"value"`
	}
	if err := d.do(ctx, http.MethodPost, d.sessionPath("/element"), map[string]string{"using": using, "value": value}, &resp); err != nil {
		return "", fmt.Errorf("find element %q: %w", selector, err)
	}
	for _, id := range resp.Value { // W3C returns a single key like "element-6066-..."
		return id, nil
	}
	return "", fmt.Errorf("find element %q: no element id in response", selector)
}

func (d *Driver) sessionPath(suffix string) string {
	return "/session/" + d.sessionID + suffix
}

// do performs one WebDriver HTTP call and decodes the JSON response into
// out (if non-nil). It centralizes URL joining, JSON encoding/decoding and
// error-body surfacing so every method above stays a one-liner.
func (d *Driver) do(ctx context.Context, method, path string, payload interface{}, out interface{}) error {
	var reqBody io.Reader
	if payload != nil {
		b, err := json.Marshal(payload)
		if err != nil {
			return fmt.Errorf("encoding request: %w", err)
		}
		reqBody = bytes.NewReader(b)
	}

	url := strings.TrimRight(d.cfg.RemoteURL, "/") + path
	req, err := http.NewRequestWithContext(ctx, method, url, reqBody)
	if err != nil {
		return fmt.Errorf("building request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := d.client.Do(req)
	if err != nil {
		return fmt.Errorf("%s %s: %w", method, url, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("reading response: %w", err)
	}

	if resp.StatusCode >= 400 {
		return fmt.Errorf("%s %s: status %d: %s", method, url, resp.StatusCode, string(body))
	}
	if out != nil && len(body) > 0 {
		if err := json.Unmarshal(body, out); err != nil {
			return fmt.Errorf("decoding response from %s %s: %w", method, url, err)
		}
	}
	return nil
}

var _ domain.UIDriver = (*Driver)(nil)
