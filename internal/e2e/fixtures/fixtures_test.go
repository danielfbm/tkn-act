package fixtures

import "testing"

// ResultsEqual exists so the docker harness (which sees the engine's
// typed []string / map[string]string) and the cluster harness can
// share the same WantResults assertion against fixture-author-written
// []any literals. Lock the normalisation here so a future refactor
// can't quietly tighten or loosen the comparison.
func TestResultsEqualNormalisesArrayShape(t *testing.T) {
	got := map[string]any{"r": []string{"a", "b"}}
	want := map[string]any{"r": []any{"a", "b"}}
	if !ResultsEqual(got, want) {
		t.Errorf("[]string vs []any-of-strings should compare equal")
	}
}

func TestResultsEqualNormalisesObjectShape(t *testing.T) {
	got := map[string]any{"o": map[string]string{"k": "v"}}
	want := map[string]any{"o": map[string]any{"k": "v"}}
	if !ResultsEqual(got, want) {
		t.Errorf("map[string]string vs map[string]any-of-strings should compare equal")
	}
}

// TestFixtureCarriesWantEventFields locks in that the Fixture struct
// exposes a WantEventFields map so both e2e harnesses can assert
// per-event-kind JSON-key/value pairs. The shape is part of the
// cross-backend fidelity contract; if a future refactor renames or
// removes the field, the harnesses go silent — this test keeps that
// from happening unnoticed.
func TestFixtureCarriesWantEventFields(t *testing.T) {
	f := Fixture{
		Dir: "fake", Pipeline: "fake", WantStatus: "succeeded",
		WantEventFields: map[string]map[string]string{
			"run-start": {"display_name": "X", "description": "Y"},
		},
	}
	if got := f.WantEventFields["run-start"]["display_name"]; got != "X" {
		t.Errorf("WantEventFields read-back failed: got %q, want X", got)
	}
	if got := f.WantEventFields["run-start"]["description"]; got != "Y" {
		t.Errorf("WantEventFields read-back failed: got %q, want Y", got)
	}
}

func TestResultsEqualStringsExact(t *testing.T) {
	if !ResultsEqual(
		map[string]any{"a": "x"},
		map[string]any{"a": "x"},
	) {
		t.Errorf("identical string values should compare equal")
	}
	if ResultsEqual(
		map[string]any{"a": "x"},
		map[string]any{"a": "y"},
	) {
		t.Errorf("different string values should not compare equal")
	}
}

func TestResultsEqualMissingKeyFails(t *testing.T) {
	if ResultsEqual(
		map[string]any{"a": "x"},
		map[string]any{"a": "x", "b": "y"},
	) {
		t.Errorf("missing key on the got side should fail comparison")
	}
}

func TestResultsEqualEmptyAndNil(t *testing.T) {
	if !ResultsEqual(nil, nil) {
		t.Errorf("nil vs nil should be equal")
	}
	if !ResultsEqual(nil, map[string]any{}) {
		t.Errorf("nil vs empty should be equal (both mean 'no results')")
	}
	if !ResultsEqual(map[string]any{}, nil) {
		t.Errorf("empty vs nil should be equal")
	}
}

func TestResultsEqualArrayContentsDiffer(t *testing.T) {
	if ResultsEqual(
		map[string]any{"r": []string{"a", "b"}},
		map[string]any{"r": []any{"a", "c"}},
	) {
		t.Errorf("differing array contents should not compare equal")
	}
}

func TestResultsEqualTypeMismatchFails(t *testing.T) {
	// String vs array should not compare equal.
	if ResultsEqual(
		map[string]any{"r": "abc"},
		map[string]any{"r": []any{"a", "b"}},
	) {
		t.Errorf("string vs array should not compare equal")
	}
}
