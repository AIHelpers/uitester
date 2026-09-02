# uitester

A Go tool for scripted UI testing — browsers **and** native desktop
applications — built with clean architecture so new automation backends
("tools") can be plugged in without touching core logic.

## Why it's shaped this way

```
cmd/uitester            composition root (CLI, wiring)
internal/domain          entities + ports (UIDriver, ToolConnector, Reporter) — no external deps
internal/usecase          Runner + ToolRegistry — orchestration logic, depends only on domain
internal/adapter/driver     concrete UIDriver implementations (chromedp, WebDriver, desktop)
internal/adapter/tool        ToolConnector wrappers that turn config into a driver
internal/reporter            Reporter implementations (console, JSON file)
internal/config               JSON config/scenario loading
internal/ui                    embedded web UI (SPA + JSON API + live run events)
configs/, scenarios/           example files
examples/                       reference code not compiled into the module (see below)
```

Dependencies point inward: `domain` knows nothing about chromedp or HTTP;
`usecase` knows nothing about JSON or any specific browser; only the
`adapter` packages touch real automation libraries. That's what makes the
`Runner` unit-testable with fakes (see `internal/usecase/runner_test.go`)
and lets you add a new tool without editing the runner at all.

## Connecting a required tool

Two built-in backends cover almost everything:

- **`chrome`** — drives Chrome/Chromium directly via `chromedp` (DevTools
  protocol). No server needed; fast; browser-only.
- **`selenium` / `appium` / `winappdriver`** — one adapter speaking the
  standard **W3C WebDriver wire protocol**, implemented with only
  `net/http` + `encoding/json` (no third-party client library). Because
  Selenium Grid, Appium (Android/iOS), WinAppDriver (native Windows apps)
  and Appium-Mac2Driver (native macOS apps) all speak this same protocol,
  this single adapter is what lets the same tool test **either** browsers
  **or** native desktop apps — only `remote_url` and `capabilities` in
  `configs/*.json` change.

To add a genuinely new tool: implement `domain.ToolConnector` (one method
returning a `domain.UIDriver`) in `internal/adapter/tool`, and register it
in `cmd/uitester/main.go`. That's the entire extension surface.

### Local desktop automation (robotgo)

