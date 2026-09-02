//go:build windows

// Win32 implementation of the desktop driver. Everything here is plain
// syscall (LazyDLL/LazyProc) — no CGO, no UIAutomation bridge, no
// WinAppDriver — so uitester builds and runs on any Windows box with just
// the Go toolchain.
//
// Selector semantics (documented per-method below):
//
//	click / input   "x,y"      absolute screen pixels
//	               "win:fx,fy" window-relative fractions (0..1)
//	               "center"    center of the app window
//	wait_for       "<title>"   any visible top-level window whose title
//	                           contains the string
//	get_text       "title"     the main window's current title
package desktopdriver

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"uitester/internal/domain"
)

// ---------------------------------------------------------------------------
// Win32 bindings
// ---------------------------------------------------------------------------

var (
	user32 = syscall.NewLazyDLL("user32.dll")
	gdi32  = syscall.NewLazyDLL("gdi32.dll")

	procSetProcessDPIAware       = user32.NewProc("SetProcessDPIAware")
	procEnumWindows              = user32.NewProc("EnumWindows")
	procGetWindowTextW           = user32.NewProc("GetWindowTextW")
	procGetWindowTextLengthW     = user32.NewProc("GetWindowTextLengthW")
	procIsWindowVisible          = user32.NewProc("IsWindowVisible")
	procGetWindowThreadProcessId = user32.NewProc("GetWindowThreadProcessId")
	procGetWindowRect            = user32.NewProc("GetWindowRect")
	procSetForegroundWindow      = user32.NewProc("SetForegroundWindow")
	procShowWindow               = user32.NewProc("ShowWindow")
	procSetCursorPos             = user32.NewProc("SetCursorPos")
	procMouseEvent               = user32.NewProc("mouse_event")
	procSendInput                = user32.NewProc("SendInput")
	procPrintWindow              = user32.NewProc("PrintWindow")
	procPostMessageW             = user32.NewProc("PostMessageW")
	procGetDC                    = user32.NewProc("GetDC")
	procReleaseDC                = user32.NewProc("ReleaseDC")

	procCreateCompatibleDC     = gdi32.NewProc("CreateCompatibleDC")
	procCreateCompatibleBitmap = gdi32.NewProc("CreateCompatibleBitmap")
	procSelectObject           = gdi32.NewProc("SelectObject")
	procDeleteObject           = gdi32.NewProc("DeleteObject")
	procDeleteDC               = gdi32.NewProc("DeleteDC")
	procGetDIBits              = gdi32.NewProc("GetDIBits")
	procCreateDIBSection       = gdi32.NewProc("CreateDIBSection")
)

const (
	inputKeyboard    = 1
	keyeventfKeyup   = 0x0002
	keyeventfUnicode = 0x0004

	mouseeventfLeftdown = 0x0002
	mouseeventfLeftup   = 0x0004

	// pwRenderFullContent is the (undocumented but stable) PrintWindow
	// flag that captures DirectX/DirectComposition-rendered content —
	// which is how Avalonia, Chromium and other GPU-composited apps draw.
	pwRenderFullContent = 2

	wmClose   = 0x0010
	swRestore = 9
)

type rect struct{ Left, Top, Right, Bottom int32 }

// keyboardInput mirrors Win32 KEYBDINPUT; the [8]byte pad below brings
// tagINPUT to the same 40-byte size as the C INPUT union (whose largest
// member, MOUSEINPUT, occupies 32 bytes after the 4-byte type + 4 pad).
type keyboardInput struct {
	WVk         uint16
	WScan       uint16
	DwFlags     uint32
	Time        uint32
	DwExtraInfo uintptr
}

type tagINPUT struct {
	Type uint32
	Ki   keyboardInput
	Pad  [8]byte
}

type bitmapInfoHeader struct {
	BiSize          uint32
	BiWidth         int32
	BiHeight        int32
	BiPlanes        uint16
	BiBitCount      uint16
	BiCompression   uint32
	BiSizeImage     uint32
	BiXPelsPerMeter int32
	BiYPelsPerMeter int32
	BiClrUsed       uint32
	BiClrImportant  uint32
}

type bitmapInfo struct {
	BmiHeader bitmapInfoHeader
	BmiColors [1]uint32 // color-table room; unused for 32bpp
}

