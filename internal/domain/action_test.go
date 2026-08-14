package domain_test

import (
	"encoding/json"
	"strings"
	"testing"

	"uitester/internal/domain"
)

func TestAction_MarshalJSON_MasksSensitiveValue(t *testing.T) {
	a := domain.Action{Type: domain.ActionInput, Selector: "#password", Value: "hunter2", Sensitive: true}

	data, err := json.Marshal(a)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if strings.Contains(string(data), "hunter2") {
		t.Fatalf("sensitive value leaked into marshaled JSON: %s", data)
	}

	// Sanity check the non-sensitive path still round-trips the real value.
	b := domain.Action{Type: domain.ActionInput, Selector: "#username", Value: "demo-user"}
	data2, err := json.Marshal(b)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if !strings.Contains(string(data2), "demo-user") {
		t.Fatalf("expected non-sensitive value to be present in JSON, got: %s", data2)
	}
}

func TestAction_UnmarshalJSON_StillReadsRealValue(t *testing.T) {
	raw := `{"type":"input","selector":"#password","value":"hunter2","sensitive":true}`
	var a domain.Action
	if err := json.Unmarshal([]byte(raw), &a); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if a.Value != "hunter2" {
		t.Fatalf("expected driver-facing Value to be the real secret, got %q", a.Value)
	}
	if a.DisplayValue() != "••••••" {
		t.Fatalf("expected masked DisplayValue, got %q", a.DisplayValue())
	}
}
