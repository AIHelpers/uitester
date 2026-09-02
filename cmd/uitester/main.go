// Command uitester runs a UI test scenario (browser or desktop app) against
// a pluggable automation backend.
//
// Usage:
//
//	uitester -config configs/config.example.json -scenario scenarios/example_login.json
//	uitester -ui                 # serve the web UI instead
//
// This file is the application's composition root: it's the only place that
// wires concrete adapters (chromedp, WebDriver, desktop, JSON config,
// console/JSON reporters) into the technology-agnostic usecase.Runner. To
// add a new tool, register a domain.ToolConnector here (or extend
// internal/adapter/tool) — nothing else in the codebase needs to change.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"uitester/internal/adapter/tool"
	"uitester/internal/config"
	"uitester/internal/domain"
	"uitester/internal/reporter"
	"uitester/internal/reporter/allure"
	"uitester/internal/ui"
	"uitester/internal/usecase"
)

func main() {
	os.Exit(run())
}

func run() int {
	configPath := flag.String("config", "configs/config.example.json", "path to app config JSON")
	scenarioPath := flag.String("scenario", "", "path to a scenario JSON file (required without -ui)")
	listTools := flag.Bool("list-tools", false, "print registered tool names and exit")
	serveUI := flag.Bool("ui", false, "serve the web UI instead of running a single scenario")
	uiAddr := flag.String("addr", "127.0.0.1:8080", "listen address for -ui")
	scenarioDir := flag.String("scenarios", "scenarios", "directory of scenario JSON files for -ui")
	flag.Parse()

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))

	registry := usecase.NewToolRegistry()
	if err := tool.RegisterDefaults(registry); err != nil {
		logger.Error("registering tool connectors", "error", err)
		return 1
	}

	if *listTools {
		fmt.Println("registered tools:", registry.Names())
		return 0
	}

	appCfg, err := config.LoadApp(*configPath)
	if err != nil {
		// -ui proceeds with defaults so a missing config isn't fatal there;
		// the single-scenario path still requires it up front.
		if !*serveUI {
			logger.Error("loading app config", "error", err)
			return 1
		}
		logger.Warn("loading app config; proceeding with defaults", "error", err)
		appCfg = config.App{}
	}

	if *serveUI {
		return serveUICommand(signalCtx(), logger, registry, *configPath, *scenarioDir, appCfg, *uiAddr)
	}

	if *scenarioPath == "" {
		fmt.Fprintln(os.Stderr, "error: -scenario is required")
		flag.Usage()
		return 2
	}

	scenario, err := config.LoadScenario(*scenarioPath)
	if err != nil {
		logger.Error("loading scenario", "error", err)
		return 1
	}

	reporters := []domain.Reporter{reporter.NewConsole(os.Stdout)}
	if appCfg.Reporters.JSONPath != "" {
		reporters = append(reporters, reporter.NewJSONFile(appCfg.Reporters.JSONPath))
	}
	if appCfg.Reporters.AllureDir != "" {
		reporters = append(reporters, allure.New(appCfg.Reporters.AllureDir))
	}

	runnerOpts := usecase.Options{
		AutoScreenshot: appCfg.AutoScreenshots(),
		ScreenshotDir:  appCfg.Screenshots.Dir,
	}
	runner := usecase.NewRunnerWithOptions(registry, logger, runnerOpts, reporters...)

	result, err := runner.Run(context.Background(), scenario, appCfg.Tools[scenario.Tool])
	if err != nil {
		logger.Error("run failed", "error", err)
		return 1
	}
	if result.Failed() {
		return 1
	}
	return 0
}

// signalCtx returns a context canceled on SIGINT/SIGTERM, so the UI server
// (and any in-flight runs it supervises) shut down gracefully on Ctrl+C.
func signalCtx() context.Context {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	_ = cancel // the process exits with main; the signal goroutine dies with it
	return ctx
}

// serveUICommand wires the same runner/reporters as the single-scenario
// mode, serves the embedded SPA + JSON API, and blocks until Ctrl+C.
func serveUICommand(ctx context.Context, logger *slog.Logger, registry *usecase.ToolRegistry,
	configPath, scenarioDir string, appCfg config.App, addr string) int {

	reporters := []domain.Reporter{}
	if appCfg.Reporters.JSONPath != "" {
		reporters = append(reporters, reporter.NewJSONFile(appCfg.Reporters.JSONPath))
	}
	if appCfg.Reporters.AllureDir != "" {
		reporters = append(reporters, allure.New(appCfg.Reporters.AllureDir))
	}
	runnerOpts := usecase.Options{
		AutoScreenshot: appCfg.AutoScreenshots(),
		ScreenshotDir:  appCfg.Screenshots.Dir,
	}
	runner := usecase.NewRunnerWithOptions(registry, logger, runnerOpts, reporters...)

	srv := ui.NewServer(registry, runner, configPath, scenarioDir, appCfg.Screenshots.Dir, logger)
	logger.Info("web UI listening", "url", "http://"+addr)
	if err := srv.Start(ctx, addr); err != nil {
		logger.Error("UI server", "error", err)
		return 1
	}
	return 0
}
