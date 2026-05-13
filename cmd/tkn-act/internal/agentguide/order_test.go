package agentguide

import "testing"

func TestOrderHasOverviewFirst(t *testing.T) {
	if len(Order) == 0 || Order[0] != "overview" {
		t.Fatalf("Order[0] must be \"overview\" (README.md); got %v", Order)
	}
}

func TestOrderNoDuplicates(t *testing.T) {
	seen := map[string]bool{}
	for _, s := range Order {
		if seen[s] {
			t.Fatalf("Order has duplicate section %q", s)
		}
		seen[s] = true
	}
}

func TestFileNameOverviewAlias(t *testing.T) {
	if got := FileName("overview"); got != "README.md" {
		t.Errorf("FileName(\"overview\") = %q; want \"README.md\"", got)
	}
}

func TestFileNameRegular(t *testing.T) {
	cases := map[string]string{
		"matrix":           "matrix.md",
		"step-template":    "step-template.md",
		"pipeline-results": "pipeline-results.md",
	}
	for in, want := range cases {
		if got := FileName(in); got != want {
			t.Errorf("FileName(%q) = %q; want %q", in, got, want)
		}
	}
}

func TestSectionsReturnsCopy(t *testing.T) {
	s := Sections()
	if len(s) != len(Order) {
		t.Fatalf("Sections() len = %d; want %d", len(s), len(Order))
	}
	s[0] = "MUTATED"
	if Order[0] == "MUTATED" {
		t.Error("Sections() must return a copy; mutating it changed Order")
	}
}
