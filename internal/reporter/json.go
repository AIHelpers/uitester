package reporter

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"uitester/internal/domain"
)

// JSONFile writes each ScenarioResult as a pretty-printed JSON file, one
// file per run, handy for CI artifact upload or dashboards.
type JSONFile struct {
	Path string
}

func NewJSONFile(path string) *JSONFile {
	return &JSONFile{Path: path}
}

func (j *JSONFile) Report(ctx context.Context, result domain.ScenarioResult) error {
	data, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal result: %w", err)
	}
	if dir := filepath.Dir(j.Path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("creating dir for %s: %w", j.Path, err)
		}
	}
	if err := os.WriteFile(j.Path, data, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", j.Path, err)
	}
	return nil
}

var _ domain.Reporter = (*JSONFile)(nil)
