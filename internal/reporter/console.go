// Package reporter contains domain.Reporter implementations: things that
// consume a finished ScenarioResult (print it, persist it, ship it to CI).
package reporter

import (
	"context"
	"fmt"
	"io"

	"uitester/internal/domain"
)

// Console prints a human-readable pass/fail summary, suitable for local runs
// and CI logs.
type Console struct {
	Out io.Writer
}

func NewConsole(out io.Writer) *Console {
	return &Console{Out: out}
}

func (c *Console) Report(ctx context.Context, result domain.ScenarioResult) error {
	icon := "✅"
	if result.Failed() {
		icon = "❌"
	}
	fmt.Fprintf(c.Out, "\n%s Scenario %q (tool: %s) — %s in %s\n", icon, result.ScenarioName, result.Tool, result.Status, result.Duration.Round(1_000_000))
	for i, step := range result.Steps {
		stepIcon := "✔"
		if step.Status == domain.StatusFailed {
			stepIcon = "✘"
		}
		fmt.Fprintf(c.Out, "  %s [%d] %-14s selector=%-25s %s\n", stepIcon, i+1, step.Action.Type, quoteOrDash(step.Action.Selector), step.Duration.Round(1_000_000))
		if step.Action.Value != "" {
			fmt.Fprintf(c.Out, "      value: %s\n", step.Action.DisplayValue())
		}
		if step.Error != "" {
			fmt.Fprintf(c.Out, "      error: %s\n", step.Error)
		}
		if step.Output != "" {
			fmt.Fprintf(c.Out, "      output: %s\n", step.Output)
		}
		for _, att := range step.Attachments {
			fmt.Fprintf(c.Out, "      attachment: %s (%s)\n", att.Path, att.MimeType)
		}
	}
	return nil
}

func quoteOrDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

var _ domain.Reporter = (*Console)(nil)