// ---------------------------------------------------------------------------
// Window enumeration
// ---------------------------------------------------------------------------

// A single EnumWindows callback is created once per process (Windows caps
// NewCallback registrations); the active match predicate and its result are
// funneled through package-level state guarded by enumMu.
var (
	enumMu     sync.Mutex
	enumMatch  func(hwnd uintptr) bool
	enumResult uintptr
)

var enumCallback = syscall.NewCallback(func(hwnd uintptr, _ uintptr) uintptr {
	enumMu.Lock()
	defer enumMu.Unlock()
	if enumMatch != nil && enumMatch(hwnd) {
		enumResult = hwnd
		return 0 // stop enumeration
	}
	return 1 // continue
})

// enumTopWindows hands each visible top-level window to match and returns
// the first hwnd for which match returns true (0 if none).
func enumTopWindows(match func(hwnd uintptr) bool) uintptr {
	enumMu.Lock()
	enumMatch = match
	enumResult = 0
	enumMu.Unlock()
	procEnumWindows.Call(enumCallback, 0)
	enumMu.Lock()
	hwnd := enumResult
	enumMatch = nil
	enumMu.Unlock()
	return hwnd
}

func windowText(hwnd uintptr) string {
	n, _, _ := procGetWindowTextLengthW.Call(hwnd)
	if n == 0 {
		return ""
	}
	buf := make([]uint16, n+1)
	procGetWindowTextW.Call(hwnd, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
	return syscall.UTF16ToString(buf)
}

func windowPid(hwnd uintptr) uint32 {
	var pid uint32
	procGetWindowThreadProcessId.Call(hwnd, uintptr(unsafe.Pointer(&pid)))
	return pid
}

// findWindow returns the first visible top-level window whose title contains
// titleSubstring (if non-empty) and belongs to pid (if non-zero).
func findWindow(titleSubstring string, pid uint32) uintptr {
	return enumTopWindows(func(hwnd uintptr) bool {
		if visible, _, _ := procIsWindowVisible.Call(hwnd); visible == 0 {
			return false
		}
		if titleSubstring != "" {
			if !strings.Contains(windowText(hwnd), titleSubstring) {
				return false
			}
		} else if windowText(hwnd) == "" {
			return false
		}
		if pid != 0 && windowPid(hwnd) != pid {
			return false
		}
		return true
	})
}

func windowRect(hwnd uintptr) (rect, error) {
	var r rect
	if ok, _, _ := procGetWindowRect.Call(hwnd, uintptr(unsafe.Pointer(&r))); ok == 0 {
		return r, fmt.Errorf("GetWindowRect failed")
	}
	return r, nil
}

func bringToFront(hwnd uintptr) {
	// Restore if minimized, then try to foreground. We launched the app, so
	// the foreground-lock rules allow us to take focus; even when they
	// don't, a later synthesized click re-activates the window.
	procShowWindow.Call(hwnd, swRestore)
	procSetForegroundWindow.Call(hwnd)
}

// ---------------------------------------------------------------------------
// Driver
// ---------------------------------------------------------------------------

// Driver drives a local Windows desktop app through the Win32 API: it
// launches (or attaches to) the app, finds its top-level window, and
// synthesizes real mouse and keyboard input.
type Driver struct {
	cfg Config

	mu     sync.Mutex
	cmd    *exec.Cmd
	exited chan struct{} // closed once the launched process terminated
	hwnd   uintptr
}

// New builds a desktop driver from connector-level config.
func New(cfg Config) *Driver { return &Driver{cfg: cfg} }

var _ domain.UIDriver = (*Driver)(nil)

func (d *Driver) pid() uint32 {
	if d.cmd != nil && d.cmd.Process != nil {
		return uint32(d.cmd.Process.Pid)
	}
	return 0
}

func (d *Driver) mainWindow() (uintptr, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.hwnd == 0 {
		return 0, fmt.Errorf("desktop driver: no window attached")
	}
	return d.hwnd, nil
}

// Connect launches the configured app (if any) and waits for its main
// window to appear. When Config.App is empty it attaches to an existing
// window matching Config.WindowTitle instead.
func (d *Driver) Connect(ctx context.Context) error {
	// Physical pixels, please: no coordinate virtualization on high-DPI
	// screens. Best-effort; fails harmlessly if already set.
	procSetProcessDPIAware.Call()

	if d.cfg.App != "" {
		if err := d.launch(ctx); err != nil {
			return err
		}
	}
	if d.cfg.WindowTitle == "" && d.cfg.App == "" {
		return fmt.Errorf("desktop driver: window_title is required when the driver does not launch the app itself")
	}

	timeout := d.cfg.StartupTimeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	deadline := time.Now().Add(timeout)
	for {
		if hwnd := findWindow(d.cfg.WindowTitle, d.pid()); hwnd != 0 {
			d.mu.Lock()
			d.hwnd = hwnd
			d.mu.Unlock()
			// Let the first layout/render pass settle, then put the
			// window up front so synthesized input reaches it.
			time.Sleep(400 * time.Millisecond)
			bringToFront(hwnd)
			return nil
		}
		if d.exited != nil {
			select {
			case <-d.exited:
				return fmt.Errorf("desktop app %q exited before showing a window", d.cfg.App)
			default:
			}
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("timed out after %v waiting for a window titled %q", timeout, d.cfg.WindowTitle)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(250 * time.Millisecond):
		}
	}
}

func (d *Driver) launch(ctx context.Context) error {
	app := d.cfg.App
	if _, err := os.Stat(app); err != nil {
		// Maybe a bare command on PATH, or contains %ENV% references.
		if resolved, rerr := exec.LookPath(os.ExpandEnv(app)); rerr == nil {
			app = resolved
		} else {
			return fmt.Errorf("desktop app %q not found: %w", app, err)
		}
	}
	dir := d.cfg.WorkingDir
	if dir == "" {
		dir = filepath.Dir(app) // apps resolve assets/logs relative to their exe
	}
	cmd := exec.Command(app)
	cmd.Dir = dir
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("starting desktop app %q: %w", app, err)
	}
	d.cmd = cmd
	d.exited = make(chan struct{})
	go func() {
		defer close(d.exited)
		_ = cmd.Wait()
	}()
	return nil
}

