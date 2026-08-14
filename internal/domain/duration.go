package domain

import (
	"encoding/json"
	"fmt"
	"time"
)

// Duration wraps time.Duration so scenario JSON can write human-friendly
// values like "10s" or "1m30s" instead of raw nanosecond integers, without
// pulling in a YAML/TOML library just for that convenience.
type Duration time.Duration

func (d Duration) AsTime() time.Duration { return time.Duration(d) }

func (d Duration) MarshalJSON() ([]byte, error) {
	return json.Marshal(time.Duration(d).String())
}

func (d *Duration) UnmarshalJSON(data []byte) error {
	var raw interface{}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	switch v := raw.(type) {
	case string:
		if v == "" {
			*d = 0
			return nil
		}
		parsed, err := time.ParseDuration(v)
		if err != nil {
			return fmt.Errorf("invalid duration %q: %w", v, err)
		}
		*d = Duration(parsed)
		return nil
	case float64:
		// bare numbers are treated as seconds for convenience
		*d = Duration(time.Duration(v) * time.Second)
		return nil
	default:
		return fmt.Errorf("invalid duration value: %v", raw)
	}
}
