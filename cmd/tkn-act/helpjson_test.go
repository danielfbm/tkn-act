package main

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestHelpJSONShape(t *testing.T) {
	root := newRootCmd()
	out := buildHelpJSON(root)

	if out.Name != "tkn-act" {
		t.Fatalf("Name = %q", out.Name)
	}
	if len(out.ExitCodes) < 5 {
		t.Fatalf("ExitCodes too short: %d", len(out.ExitCodes))
	}
	// global flags must include the documented ones
	got := map[string]bool{}
	for _, f := range out.GlobalFlags {
		got[f.Name] = true
	}
	for _, want := range []string{
		"output", "debug", "cleanup", "max-parallel", "cluster",
		"no-color", "color", "quiet", "verbose",
		"configmap-dir", "secret-dir", "configmap", "secret",
		// Resolver scaffolding (Track 1 #9 Phase 1).
		"resolver-cache-dir", "resolver-allow", "resolver-config",
		"offline", "remote-resolver-context", "resolver-allow-insecure-http",
		// Sidecar pacing.
		"sidecar-start-grace", "sidecar-stop-grace",
	} {
		if !got[want] {
			t.Errorf("missing global flag %q", want)
		}
	}

	// every command we ship must be present
	want := []string{
		"tkn-act run",
		"tkn-act list",
		"tkn-act validate",
		"tkn-act version",
		"tkn-act doctor",
		"tkn-act help-json",
		"tkn-act agent-guide",
		"tkn-act cluster up",
		"tkn-act cluster down",
		"tkn-act cluster status",
	}
	have := map[string]commandInfo{}
	for _, c := range out.Commands {
		have[c.Path] = c
	}
	for _, p := range want {
		if _, ok := have[p]; !ok {
			t.Errorf("missing command %q in help-json", p)
		}
	}

	// run, doctor, validate, list, version, agent-guide, help-json must each have at least one Example
	for _, p := range []string{
		"tkn-act run", "tkn-act doctor", "tkn-act validate",
		"tkn-act list", "tkn-act version", "tkn-act agent-guide", "tkn-act help-json",
	} {
		if len(have[p].Examples) == 0 {
			t.Errorf("%q has no examples", p)
		}
	}

	// ensure JSON encoding doesn't panic and is non-empty
	b, err := json.Marshal(out)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(b), `"tkn-act run"`) {
		t.Fatalf("encoded JSON missing run path; got:\n%s", string(b))
	}
}

func TestHelpJSONDoesNotIncludeRoot(t *testing.T) {
	root := newRootCmd()
	out := buildHelpJSON(root)
	for _, c := range out.Commands {
		if c.Path == "tkn-act" {
			t.Fatalf("root command should not be in commands list")
		}
	}
}
