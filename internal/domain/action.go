package domain

import (
	"encoding/json"
	"time"
)

// (Duration is defined in duration.go)

// ActionType enumerates the kinds of UI interactions the runner understands.
// Every driver (browser, desktop, mobile) must interpret the same vocabulary,
// which is what lets a Scenario stay driver-agnostic.
type ActionType string

const (
	ActionNavigate    ActionType = "navigate"     // open a URL (browser-only; no-op for most desktop drivers)
	ActionClick       ActionType = "click"        // click an element located by Selector
	ActionInput       ActionType = "input"        // type Value into the element located by Selector
	ActionWaitFor     ActionType = "wait_for"     // block until Selector is present/visible
	ActionAssertText  ActionType = "assert_text"  // fail the step if Selector's text != Value
	ActionAssertExist ActionType = "assert_exist" // fail the step if Selector is not found
	ActionScreenshot  ActionType = "screenshot"   // capture a screenshot, saved under Value (path) if set
	ActionSleep       ActionType = "sleep"        // pause for Timeout, useful for flaky third-party widgets
)

// Action is a single, serializable instruction in a test Scenario.
// Selector semantics are driver-defined: chromedp/webdriver use CSS selectors,
// a desktop driver might use an accessibility id or coordinates encoded as "x,y".
type Action struct {
	Type     ActionType `json:"type"`
	Selector string     `json:"selector,omitempty"`
	Value    string     `json:"value,omitempty"`
	Timeout  Duration   `json:"timeout,omitempty"` // e.g. "10s" in JSON; see Duration

	// Sensitive marks Value as secret (passwords, tokens, PII). When true,
	// reports and logs show a masked placeholder instead of the raw value —
	// screenshots are unaffected (masking pixels is a driver-level concern),
	// but structured logs, console output, and Allure step parameters never
	// print the real text.
	Sensitive bool `json:"sensitive,omitempty"`
}

// DisplayValue returns Value, or a fixed-width mask if the action is
// Sensitive. Every place that logs or reports an action's value (console,
// JSON, Allure) should go through this instead of reading Value directly.
func (a Action) DisplayValue() string {
	if a.Sensitive && a.Value != "" {
		return "••••••"
	}
	return a.Value
}

// MarshalJSON masks Value whenever Sensitive is set, so every JSON-based
// reporter (JSONFile today, anything added later) automatically gets the
// same masking as the console and Allure reporters — there is no separate
// "raw" path a future reporter could accidentally serialize secrets
// through. This only affects encoding; UnmarshalJSON is untouched, so
// scenario files still parse the real value driver-side.
func (a Action) MarshalJSON() ([]byte, error) {
	type alias Action // avoid infinite recursion into this method
	out := alias(a)
	if a.Sensitive {
		out.Value = a.DisplayValue()
	}
	return json.Marshal(out)
}

// EffectiveTimeout returns Timeout if set, otherwise a sane default so
// scenario authors aren't forced to specify it on every wait/assert step.
func (a Action) EffectiveTimeout(defaultTimeout time.Duration) time.Duration {
	if a.Timeout > 0 {
		return a.Timeout.AsTime()
	}
	return defaultTimeout
}