// Navigate is meaningless for a native desktop app.
func (d *Driver) Navigate(ctx context.Context, url string) error {
	return domain.ErrUnsupported
}

// Click moves the cursor and presses the left mouse button. Selector:
// "x,y" (absolute screen pixels), "win:fx,fy" (fractions of the app
// window's current rectangle — robust against position/size/DPI), or
// "center".
func (d *Driver) Click(ctx context.Context, selector string) error {
	hwnd, err := d.mainWindow()
	if err != nil {
		return err
	}
	x, y, err := resolvePoint(hwnd, selector)
	if err != nil {
		return err
	}
	procSetCursorPos.Call(uintptr(x), uintptr(y))
	time.Sleep(80 * time.Millisecond)
	procMouseEvent.Call(mouseeventfLeftdown, 0, 0, 0, 0)
	time.Sleep(40 * time.Millisecond)
	procMouseEvent.Call(mouseeventfLeftup, 0, 0, 0, 0)
	time.Sleep(200 * time.Millisecond) // let the click propagate through the UI
	return nil
}

// resolvePoint turns a selector into absolute screen coordinates.
func resolvePoint(hwnd uintptr, selector string) (int32, int32, error) {
	s := strings.ToLower(strings.TrimSpace(selector))
	switch {
	case s == "" || s == "center":
		r, err := windowRect(hwnd)
		if err != nil {
			return 0, 0, err
		}
		return (r.Left + r.Right) / 2, (r.Top + r.Bottom) / 2, nil

	case strings.HasPrefix(s, "win:"), strings.HasPrefix(s, "window:"):
		body := s[strings.Index(s, ":")+1:]
		parts := strings.Split(body, ",")
		if len(parts) != 2 {
			return 0, 0, fmt.Errorf("desktop selector %q: expected \"win:fx,fy\" with two fractions", selector)
		}
		fx, err1 := strconv.ParseFloat(strings.TrimSpace(parts[0]), 64)
		fy, err2 := strconv.ParseFloat(strings.TrimSpace(parts[1]), 64)
		if err1 != nil || err2 != nil || fx < 0 || fx > 1 || fy < 0 || fy > 1 {
			return 0, 0, fmt.Errorf("desktop selector %q: fractions must be numbers in [0,1]", selector)
		}
		r, err := windowRect(hwnd)
		if err != nil {
			return 0, 0, err
		}
		return int32(float64(r.Left) + float64(r.Right-r.Left)*fx),
			int32(float64(r.Top) + float64(r.Bottom-r.Top)*fy), nil

	case strings.Contains(s, ","):
		parts := strings.Split(s, ",")
		if len(parts) != 2 {
			return 0, 0, fmt.Errorf("desktop selector %q: expected \"x,y\"", selector)
		}
		x, err1 := strconv.ParseInt(strings.TrimSpace(parts[0]), 10, 32)
		y, err2 := strconv.ParseInt(strings.TrimSpace(parts[1]), 10, 32)
		if err1 != nil || err2 != nil {
			return 0, 0, fmt.Errorf("desktop selector %q: coordinates must be integers", selector)
		}
		return int32(x), int32(y), nil

	default:
		return 0, 0, fmt.Errorf("desktop click: unsupported selector %q (use \"x,y\", \"win:fx,fy\" or \"center\")", selector)
	}
}

