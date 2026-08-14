package domain

// Scenario is a named, ordered list of Actions to run against a chosen Tool.
// It is pure data: it knows nothing about chromedp, Selenium, or any transport.
type Scenario struct {
	Name    string   `json:"name"`
	Tool    string   `json:"tool"` // key into the ToolRegistry, e.g. "chrome", "selenium", "appium"
	BaseURL string   `json:"base_url,omitempty"`
	Steps   []Action `json:"steps"`
}

// Validate performs cheap, dependency-free sanity checks before a Scenario
// is handed to the runner, so config mistakes fail fast with a clear message.
func (s Scenario) Validate() error {
	if s.Name == "" {
		return errScenario("scenario name is required")
	}
	if s.Tool == "" {
		return errScenario("scenario %q: tool is required", s.Name)
	}
	if len(s.Steps) == 0 {
		return errScenario("scenario %q: at least one step is required", s.Name)
	}
	for i, step := range s.Steps {
		if step.Type == "" {
			return errScenario("scenario %q: step %d: type is required", s.Name, i)
		}
	}
	return nil
}
