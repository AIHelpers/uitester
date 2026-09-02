//go:build windows

// Unit tests for the Win32 desktop driver.
//
// Hermeticity ground rules (these run on a real desktop, so they must never
// disturb it):
//
//   - the window fixture is created off-screen (-32000,-32000), tiny, with
//     WS_EX_TOOLWINDOW (no taskbar entry) and WS_EX_NOACTIVATE (no focus
//     stealing) — invisible to the user in practice;
//   - no test synthesizes real input: only error paths that fail *before*
//     SetCursorPos/SendInput are exercised, or empty text (zero keystrokes);
//   - no test launches a real application;
//   - if a window can't be created (headless CI session), tests skip.
package desktopdriver

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image/png"
	"os"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
	"unsafe"

	"uitester/internal/domain"
)

// ---------------------------------------------------------------------------
// Win32 test fixture
// ---------------------------------------------------------------------------

var (
	testUser32           = syscall.NewLazyDLL("user32.dll")
	testKernel32         = syscall.NewLazyDLL("kernel32.dll")
	procRegisterClassExW = testUser32.NewProc("RegisterClassExW")
	procCreateWindowExW  = testUser32.NewProc("CreateWindowExW")
	procDestroyWindow    = testUser32.NewProc("DestroyWindow")
	procDefWindowProcW   = testUser32.NewProc("DefWindowProcW")
	procGetModuleHandleW = testKernel32.NewProc("GetModuleHandleW")
)

const (
	wsPopup        = 0x80000000
	wsVisible      = 0x10000000
	wsExToolWin    = 0x00000080
	wsExNoActivate = 0x08000000
)

// testWndClassEx mirrors the full WNDCLASSEXW (including the trailing
// HIconSm) so RegisterClassExW's cbSize validation passes.
type testWndClassEx struct {
	CbSize        uint32
	Style         uint32
	LpfnWndProc   uintptr
	CbClsExtra    int32
	CbWndExtra    int32
	HInstance     uintptr
	HIcon         uintptr
	HCursor       uintptr
	HbrBackground uintptr
	LpszMenuName  *uint16
	LpszClassName *uint16
	HIconSm       uintptr
}

var (
	testClassOnce   sync.Once
	testClassAtom   uint16
	testClassName16 *uint16
	testHInstance   uintptr
)

var titleCounter atomic.Uint32

// uniqueTitle returns a title that no other window on the desktop (and no
// other test) can contain as a substring: the "-end" suffix keeps "-1" from
// matching "-10" style neighbors.
func uniqueTitle() string {
	return fmt.Sprintf("uitester-unit-window-%d-%d-end", os.Getpid(), titleCounter.Add(1))
}

func ensureTestClass(t *testing.T) {
	t.Helper()
	testClassOnce.Do(func() {
		h, _, _ := procGetModuleHandleW.Call(0)
		testHInstance = h
		name, err := syscall.UTF16PtrFromString(fmt.Sprintf("uitester_test_wnd_%d", os.Getpid()))
		if err != nil {
			return
		}
		testClassName16 = name
		wc := testWndClassEx{
			CbSize:        uint32(unsafe.Sizeof(testWndClassEx{})),
			LpfnWndProc:   procDefWindowProcW.Addr(), // no custom WndProc needed
			HInstance:     testHInstance,
			LpszClassName: testClassName16,
		}
		atom, _, _ := procRegisterClassExW.Call(uintptr(unsafe.Pointer(&wc)))
		testClassAtom = uint16(atom)
	})
	if testClassAtom == 0 {
		t.Skip("RegisterClassExW failed; skipping Win32 window tests (headless session?)")
	}
}

// newTestWindow creates an off-screen top-level window owned by the test
// process. With visible=false the window has no WS_VISIBLE style, so Win32
// reports it as hidden — exactly what the driver's visibility checks expect.
func newTestWindow(t *testing.T, title string, visible bool) uintptr {
	t.Helper()
	ensureTestClass(t)
	// Window handles are thread-affine: pin the goroutine so the window is
	// created and destroyed on the same OS thread.
	runtime.LockOSThread()

	name, err := syscall.UTF16PtrFromString(title)
	if err != nil {
		runtime.UnlockOSThread()
		t.Fatalf("window title %q: %v", title, err)
	}
	style := uintptr(wsPopup)
	if visible {
		style |= wsVisible
	}
	var x, y int32 = -32000, -32000
	hwnd, _, _ := procCreateWindowExW.Call(
		wsExToolWin|wsExNoActivate,
		uintptr(unsafe.Pointer(testClassName16)),
		uintptr(unsafe.Pointer(name)),
		style,
		uintptr(x), uintptr(y), 120, 80,
		0, 0, testHInstance, 0,
	)
	if hwnd == 0 {
		runtime.UnlockOSThread()
		t.Skip("CreateWindowExW failed; skipping Win32 window tests (headless session?)")
	}
	t.Cleanup(func() {
		procDestroyWindow.Call(hwnd)
		runtime.UnlockOSThread()
	})
	return hwnd
}