// Input types text through the keyboard. If selector is non-empty it is
// first resolved and clicked (same syntax as Click), focusing the target
// control; an empty (or "focused") selector types into whatever already
// holds keyboard focus.
func (d *Driver) Input(ctx context.Context, selector, text string) error {
	s := strings.ToLower(strings.TrimSpace(selector))
	if s != "" && s != "focused" {
		if err := d.Click(ctx, selector); err != nil {
			return err
		}
	}
	return typeText(text)
}

// typeText synthesizes Unicode keystrokes via SendInput, which arrives at
// the focused window as regular WM_CHAR input — works for any text the
// keyboard can produce, no layout mapping needed.
func typeText(text string) error {
	for _, r := range text {
		if r > 0xFFFF {
			return fmt.Errorf("cannot type rune %q: above UTF-16 range", r)
		}
		down := tagINPUT{Type: inputKeyboard, Ki: keyboardInput{WScan: uint16(r), DwFlags: keyeventfUnicode}}
		up := tagINPUT{Type: inputKeyboard, Ki: keyboardInput{WScan: uint16(r), DwFlags: keyeventfUnicode | keyeventfKeyup}}
		if n, _, _ := procSendInput.Call(1, uintptr(unsafe.Pointer(&down)), unsafe.Sizeof(down)); n != 1 {
			return fmt.Errorf("SendInput (down) failed for rune %q", r)
		}
		procSendInput.Call(1, uintptr(unsafe.Pointer(&up)), unsafe.Sizeof(up))
		time.Sleep(8 * time.Millisecond)
	}
	return nil
}

// WaitFor blocks until a visible top-level window whose title contains the
// selector exists (an optional "title:"/"window:" prefix is accepted).
// When the driver launched the app, only that process's windows count —
// so an editor window that happens to contain the same text can't fool
// the wait.
func (d *Driver) WaitFor(ctx context.Context, selector string, timeout time.Duration) error {
	sub := strings.TrimSpace(selector)
	if l := strings.ToLower(sub); strings.HasPrefix(l, "title:") {
		sub = sub[len("title:"):]
	} else if strings.HasPrefix(l, "window:") {
		sub = sub[len("window:"):]
	}
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	deadline := time.Now().Add(timeout)
	for {
		if findWindow(sub, d.pid()) != 0 {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("timed out after %v waiting for a window titled %q", timeout, sub)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(200 * time.Millisecond):
		}
	}
}

// GetText returns window text. Selector: "title" (or empty) → the main
// window's current title; "title:<substring>" → the full title of the
// first visible window containing <substring>.
func (d *Driver) GetText(ctx context.Context, selector string) (string, error) {
	s := strings.TrimSpace(selector)
	if l := strings.ToLower(s); strings.HasPrefix(l, "title:") {
		s = s[len("title:"):]
	} else if strings.HasPrefix(l, "window:") {
		s = s[len("window:"):]
	} else if strings.ToLower(s) == "window" || strings.EqualFold(s, "title") {
		s = ""
	}
	if s == "" {
		hwnd, err := d.mainWindow()
		if err != nil {
			return "", err
		}
		return windowText(hwnd), nil
	}
	hwnd := findWindow(s, d.pid())
	if hwnd == 0 {
		return "", fmt.Errorf("no visible window titled %q", s)
	}
	return windowText(hwnd), nil
}

