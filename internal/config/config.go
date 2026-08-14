// Package config loads the two JSON documents the CLI needs: the app config
// (which tools are available and how they're configured, which reporters to
// use) and one or more scenario files (what to actually run). Keeping this
// in its own package means the domain/usecase layers never import an
// encoding library directly. JSON (not YAML) is used deliberately: it's
// stdlib-only, which keeps the module dependency-light and avoids a whole
// class of supply-chain surface for a config-parsing concern.
package config

import (
	"encoding/json"
	"fmt"
	"os"

	"uitester/internal/domain"
)

// App is the top-level application configuration.
type App struct {
	// Tools maps a tool name (as referenced by scenario.Tool) to its
	// connector-specific config, forwarded verbatim to ToolConnector.NewDriver.
	Tools map[string]map[string]interface{} `json:"tools"`

	Reporters struct {
		JSONPath  string `json:"json_path,omitempty"`  // write a single-run JSON summary here
		AllureDir string `json:"allure_dir,omitempty"` // write Allure "allure-results" here; view with `allure serve <dir>`
	} `json:"reporters"`

	Screenshots struct {
		Auto *bool  `json:"auto,omitempty"` // capture a screenshot after every step; defaults to true when omitted
		Dir  string `json:"dir,omitempty"`  // where screenshots are written; default "results/screenshots"
	} `json:"screenshots"`
}

// AutoScreenshots reports the effective auto-screenshot setting: true
// unless the config explicitly set "auto": false.
func (a App) AutoScreenshots() bool {
	return a.Screenshots.Auto == nil || *a.Screenshots.Auto
}

func LoadApp(path string) (App, error) {
	var cfg App
	data, err := os.ReadFile(path)
	if err != nil {
		return cfg, fmt.Errorf("reading config %s: %w", path, err)
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return cfg, fmt.Errorf("parsing config %s: %w", path, err)
	}
	return cfg, nil
}

// LoadScenario reads a single scenario JSON file into the domain model.
func LoadScenario(path string) (domain.Scenario, error) {
	var s domain.Scenario
	data, err := os.ReadFile(path)
	if err != nil {
		return s, fmt.Errorf("reading scenario %s: %w", path, err)
	}
	if err := json.Unmarshal(data, &s); err != nil {
		return s, fmt.Errorf("parsing scenario %s: %w", path, err)
	}
	return s, nil
}