// attach marks hwnd as the driver's "connected" window without going through
// Connect (which sleeps for layout settling).
func attach(t *testing.T, d *Driver, hwnd uintptr) {
	t.Helper()
	d.mu.Lock()
	d.hwnd = hwnd
	d.mu.Unlock()
}

// ---------------------------------------------------------------------------
// resolvePoint: selector parsing
// ---------------------------------------------------------------------------

func TestResolvePoint_AbsoluteCoordinates(t *testing.T) {
	cases := []struct {
		selector     string
		wantX, wantY int32
	}{
		{"100,200", 100, 200},
		{" 100 , 200 ", 100, 200}, // whitespace tolerated
		{"0,0", 0, 0},
		{"-5,-10", -5, -10}, // multi-monitor coordinates can be negative
	}
	for _, tc := range cases {
		x, y, err := resolvePoint(0, tc.selector)
		if err != nil {
			t.Fatalf("resolvePoint(%q): %v", tc.selector, err)
		}
		if x != tc.wantX || y != tc.wantY {
			t.Fatalf("resolvePoint(%q) = (%d,%d), want (%d,%d)", tc.selector, x, y, tc.wantX, tc.wantY)
		}
	}
}

func TestResolvePoint_RejectsBadSelectors(t *testing.T) {
	cases := []struct {
		selector string
		wantErr  string
	}{
		{"1,2,3", `expected "x,y"`},
		{"10,20,", `expected "x,y"`},
		{"abc,def", "coordinates must be integers"},
		{"1.5,2", "coordinates must be integers"},
		{"foo", "unsupported selector"},
		{"win:0.5", "two fractions"},
		{"win:0.5,0.5,0.5", "two fractions"},
		{"window:0.5", "two fractions"},
		{"win:1.5,0.5", "fractions must be numbers in [0,1]"},
		{"win:-0.5,0.5", "fractions must be numbers in [0,1]"},
		{"win:a,b", "fractions must be numbers in [0,1]"},
	}
	for _, tc := range cases {
		_, _, err := resolvePoint(0, tc.selector)
		if err == nil {
			t.Fatalf("resolvePoint(%q): expected error, got nil", tc.selector)
		}
		if !strings.Contains(err.Error(), tc.wantErr) {
			t.Fatalf("resolvePoint(%q) error %q does not contain %q", tc.selector, err, tc.wantErr)
		}
	}
}

func TestResolvePoint_CenterAndWindowFractions(t *testing.T) {
	hwnd := newTestWindow(t, uniqueTitle(), false)
	r, err := windowRect(hwnd)
	if err != nil {
		t.Fatalf("windowRect: %v", err)
	}

	// "" and "center" (any case, any whitespace) resolve to the midpoint.
	for _, sel := range []string{"", "center", "  CENTER  "} {
		x, y, err := resolvePoint(hwnd, sel)
		if err != nil {
			t.Fatalf("resolvePoint(%q): %v", sel, err)
		}
		if wantX, wantY := (r.Left+r.Right)/2, (r.Top+r.Bottom)/2; x != wantX || y != wantY {
			t.Fatalf("resolvePoint(%q) = (%d,%d), want (%d,%d)", sel, x, y, wantX, wantY)
		}
	}

	// Fraction endpoints land exactly on the window rectangle corners.
	for _, tc := range []struct {
		selector     string
		wantX, wantY int32
	}{
		{"win:0,0", r.Left, r.Top},
		{"win:1,1", r.Right, r.Bottom},
	} {
		x, y, err := resolvePoint(hwnd, tc.selector)
		if err != nil {
			t.Fatalf("resolvePoint(%q): %v", tc.selector, err)
		}
		if x != tc.wantX || y != tc.wantY {
			t.Fatalf("resolvePoint(%q) = (%d,%d), want (%d,%d)", tc.selector, x, y, tc.wantX, tc.wantY)
		}
	}

	// The "window:" alias and mixed case are accepted, and fractions between
	// the endpoints stay inside the rectangle (exact expectations are
	// DPI-scale dependent, so the containment is what's asserted).
	for _, sel := range []string{"win:0.25,0.75", "WINDOW:0.25,0.75", "win:0.5,0.5"} {
		x, y, err := resolvePoint(hwnd, sel)
		if err != nil {
			t.Fatalf("resolvePoint(%q): %v", sel, err)
		}
		if x < r.Left || x > r.Right || y < r.Top || y > r.Bottom {
			t.Fatalf("resolvePoint(%q) = (%d,%d) outside window rect %+v", sel, x, y, r)
		}
	}
}

