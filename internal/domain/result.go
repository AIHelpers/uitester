package domain

import "time"

// Status is the outcome of a step or an entire scenario run.
type Status string

const (
	StatusPassed  Status = "passed"
	StatusFailed  Status = "failed"
	StatusSkipped Status = "skipped"
)

// Attachment references a file captured during a step — typically a
// screenshot, but the same shape works for logs, page source dumps, etc.
// Path is a real filesystem path so any Reporter (JSON, Allure, a future
// S3-upload reporter) can read the bytes itself; StepResult doesn't hold
// raw bytes to keep results cheap to pass around and serialize.
type Attachment struct {
	Name     string `json:"name"`
	Path     string `json:"path"`
	MimeType string `json:"mime_type"`
}

// StepResult records what happened when a single Action was executed.
type StepResult struct {
	Action      Action        `json:"action"`
	Status      Status        `json:"status"`
	Error       string        `json:"error,omitempty"`
	Output      string        `json:"output,omitempty"` // e.g. text read back
	Log         string        `json:"log,omitempty"`    // human-readable one-liner: what was done, with sensitive values masked
	Attachments []Attachment  `json:"attachments,omitempty"`
	StartedAt   time.Time     `json:"started_at"`
	Duration    time.Duration `json:"duration"`
}

// ScenarioResult aggregates all StepResults plus overall pass/fail status.
type ScenarioResult struct {
	ScenarioName string        `json:"scenario_name"`
	Tool         string        `json:"tool"`
	Status       Status        `json:"status"`
	StartedAt    time.Time     `json:"started_at"`
	Duration     time.Duration `json:"duration"`
	Steps        []StepResult  `json:"steps"`
}

// Failed reports whether any step failed.
func (r ScenarioResult) Failed() bool {
	return r.Status == StatusFailed
}
