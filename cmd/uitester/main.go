// Command uitester runs a UI test scenario (browser or desktop app) against
// a pluggable automation backend.
//
// Usage:
//
//	uitester -config configs/config.example.yaml -scenario scenarios/example_login.yaml
//
// This file is the application's composition root: it's the only place that
// wires concrete adapters (chromedp, WebDriver, desktop, YAML config,
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

	"uitester/internal/adapter/tool"
	"uitester/internal/config"
	"uitester/internal/domain"
	"uitester/internal/reporter"
	"uitester/internal/reporter/allure"
	"uitester/internal/usecase"
)

func main() {
	os.Exit(run())
}

func run() int {
	configPath := flag.String("config", "configs/config.example.json", "path to app config JSON")
	scenarioPath := flag.String("scenario", "", "path to a scenario JSON file (required)")
	listTools := flag.Bool("list-tools", false, "print registered tool names and exit")
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

	if *scenarioPath == "" {
		fmt.Fprintln(os.Stderr, "error: -scenario is required")
		flag.Usage()
		return 2
	}

	appCfg, err := config.LoadApp(*configPath)
	if err != nil {
		logger.Error("loading app config", "error", err)
		return 1
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

	ctx := context.Background()
	result, err := runner.Run(ctx, scenario, appCfg.Tools[scenario.Tool])
	if err != nil {
		logger.Error("run failed", "error", err)
		return 1
	}
	if result.Failed() {
		return 1
	}
	return 0
}
