package desktopdriver

import "time"

// Config carries the connector-level settings for the local desktop
// driver. It is populated by the DesktopConnector from the app config's
// "desktop" tool entry, e.g.:
//
//	"tools": {
//	  "desktop": {
//	    "app": "C:\\path\\to\\App.exe",
//	    "window_title": "App Main Window",
//	    "startup_timeout": "30s"
//	  }
//	}
//
// Only "app" is required; everything else falls back to sane defaults.
type Config struct {
	// App is the path (or bare command name resolved via PATH) of the
	// executable the driver should launch on Connect. Empty means "do
	// not launch anything" — the driver will just look for a window
	// matching WindowTitle (useful for attaching to an app you started
	// yourself, outside the scenario).
	App string

	// WindowTitle is a substring matched (case-sensitively) against
	// top-level window titles to find the app's main window after
	// launch. Empty means "use the first top-level window of the
	// launched process".
	WindowTitle string

	// StartupTimeout bounds how long Connect waits for the app's window
	// to appear. Defaults to 30s when zero.
	StartupTimeout time.Duration

	// WorkingDir is the directory the app is launched from. Defaults to
	// the app's own directory when empty, which is what most installed
	// apps expect (they resolve assets relative to the exe).
	WorkingDir string
}