func TestResolvePoint_NeedsRealWindowForRelativeSelectors(t *testing.T) {
	// "center"/"win:" read the window rect; with no window they must fail
	// rather than return garbage coordinates.
	for _, sel := range []string{"center", "win:0.5,0.5"} {
		if _, _, err := resolvePoint(0, sel); err == nil {
			t.Fatalf("resolvePoint(0, %q): expected error, got nil", sel)
		}
	}
}

// ---------------------------------------------------------------------------
// Window enumeration helpers
// ---------------------------------------------------------------------------

func TestEnumTopWindows(t *testing.T) {
	hwnd := newTestWindow(t, uniqueTitle(), false)

	if got := enumTopWindows(func(h uintptr) bool { return h == hwnd }); got != hwnd {
		t.Fatalf("expected enumeration to find the fixture window %#x, got %#x", hwnd, got)
	}
	if got := enumTopWindows(func(uintptr) bool { return false }); got != 0 {
		t.Fatalf("expected no match for always-false predicate, got %#x", got)
	}
	// The shared callback state must be reset after every call.
	enumMu.Lock()
	stale := enumMatch != nil
	enumMu.Unlock()
	if stale {
		t.Fatal("enumTopWindows left a stale match predicate behind")
	}
}

func TestWindowHelpersOnFixture(t *testing.T) {
	title := uniqueTitle()
	hwnd := newTestWindow(t, title, false)

	if got := windowText(hwnd); got != title {
		t.Fatalf("windowText = %q, want %q", got, title)
	}
	if pid := windowPid(hwnd); pid != uint32(os.Getpid()) {
		t.Fatalf("windowPid = %d, want the test process pid %d", pid, os.Getpid())
	}
	if _, err := windowRect(hwnd); err != nil {
		t.Fatalf("windowRect: %v", err)
	}
}

func TestFindWindow(t *testing.T) {
	title := uniqueTitle()
	hwnd := newTestWindow(t, title, true) // visible fixture

	// Full and partial title substrings both find the fixture.
	if got := findWindow(title, 0); got != hwnd {
		t.Fatalf("findWindow(full title) = %#x, want fixture %#x", got, hwnd)
	}
	sub := title[len(title)-6:]
	if got := findWindow(sub, 0); got != hwnd {
		t.Fatalf("findWindow(%q) = %#x, want fixture %#x", sub, got, hwnd)
	}

	// A non-zero pid filters out windows of other processes: a foreign pid
	// (System, pid 4) never matches; our own pid does.
	if got := findWindow(title, 4); got != 0 { // pid 4 = System, never ours
		t.Fatalf("findWindow with foreign pid matched %#x, want none", got)
	}
	if got := findWindow(title, uint32(os.Getpid())); got != hwnd {
		t.Fatalf("findWindow(title, own pid) = %#x, want fixture %#x", got, hwnd)
	}

	// Titles that exist nowhere match nothing.
	if got := findWindow(uniqueTitle(), 0); got != 0 {
		t.Fatalf("findWindow(unknown title) = %#x, want 0", got)
	}

	// Hidden windows are invisible to findWindow even with exact title.
	hidden := newTestWindow(t, uniqueTitle(), false)
	if got := findWindow(windowText(hidden), 0); got != 0 {
		t.Fatalf("findWindow found hidden window %#x, want 0", got)
	}
}

// ---------------------------------------------------------------------------
// Driver: Connect / Close
// ---------------------------------------------------------------------------

func TestConnect_RequiresWindowTitleWhenNotLaunching(t *testing.T) {
	d := New(Config{})
	err := d.Connect(context.Background())
	if err == nil {
		t.Fatal("expected error when neither app nor window_title is configured, got nil")
	}
	if !strings.Contains(err.Error(), "window_title") {
		t.Fatalf("expected error to mention window_title, got: %v", err)
	}
}

func TestConnect_AppNotFound(t *testing.T) {
	missing := fmt.Sprintf("%s%c%s", t.TempDir(), os.PathSeparator, "no-such-app.exe")
	d := New(Config{App: missing})
	err := d.Connect(context.Background())
	if err == nil {
		t.Fatal("expected error for missing app, got nil")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Fatalf("expected 'not found' error, got: %v", err)
	}
}

