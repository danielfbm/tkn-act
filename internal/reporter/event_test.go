package reporter_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/danielfbm/tkn-act/internal/reporter"
)

func TestDebugEvent_JSONRoundTrip(t *testing.T) {
	e := reporter.Event{
		Kind:      reporter.EvtDebug,
		Component: "resolver",
		Message:   "cache hit",
		Fields:    map[string]any{"ref": "hub://git-clone:0.9", "bytes": float64(4096)},
	}
	b, err := json.Marshal(e)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(b), `"kind":"debug"`) {
		t.Errorf("missing kind in JSON: %s", b)
	}
	if !strings.Contains(string(b), `"component":"resolver"`) {
		t.Errorf("missing component in JSON: %s", b)
	}
	var got reporter.Event
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Kind != reporter.EvtDebug {
		t.Errorf("Kind = %q, want debug", got.Kind)
	}
	if got.Component != "resolver" {
		t.Errorf("Component = %q, want resolver", got.Component)
	}
	if got.Message != "cache hit" {
		t.Errorf("Message = %q, want cache hit", got.Message)
	}
	if got.Fields["ref"] != "hub://git-clone:0.9" {
		t.Errorf("Fields.ref = %v", got.Fields["ref"])
	}
	if got.Fields["bytes"].(float64) != 4096 {
		t.Errorf("Fields.bytes = %v", got.Fields["bytes"])
	}
}

func TestNonDebugEvent_OmitsComponentAndFields(t *testing.T) {
	// Existing event kinds must not carry component/fields keys when
	// they aren't set — they're additive on the JSON contract.
	e := reporter.Event{Kind: reporter.EvtRunStart, Pipeline: "p"}
	b, err := json.Marshal(e)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(b), `"component"`) {
		t.Errorf("unexpected component key: %s", b)
	}
	if strings.Contains(string(b), `"fields"`) {
		t.Errorf("unexpected fields key: %s", b)
	}
}
