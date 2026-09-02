// Package ui serves the embedded single-page web UI and its JSON API on top
// of the existing usecase layer. It is a pure adapter: it talks to
// usecase.Runner / usecase.ToolRegistry and domain types, and changes no
// core logic. The frontend (static/*) is embedded into the binary with
// go:embed, so the whole UI ships as part of the single uitester binary —
// no separate frontend build or deployment step.
package ui

import (
	"context"
	"embed"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	"uitester/internal/usecase"
)

//go:embed static
var staticFS embed.FS

// Server wires the HTTP API, the async runner, and the embedded SPA
// together. All file paths are absolute-friendly relative paths resolved
// from the process working directory (same convention as the CLI flags).
type Server struct {
	registry      *usecase.ToolRegistry
	runner        *usecase.Runner
	async         *AsyncRunner
	configPath    string
	scenarioDir   string
	screenshotDir string
	logger        *slog.Logger
}

// NewServer builds a Server. screenshotDir is where the runner writes
// auto-captured screenshots (usecase.Options.ScreenshotDir) — the UI serves
// them from there so attachments and URLs line up without copying files.
func NewServer(registry *usecase.ToolRegistry, runner *usecase.Runner, configPath, scenarioDir, screenshotDir string, logger *slog.Logger) *Server {
	if logger == nil {
		logger = slog.Default()
	}
	if screenshotDir == "" {
		screenshotDir = "results/screenshots"
	}
	s := &Server{
		registry:      registry,
		runner:        runner,
		configPath:    configPath,
		scenarioDir:   scenarioDir,
		screenshotDir: screenshotDir,
		logger:        logger,
	}
	s.async = NewAsyncRunner(runner, viewConverter{
		stepView:   s.makeStepView,
		resultView: s.makeResultView,
	})
	return s
}

// Handler builds the full HTTP handler: JSON API under /api/ and the
// embedded SPA at every other path.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	// --- API routes ---
	mux.HandleFunc("GET /api/scenarios", s.handleListScenarios)
	mux.HandleFunc("POST /api/scenarios", s.handleSaveScenario)
	mux.HandleFunc("GET /api/scenarios/{id}", s.handleGetScenario)
	mux.HandleFunc("PUT /api/scenarios/{id}", s.handleSaveScenario)
	mux.HandleFunc("DELETE /api/scenarios/{id}", s.handleDeleteScenario)

	mux.HandleFunc("GET /api/config", s.handleGetConfig)
	mux.HandleFunc("PUT /api/config", s.handlePutConfig)
	mux.HandleFunc("GET /api/tools", s.handleListTools)

	mux.HandleFunc("POST /api/run", s.handleStartRun)
	mux.HandleFunc("GET /api/run/{id}", s.handleGetRun)
	mux.HandleFunc("GET /api/run/{id}/events", s.handleRunEvents)
	mux.HandleFunc("POST /api/run/{id}/stop", s.handleStopRun)

	mux.HandleFunc("GET /api/results", s.handleListResults)
	mux.HandleFunc("GET /api/results/{id}", s.handleGetResult)
	mux.HandleFunc("GET /api/screenshots/{name}", s.handleScreenshot)
	mux.HandleFunc("GET /api/report", s.handleReport)

	// --- SPA ---
	mux.Handle("GET /", spaHandler())

	return logRequests(s.logger, mux)
}

// spaHandler serves the embedded frontend. Unknown non-API paths fall back
// to index.html so the hash router deep-links work on a hard refresh.
func spaHandler() http.Handler {
	sub, err := fs.Sub(staticFS, "static")
	if err != nil {
		panic(fmt.Sprintf("embedded static FS is corrupt: %v", err))
	}
	fileServer := http.FileServer(http.FS(sub))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/")
		if path == "" {
			path = "index.html"
		}
		if _, err := fs.Stat(sub, path); err != nil {
			// Serve the SPA shell for client-side routes.
			serveIndex(w, sub)
			return
		}
		fileServer.ServeHTTP(w, r)
	})
}

func serveIndex(w http.ResponseWriter, sub fs.FS) {
	data, err := fs.ReadFile(sub, "index.html")
	if err != nil {
		http.Error(w, "index.html missing from embedded FS", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	_, _ = w.Write(data)
}

// logRequests adds one compact log line per request at debug level, so a
// production console isn't flooded, but troubleshooting is still possible.
func logRequests(logger *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		if r.URL.Path == "/api/run/" || strings.HasSuffix(r.URL.Path, "/events") {
			return // SSE streams stay open for the whole run; don't log them
		}
		logger.Debug("http", "method", r.Method, "path", r.URL.Path, "status", rec.status, "took", time.Since(start).Round(time.Millisecond))
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(status int) {
	r.status = status
	r.ResponseWriter.WriteHeader(status)
}

// Flush lets the SSE handler work through the logging wrapper.
func (r *statusRecorder) Flush() {
	if f, ok := r.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Start runs the HTTP server on addr (e.g. ":8080") and blocks until the
// context is canceled or the listener fails.
func (s *Server) Start(ctx context.Context, addr string) error {
	httpSrv := &http.Server{
		Addr:              addr,
		Handler:           s.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("listening on %s: %w", addr, err)
	}

	errCh := make(chan error, 1)
	go func() {
		if err := httpSrv.Serve(listener); err != nil && err != http.ErrServerClosed {
			errCh <- err
		}
	}()

	s.logger.Info("UI server listening", "addr", listener.Addr().String())
	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return httpSrv.Shutdown(shutdownCtx)
	case err := <-errCh:
		return err
	}
}