func TestConnect_AttachTimesOutWithConfiguredTimeout(t *testing.T) {
	title := uniqueTitle()
	d := New(Config{WindowTitle: title, StartupTimeout: 700 * time.Millisecond})
	start := time.Now()
	err := d.Connect(context.Background())
	if err == nil {
		t.Fatal("expected timeout error for a window that does not exist, got nil")
	}
	if el := time.Since(start); el > 10*time.Second {
		t.Fatalf("attach timed out far later than the configured 700ms: %v", el)
	}
	msg := err.Error()
	if !strings.Contains(msg, "700ms") {
		t.Fatalf("expected timeout error to report the configured 700ms, got: %v", err)
	}
	if !strings.Contains(msg, title) {
		t.Fatalf("expected timeout error to mention window title %q, got: %v", title, err)
	}
}

func TestConnect_AttachesToExistingWindow(t *testing.T) {
	title := uniqueTitle()
	newTestWindow(t, title, true)

	d := New(Config{WindowTitle: title, StartupTimeout: 2 * time.Second})
	if err := d.Connect(context.Background()); err != nil {
		t.Fatalf("Connect to existing window: %v", err)
	}
	if _, err := d.mainWindow(); err != nil {
		t.Fatalf("expected a window attached after Connect: %v", err)
	}
	if got, err := d.GetText(context.Background(), ""); err != nil || got != title {
		t.Fatalf("GetText after attach = (%q, %v), want (%q, nil)", got, err, title)
	}
	if err := d.Close(context.Background()); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

func TestConnect_HonorsContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	d := New(Config{WindowTitle: uniqueTitle(), StartupTimeout: 30 * time.Second})
	if err := d.Connect(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
}

func TestClose_NoSessionIsNoop(t *testing.T) {
	d := New(Config{})
	if err := d.Close(context.Background()); err != nil {
		t.Fatalf("Close on a never-connected driver: %v", err)
	}
}

func TestClose_ReturnsOnceLaunchedProcessExited(t *testing.T) {
	d := New(Config{})
	exited := make(chan struct{})
	close(exited)
	d.exited = exited // simulate: a process was launched and already terminated
	if err := d.Close(context.Background()); err != nil {
		t.Fatalf("Close with already-exited process: %v", err)
	}
}

func TestDriver_PidWithoutProcess(t *testing.T) {
	if pid := New(Config{}).pid(); pid != 0 {
		t.Fatalf("pid without a launched process = %d, want 0", pid)
	}
}

// ---------------------------------------------------------------------------
// Driver: Navigate / Click / Input / Screenshot
// ---------------------------------------------------------------------------

func TestNavigate_Unsupported(t *testing.T) {
	d := New(Config{})
	if err := d.Navigate(context.Background(), "https://example.com"); !errors.Is(err, domain.ErrUnsupported) {
		t.Fatalf("expected ErrUnsupported from Navigate, got %v", err)
	}
}

func TestClick_RequiresAttachedWindow(t *testing.T) {
	d := New(Config{})
	if err := d.Click(context.Background(), "100,100"); err == nil || !strings.Contains(err.Error(), "no window attached") {
		t.Fatalf("expected 'no window attached' error, got %v", err)
	}
}

func TestClick_InvalidSelectorFailsBeforeMovingCursor(t *testing.T) {
	// resolvePoint must reject malformed selectors before any cursor
	// movement happens, so this test cannot disturb the desktop.
	d := New(Config{})
	attach(t, d, newTestWindow(t, uniqueTitle(), false))
	if err := d.Click(context.Background(), "1,2,3"); err == nil {
		t.Fatal("expected error for malformed selector, got nil")
	}
}

func TestInput_WithoutTextTypesNothing(t *testing.T) {
	d := New(Config{})
	// Empty text → zero keystrokes; the empty/"focused" selectors must not
	// attempt a click either.
	for _, sel := range []string{"", "focused", "  FOCUSED  "} {
		if err := d.Input(context.Background(), sel, ""); err != nil {
			t.Fatalf("Input(%q, \"\"): %v", sel, err)
		}
	}
}

func TestInput_NonFocusedSelectorRoutesThroughClick(t *testing.T) {
	d := New(Config{})
	// With no window attached, any location selector must fail in Click —
	// proving Input resolves the selector before typing.
	if err := d.Input(context.Background(), "win:0.5,0.5", "hello"); err == nil || !strings.Contains(err.Error(), "no window attached") {
		t.Fatalf("expected 'no window attached' error, got %v", err)
	}
}

func TestTypeText_RejectsRunesAboveUTF16(t *testing.T) {
	// The validation fires before any keystroke is synthesized, so this
	// test types nothing.
	if err := typeText("\U0010FFFF"); err == nil || !strings.Contains(err.Error(), "UTF-16") {
		t.Fatalf("expected 'above UTF-16 range' error, got %v", err)
	}
	if err := typeText(""); err != nil {
		t.Fatalf("typeText(\"\"): %v", err)
	}
}

func TestScreenshot_RequiresAttachedWindow(t *testing.T) {
	d := New(Config{})
	if _, err := d.Screenshot(context.Background()); err == nil || !strings.Contains(err.Error(), "no window attached") {
		t.Fatalf("expected 'no window attached' error, got %v", err)
	}
}

func TestScreenshot_CapturesAttachedWindowAsPNG(t *testing.T) {
	d := New(Config{})
	attach(t, d, newTestWindow(t, uniqueTitle(), true))

	pngBytes, err := d.Screenshot(context.Background())
	if err != nil {
		t.Fatalf("Screenshot: %v", err)
	}
	cfg, err := png.DecodeConfig(bytes.NewReader(pngBytes))
	if err != nil {
		t.Fatalf("screenshot bytes are not a valid PNG: %v", err)
	}
	if cfg.Width != 120 || cfg.Height != 80 {
		t.Fatalf("expected fixture window size 120x80 in PNG, got %dx%d", cfg.Width, cfg.Height)
	}
}

// ---------------------------------------------------------------------------
// Driver: WaitFor / GetText
// ---------------------------------------------------------------------------

func TestWaitFor_FindsWindowByTitle(t *testing.T) {
	title := uniqueTitle()
	newTestWindow(t, title, true)
	d := New(Config{})
	for _, sel := range []string{title, "title:" + title, "window:" + title} {
		if err := d.WaitFor(context.Background(), sel, 2*time.Second); err != nil {
			t.Fatalf("WaitFor(%q): %v", sel, err)
		}
	}
}

func TestWaitFor_TimesOutWithConfiguredTimeout(t *testing.T) {
	d := New(Config{})
	sub := uniqueTitle()
	err := d.WaitFor(context.Background(), sub, 400*time.Millisecond)
	if err == nil {
		t.Fatal("expected timeout error for a window that does not exist, got nil")
	}
	msg := err.Error()
	if !strings.Contains(msg, "400ms") {
		t.Fatalf("expected timeout error to report the configured 400ms, got: %v", err)
	}
	if !strings.Contains(msg, sub) {
		t.Fatalf("expected timeout error to mention %q, got: %v", sub, err)
	}
}

func TestWaitFor_DefaultTimeoutHonorsCancellation(t *testing.T) {
	// timeout<=0 falls back to a 10s default; a cancelled context must
	// still return immediately.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	d := New(Config{})
	if err := d.WaitFor(ctx, uniqueTitle(), 0); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
}

func TestGetText_MainWindowTitle(t *testing.T) {
	title := uniqueTitle()
	d := New(Config{})
	attach(t, d, newTestWindow(t, title, false))

	// Empty selector and its aliases return the attached window's title.
	for _, sel := range []string{"", "title", "TITLE", "window", "Window"} {
		if got, err := d.GetText(context.Background(), sel); err != nil || got != title {
			t.Fatalf("GetText(%q) = (%q, %v), want (%q, nil)", sel, got, err, title)
		}
	}
}

func TestGetText_ByTitleSubstring(t *testing.T) {
	title := uniqueTitle()
	newTestWindow(t, title, true)
	d := New(Config{})

	for _, sel := range []string{"title:" + title, "window:" + title, "TITLE:" + title} {
		got, err := d.GetText(context.Background(), sel)
		if err != nil {
			t.Fatalf("GetText(%q): %v", sel, err)
		}
		if got != title {
			t.Fatalf("GetText(%q) = %q, want full title %q", sel, got, title)
		}
	}
}

func TestGetText_UnknownWindowErrors(t *testing.T) {
	d := New(Config{})
	_, err := d.GetText(context.Background(), "title:"+uniqueTitle())
	if err == nil || !strings.Contains(err.Error(), "no visible window") {
		t.Fatalf("expected 'no visible window' error, got %v", err)
	}
}

func TestGetText_RequiresAttachedWindow(t *testing.T) {
	d := New(Config{})
	if _, err := d.GetText(context.Background(), ""); err == nil || !strings.Contains(err.Error(), "no window attached") {
		t.Fatalf("expected 'no window attached' error, got %v", err)
	}
}