For automation that doesn't go through a WebDriver-style server (direct
pixel/coordinate control of the local desktop), see
`examples/desktopdriver_robotgo_reference.go.txt` — a complete reference
`domain.UIDriver` implementation using
[go-vgo/robotgo](https://github.com/go-vgo/robotgo). It's kept out of the
buildable module on purpose: robotgo needs CGO and platform accessibility
libraries that aren't guaranteed to be present wherever this project is
built, and pulling it in would break `go build ./...`/`go mod tidy` out of
the box. To use it: copy the file into
`internal/adapter/driver/desktopdriver/driver_desktop.go`, run
`go get github.com/go-vgo/robotgo`, and build with `-tags desktop`.
`internal/adapter/driver/desktopdriver/driver_stub.go` is what compiles by
default instead, and returns a clear "not supported" error for every method.

## Scenario format

A scenario is a JSON file: a name, which tool to use, an optional base
URL, and an ordered list of steps. See `scenarios/example_login.json`
(browser) and `scenarios/example_desktop_app.json` (native Windows app via
WinAppDriver).

```json
{
  "name": "Login with valid credentials",
  "tool": "chrome",
  "base_url": "https://example.com/login",
  "steps": [
    { "type": "wait_for", "selector": "#username", "timeout": "10s" },
    { "type": "input", "selector": "#username", "value": "demo-user" },
    { "type": "click", "selector": "#login-button" },
    { "type": "assert_text", "selector": "#welcome-message", "value": "Welcome, demo-user" },
    { "type": "screenshot", "value": "results/login-success.png" }
  ]
}
```

Supported step types: `navigate`, `click`, `input`, `wait_for`,
`assert_text`, `assert_exist`, `screenshot`, `sleep`. The runner fails fast
— it stops at the first failed step so later steps don't cascade into
confusing secondary failures. Mark any step `"sensitive": true` (typically
`input` steps for passwords/tokens) to have its value masked everywhere it
gets reported — console, JSON, and Allure — while the real value still
reaches the driver. Masking happens at JSON-marshal time on the `Action`
type itself, so any current or future JSON-based reporter gets it for free
rather than needing to remember to mask.

## Screenshots and input logging

Every step — pass or fail — automatically gets a screenshot attached to
its result (`screenshots.auto` in the app config, on by default) and a
structured log line recording what was done (`[input] selector="#user"
value="demo-user" status=passed`), with sensitive values masked. This
happens regardless of which reporters are enabled — it's runner behavior,
not reporter behavior — so a failing step always has a screenshot next to
it in whichever report you look at. Auto-screenshots land in
`screenshots.dir` (default `results/screenshots`); disable with
`"screenshots": {"auto": false}` if a driver/environment can't capture
them (e.g. running the default desktop stub) or you only want screenshots
from explicit `screenshot` steps.

## Allure reports

Set `reporters.allure_dir` in the app config (e.g. `"results/allure-results"`)
to have the runner write a standard Allure results directory: one
`<uuid>-result.json` per scenario, with steps, parameters (selector/value,
masked when sensitive), and attachments (the auto-captured screenshots plus
a text attachment of each step's log line). View it with the
[Allure commandline](https://allurereport.org/docs/gettingstarted/):

```bash
allure serve results/allure-results
# or, to produce a static site:
allure generate results/allure-results -o results/allure-report --clean
```

This reporter only *writes* the results format — rendering the HTML report
is Allure's own, actively-maintained job. `internal/reporter/allure` has no
external dependency (not even a UUID library — see `uuid.go`), consistent
with the rest of the project.

## Web UI

The binary ships with a built-in single-page web app for editing scenarios,
kicking off runs, and watching them live — no separate frontend build or
deployment, the static files are embedded with `go:embed`:

```bash
./uitester -ui                              # serve at http://127.0.0.1:8080
./uitester -ui -addr :9090                  # different port
./uitester -ui -scenarios my/scenarios      # where scenario JSON files live
```

What it gives you:

- **Scenario editor** — a list of every scenario file in the directory with
  last-run badges, plus a table editor for the step list (add/remove/reorder
  rows, mark values as `sensitive`, pick the tool from the registered set).
  Saving writes the same JSON format the CLI reads; both front-ends share one
  source of truth. Sensitive values round-trip verbatim through the editor
  (a no-op save never destroys a stored secret) but are masked everywhere
  results are shown.
- **Live run monitor** — start a run from the browser and watch step-by-step
  progress stream in over Server-Sent Events (polling fallback included):
  current step, per-step log lines, auto-screenshot thumbnails, elapsed time,
  and a Stop button. The event buffer is replayed on refresh, so reopening a
  mid-run page shows the full history.
- **Results + config views** — a history of runs this server process has
  executed (with status, duration, and screenshot attachments) and an editor
  for the app config JSON. Config edits take effect on the next run without
  restarting the server.

Everything the UI does is also scriptable — it's just a JSON API:

| Endpoint | Purpose |
| --- | --- |
| `GET/POST /api/scenarios`, `GET/PUT/DELETE /api/scenarios/{id}` | scenario CRUD (files in `-scenarios` dir) |
| `GET/PUT /api/config` | app config read/write |
| `GET /api/tools` | registered tool names |
| `POST /api/run` | start a run (`{"scenario": "<id or name>"}`), returns `run_id` |
| `GET /api/run/{id}` | run status/progress snapshot |
| `GET /api/run/{id}/events` | SSE stream of `step` and `done` events |
| `POST /api/run/{id}/stop` | request cancellation |
| `GET /api/results`, `GET /api/results/{id}` | finished-run history/detail |
| `GET /api/screenshots/{name}` | screenshot files (traversal-safe) |
| `GET /api/report` | raw JSON report file, when `reporters.json_path` is set |

The UI is a thin adapter over the same `usecase.Runner` the CLI uses —
no core logic was added or changed for it, and `internal/ui` is covered by
its own `httptest`-based test suite (`internal/ui/api_test.go`).

## Running it

```bash
go build ./cmd/uitester

# Browser test via local Chrome/Chromium (requires Chrome installed):
./uitester -config configs/config.example.json -scenario scenarios/example_login.json

# Against a Selenium Grid / Appium / WinAppDriver server, edit
# configs/config.example.json's remote_url + capabilities, then point
# a scenario's "tool" at "selenium" / "appium" / "winappdriver".

./uitester -list-tools   # see every registered tool name
./uitester -ui           # serve the web UI instead of a single run
```

Exit code is `0` on pass, `1` on failure/error, `2` on bad CLI usage —
standard for CI integration. Console output plus an optional JSON report
(`reporters.json_path` in the app config) are both written by default; add
more `domain.Reporter` implementations for e.g. JUnit XML or Slack webhooks.

## Testing

```bash
go test ./...          # unit tests (fakes) + an adapter test against a
                        # real in-memory HTTP server implementing the W3C
                        # protocol — no live browser or Selenium server needed
go vet ./...
gofmt -l .
```

`internal/usecase/runner_test.go` exercises the orchestration logic
(fail-fast behavior, unknown-tool errors, reporter fan-out) against a fake
`UIDriver` — no browser required. `internal/adapter/driver/webdriverdriver/driver_test.go`
runs the actual HTTP client against an `httptest.Server`, so the WebDriver
adapter itself is verified with real requests/responses, not just mocked
interfaces.

## Dependencies

Only one external module: `github.com/chromedp/chromedp` (for the `chrome`
tool). Everything else — the WebDriver adapter, JSON config, reporters —
uses only the Go standard library, by design: fewer supply-chain
dependencies for a test-execution tool that will often run with elevated
CI credentials.