// Screenshot captures the app window (including its title bar) as PNG
// bytes, even when the window is behind other windows.
func (d *Driver) Screenshot(ctx context.Context) ([]byte, error) {
	hwnd, err := d.mainWindow()
	if err != nil {
		return nil, err
	}
	r, err := windowRect(hwnd)
	if err != nil {
		return nil, err
	}
	w, h := r.Right-r.Left, r.Bottom-r.Top
	if w <= 0 || h <= 0 {
		return nil, fmt.Errorf("window has degenerate size %dx%d", w, h)
	}

	hdcWin, _, _ := procGetDC.Call(hwnd)
	defer procReleaseDC.Call(hwnd, hdcWin)
	hdcMem, _, _ := procCreateCompatibleDC.Call(hdcWin)
	defer procDeleteDC.Call(hdcMem)

	// Create a DIB section we can read pixels from directly. PrintWindow
	// renders the target window into whatever bitmap is selected in hdcMem.
	bmi := bitmapInfo{BmiHeader: bitmapInfoHeader{
		BiSize:     uint32(unsafe.Sizeof(bitmapInfoHeader{})),
		BiWidth:    w,
		BiHeight:   -h, // top-down rows
		BiPlanes:   1,
		BiBitCount: 32,
	}}
	var pixPtr unsafe.Pointer
	hbm, _, _ := procCreateDIBSection.Call(
		uintptr(hdcMem),
		uintptr(unsafe.Pointer(&bmi)),
		0,                                // DIB_RGB_COLORS
		uintptr(unsafe.Pointer(&pixPtr)), // out: mapped pixel buffer
		0, 0,
	)
	if hbm == 0 {
		return nil, fmt.Errorf("CreateDIBSection failed")
	}
	defer procDeleteObject.Call(hbm)
	_, _, _ = procSelectObject.Call(hdcMem, hbm)

	// PW_RENDERFULLCONTENT captures GPU-composited content (Avalonia,
	// Chromium, ...); fall back to the classic flag on older systems.
	if ok, _, _ := procPrintWindow.Call(hwnd, hdcMem, pwRenderFullContent); ok == 0 {
		if ok2, _, _ := procPrintWindow.Call(hwnd, hdcMem, 0); ok2 == 0 {
			return nil, fmt.Errorf("PrintWindow failed for target window")
		}
	}

	// Copy pixels out while the DIB is still mapped; rows are BGRA, top-down.
	n := 4 * int(w) * int(h)
	pngPixels := make([]byte, n)
	copy(pngPixels, unsafe.Slice((*byte)(pixPtr), n))

	img := image.NewRGBA(image.Rect(0, 0, int(w), int(h)))
	for y := 0; y < int(h); y++ {
		row := pngPixels[y*int(w)*4 : (y+1)*int(w)*4]
		for x := 0; x < int(w); x++ {
			i := x * 4
			img.SetRGBA(x, y, color.RGBA{R: row[i+2], G: row[i+1], B: row[i], A: 0xFF}) // BGRA → RGBA
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil, fmt.Errorf("encoding PNG: %w", err)
	}
	return buf.Bytes(), nil
}

// Close asks the app to close politely (WM_CLOSE), waits for the process
// to exit, and kills it as a last resort so scenarios can't leak apps.
func (d *Driver) Close(ctx context.Context) error {
	d.mu.Lock()
	hwnd := d.hwnd
	d.hwnd = 0
	d.mu.Unlock()

	if hwnd != 0 {
		procPostMessageW.Call(hwnd, wmClose, 0, 0)
	}
	if d.exited == nil {
		return nil
	}
	waitCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	select {
	case <-d.exited:
	case <-waitCtx.Done():
		if d.cmd != nil && d.cmd.Process != nil {
			_ = d.cmd.Process.Kill()
		}
		select {
		case <-d.exited:
		case <-time.After(3 * time.Second):
		}
	}
	return nil
}
