// Package allure implements domain.Reporter by writing test results in the
// standard Allure "allure-results" format: one <uuid>-result.json file per
// scenario plus <uuid>-attachment.<ext> files for every screenshot/log,
// exactly what `allure generate <dir>` or `allure serve <dir>` expects.
//
// This package intentionally does not render HTML itself — Allure's own
// report generator (the `allure` CLI, or its Docker image / CI plugin) is
// the standard, actively-maintained renderer, and re-implementing it would
// mean maintaining a second copy of Allure's UI. Producing valid results is
// the actual integration point.
package allure

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"uitester/internal/domain"
)

// Reporter writes one Allure result file (plus attachments) per
// ScenarioResult into Dir.
type Reporter struct {
	Dir string
}

func New(dir string) *Reporter {
	return &Reporter{Dir: dir}
}

// allureResult mirrors the subset of Allure's test-result JSON schema this
// reporter populates. Field names/casing follow Allure's spec exactly.
type allureResult struct {
	UUID          string            `json:"uuid"`
	HistoryID     string            `json:"historyId"`
	Name          string            `json:"name"`
	FullName      string            `json:"fullName"`
	Status        string            `json:"status"`
	StatusDetails *allureStatusInfo `json:"statusDetails,omitempty"`
	Stage         string            `json:"stage"`
	Start         int64             `json:"start"`
	Stop          int64             `json:"stop"`
	Labels        []allureLabel     `json:"labels"`
	Steps         []allureStep      `json:"steps"`
	Attachments   []allureRef       `json:"attachments,omitempty"`
}

type allureStatusInfo struct {
	Message string `json:"message,omitempty"`
	Trace   string `json:"trace,omitempty"`
}

type allureLabel struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

type allureParameter struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

type allureRef struct {
	Name   string `json:"name"`
	Source string `json:"source"`
	Type   string `json:"type"`
}

type allureStep struct {
	Name        string            `json:"name"`
	Status      string            `json:"status"`
	Stage       string            `json:"stage"`
	Start       int64             `json:"start"`
	Stop        int64             `json:"stop"`
	Parameters  []allureParameter `json:"parameters,omitempty"`
	Attachments []allureRef       `json:"attachments,omitempty"`
}

func (r *Reporter) Report(ctx context.Context, result domain.ScenarioResult) error {
	if err := os.MkdirAll(r.Dir, 0o755); err != nil {
		return fmt.Errorf("creating allure results dir %s: %w", r.Dir, err)
	}

	uuid := newUUID()
	ar := allureResult{
		UUID:      uuid,
		HistoryID: historyID(result.ScenarioName),
		Name:      result.ScenarioName,
		FullName:  result.ScenarioName,
		Status:    mapStatus(result.Status),
		Stage:     "finished",
		Start:     result.StartedAt.UnixMilli(),
		Stop:      result.StartedAt.Add(result.Duration).UnixMilli(),
		Labels: []allureLabel{
			{Name: "framework", Value: "uitester"},
			{Name: "tool", Value: result.Tool},
			{Name: "suite", Value: result.ScenarioName},
		},
	}

	for _, step := range result.Steps {
		as := allureStep{
			Name:   stepName(step.Action),
			Status: mapStatus(step.Status),
			Stage:  "finished",
			Start:  step.StartedAt.UnixMilli(),
			Stop:   step.StartedAt.Add(step.Duration).UnixMilli(),
		}
		if step.Action.Selector != "" {
			as.Parameters = append(as.Parameters, allureParameter{Name: "selector", Value: step.Action.Selector})
		}
		if step.Action.Value != "" {
			as.Parameters = append(as.Parameters, allureParameter{Name: "value", Value: step.Action.DisplayValue()})
		}
		if step.Output != "" {
			as.Parameters = append(as.Parameters, allureParameter{Name: "output", Value: step.Output})
		}
		if step.Status == domain.StatusFailed && step.Error != "" {
			ar.StatusDetails = &allureStatusInfo{Message: step.Error}
		}

		for _, att := range step.Attachments {
			ref, err := r.copyAttachment(att)
			if err != nil {
				// A missing/unreadable screenshot shouldn't sink the whole
				// report — record what we can and move on.
				continue
			}
			as.Attachments = append(as.Attachments, ref)
		}
		if step.Log != "" {
			if ref, err := r.writeLogAttachment(uuid, step.Log); err == nil {
				as.Attachments = append(as.Attachments, ref)
			}
		}

		ar.Steps = append(ar.Steps, as)
	}

	data, err := json.MarshalIndent(ar, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal allure result: %w", err)
	}
	resultPath := filepath.Join(r.Dir, uuid+"-result.json")
	return os.WriteFile(resultPath, data, 0o644)
}

// copyAttachment copies the attachment's file bytes into the Allure results
// directory under the naming convention Allure requires (<uuid>-attachment.<ext>)
// and returns the {name, source, type} reference the result JSON embeds.
func (r *Reporter) copyAttachment(att domain.Attachment) (allureRef, error) {
	src, err := os.Open(att.Path)
	if err != nil {
		return allureRef{}, err
	}
	defer src.Close()

	ext := filepath.Ext(att.Path)
	if ext == "" {
		ext = extForMime(att.MimeType)
	}
	filename := newUUID() + "-attachment" + ext
	dstPath := filepath.Join(r.Dir, filename)

	dst, err := os.Create(dstPath)
	if err != nil {
		return allureRef{}, err
	}
	defer dst.Close()

	if _, err := io.Copy(dst, src); err != nil {
		return allureRef{}, err
	}

	return allureRef{Name: att.Name, Source: filename, Type: att.MimeType}, nil
}

func (r *Reporter) writeLogAttachment(scenarioUUID, logLine string) (allureRef, error) {
	filename := newUUID() + "-attachment.txt"
	path := filepath.Join(r.Dir, filename)
	if err := os.WriteFile(path, []byte(logLine), 0o644); err != nil {
		return allureRef{}, err
	}
	return allureRef{Name: "log", Source: filename, Type: "text/plain"}, nil
}

func stepName(a domain.Action) string {
	var b strings.Builder
	b.WriteString(string(a.Type))
	if a.Selector != "" {
		fmt.Fprintf(&b, " %s", a.Selector)
	}
	if a.Value != "" {
		fmt.Fprintf(&b, " = %q", a.DisplayValue())
	}
	return b.String()
}

// mapStatus translates domain.Status to Allure's status vocabulary. Allure
// additionally has "broken" (an unexpected exception vs. an assertion
// failure); this reporter doesn't distinguish the two yet, so failures map
// to "failed" uniformly, which Allure renders correctly either way.
func mapStatus(s domain.Status) string {
	switch s {
	case domain.StatusPassed:
		return "passed"
	case domain.StatusFailed:
		return "failed"
	case domain.StatusSkipped:
		return "skipped"
	default:
		return "unknown"
	}
}

// historyId lets Allure track a scenario's pass/fail trend across runs when
// the same allure-results directory (or its history/ subfolder) is reused
// across CI builds. A stable hash of the scenario name is sufficient here.
func historyID(scenarioName string) string {
	sum := sha256.Sum256([]byte(scenarioName))
	return hex.EncodeToString(sum[:])
}

func extForMime(mime string) string {
	switch mime {
	case "image/png":
		return ".png"
	case "image/jpeg":
		return ".jpg"
	case "text/plain":
		return ".txt"
	default:
		return ".bin"
	}
}

var _ domain.Reporter = (*Reporter)(nil)
