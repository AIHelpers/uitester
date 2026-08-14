package allure_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"uitester/internal/domain"
	"uitester/internal/reporter/allure"
)

func TestReporter_WritesValidResultAndAttachment(t *testing.T) {
	dir := t.TempDir()

	// Simulate a screenshot file the runner would have already written.
	shotPath := filepath.Join(dir, "source-screenshot.png")
	if err := os.WriteFile(shotPath, []byte("fake-png"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}

	result := domain.ScenarioResult{
		ScenarioName: "Login flow",
		Tool:         "chrome",
		Status:       domain.StatusFailed,
		StartedAt:    time.Now(),
		Duration:     2 * time.Second,
		Steps: []domain.StepResult{
			{
				Action:    domain.Action{Type: domain.ActionInput, Selector: "#password", Value: "secret", Sensitive: true},
				Status:    domain.StatusPassed,
				Log:       "[input] selector=\"#password\" value=\"••••••\" status=passed",
				StartedAt: time.Now(),
				Duration:  100 * time.Millisecond,
				Attachments: []domain.Attachment{
					{Name: "screenshot", Path: shotPath, MimeType: "image/png"},
				},
			},
			{
				Action:    domain.Action{Type: domain.ActionClick, Selector: "#submit"},
				Status:    domain.StatusFailed,
				Error:     "element not found",
				StartedAt: time.Now(),
				Duration:  50 * time.Millisecond,
			},
		},
	}

	rep := allure.New(dir)
	if err := rep.Report(context.Background(), result); err != nil {
		t.Fatalf("Report: %v", err)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}

	var resultFile string
	var attachmentFiles []string
	for _, e := range entries {
		switch {
		case strings.HasSuffix(e.Name(), "-result.json"):
			resultFile = e.Name()
		case strings.Contains(e.Name(), "-attachment"):
			attachmentFiles = append(attachmentFiles, e.Name())
		}
	}
	if resultFile == "" {
		t.Fatalf("expected a *-result.json file, found entries: %v", entries)
	}
	// One screenshot attachment + one log attachment for step 1; the failed
	// step has no screenshot in this fixture, but does get a log attachment.
	if len(attachmentFiles) < 2 {
		t.Fatalf("expected at least 2 attachment files, got %d: %v", len(attachmentFiles), attachmentFiles)
	}

	data, err := os.ReadFile(filepath.Join(dir, resultFile))
	if err != nil {
		t.Fatalf("reading result file: %v", err)
	}

	var parsed map[string]interface{}
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("result file is not valid JSON: %v", err)
	}

	if parsed["name"] != "Login flow" {
		t.Fatalf("expected name %q, got %v", "Login flow", parsed["name"])
	}
	if parsed["status"] != "failed" {
		t.Fatalf("expected status 'failed', got %v", parsed["status"])
	}
	if _, ok := parsed["uuid"].(string); !ok {
		t.Fatalf("expected a string uuid field, got %v", parsed["uuid"])
	}

	// The sensitive value must never appear anywhere in the result JSON.
	if strings.Contains(string(data), "secret") {
		t.Fatalf("sensitive value leaked into allure result JSON: %s", data)
	}

	steps, ok := parsed["steps"].([]interface{})
	if !ok || len(steps) != 2 {
		t.Fatalf("expected 2 steps in result JSON, got %v", parsed["steps"])
	}
}
