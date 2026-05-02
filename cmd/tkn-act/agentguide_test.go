package main

import (
	"strings"
	"testing"
)

func TestAgentGuideEmbedded(t *testing.T) {
	g := AgentGuideContent()
	if len(g) < 500 {
		t.Fatalf("agent guide is suspiciously short: %d bytes", len(g))
	}
	for _, must := range []string{
		"Exit codes",
		"tkn-act doctor",
		"--output json",
		"help-json",
	} {
		if !strings.Contains(g, must) {
			t.Errorf("agent guide missing %q", must)
		}
	}
}
