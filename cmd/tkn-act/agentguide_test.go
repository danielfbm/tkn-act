package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/danielfbm/tkn-act/cmd/tkn-act/internal/agentguide"
)

func TestAgentGuideEmbedded(t *testing.T) {
	g := AgentGuideContent()
	if len(g) < 500 {
		t.Fatalf("agent guide is suspiciously short: %d bytes", len(g))
	}
	for _, must := range []string{
		// Overview canon: pre-existing assertions kept verbatim so the
		// JSON-contract / exit-code / doctor / help-json strings every
		// downstream consumer relies on stay reachable from the embedded
		// blob.
		"Exit codes",
		"tkn-act doctor",
		"--output json",
		"help-json",
		// One characteristic string per per-feature file (defence
		// against an empty-tree regen that happens to match total byte
		// count of the previous blob).
		"`stepTemplate` (DRY for Steps)",
		"Sidecars (`Task.sidecars`)",
		"StepActions (`tekton.dev/v1beta1`)",
		"Matrix fan-out (`PipelineTask.matrix`)",
		"Pipeline results (`Pipeline.spec.results`)",
		"`displayName` / `description`",
		"Timeout disambiguation",
		"Resolvers (Track 1 #9, shipped)",
	} {
		if !strings.Contains(g, must) {
			t.Errorf("agent guide missing %q", must)
		}
	}
}

func TestAgentGuideSectionsCoverOrder(t *testing.T) {
	got := AgentGuideSections()
	if len(got) != len(agentguide.Order) {
		t.Fatalf("AgentGuideSections() len=%d, agentguide.Order len=%d", len(got), len(agentguide.Order))
	}
	for i, want := range agentguide.Order {
		if got[i] != want {
			t.Errorf("section[%d] = %q; want %q", i, got[i], want)
		}
	}
}

func TestAgentGuideSectionReadEach(t *testing.T) {
	for _, section := range AgentGuideSections() {
		body, err := AgentGuideSection(section)
		if err != nil {
			t.Errorf("section %q unreadable: %v", section, err)
			continue
		}
		if len(body) == 0 {
			t.Errorf("section %q is empty", section)
		}
		if !strings.HasSuffix(body, "\n") {
			t.Errorf("section %q does not end with a newline", section)
		}
	}
}

func TestAgentGuideSectionUnknown(t *testing.T) {
	_, err := AgentGuideSection("does-not-exist")
	if err == nil {
		t.Fatal("expected error for unknown section; got nil")
	}
}

func TestAgentGuideCmdDefault(t *testing.T) {
	cmd := newAgentGuideCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetArgs(nil)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !strings.Contains(buf.String(), "## What `tkn-act` is") {
		t.Error("default output missing overview content")
	}
	if !strings.Contains(buf.String(), "## Resolvers (Track 1 #9, shipped)") {
		t.Error("default output missing resolvers section")
	}
}

func TestAgentGuideCmdList(t *testing.T) {
	cmd := newAgentGuideCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetArgs([]string{"--list"})
	prev := gf.output
	gf.output = "pretty"
	defer func() { gf.output = prev }()
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}
	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	if len(lines) != len(agentguide.Order) {
		t.Fatalf("--list emitted %d lines; want %d", len(lines), len(agentguide.Order))
	}
	for i, line := range lines {
		if line != agentguide.Order[i] {
			t.Errorf("line %d = %q; want %q", i, line, agentguide.Order[i])
		}
	}
}

func TestAgentGuideCmdListJSON(t *testing.T) {
	cmd := newAgentGuideCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetArgs([]string{"--list"})
	prev := gf.output
	gf.output = "json"
	defer func() { gf.output = prev }()
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}
	var payload struct {
		Sections []string `json:"sections"`
	}
	if err := json.Unmarshal(buf.Bytes(), &payload); err != nil {
		t.Fatalf("unmarshal: %v (raw: %s)", err, buf.String())
	}
	if len(payload.Sections) != len(agentguide.Order) {
		t.Fatalf("got %d sections; want %d", len(payload.Sections), len(agentguide.Order))
	}
}

func TestAgentGuideCmdSection(t *testing.T) {
	cmd := newAgentGuideCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetArgs([]string{"--section", "resolvers"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !strings.Contains(buf.String(), "## Resolvers (Track 1 #9, shipped)") {
		t.Errorf("--section resolvers should print the resolvers heading; got: %s", buf.String()[:min(200, len(buf.String()))])
	}
	if strings.Contains(buf.String(), "## Matrix fan-out") {
		t.Errorf("--section resolvers should not include other sections")
	}
}

func TestAgentGuideCmdSectionOverviewAlias(t *testing.T) {
	cmd := newAgentGuideCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetArgs([]string{"--section", "overview"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !strings.Contains(buf.String(), "## What `tkn-act` is") {
		t.Error("--section overview should map to README.md content")
	}
}

func TestAgentGuideCmdSectionUnknown(t *testing.T) {
	cmd := newAgentGuideCmd()
	var buf, ebuf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&ebuf)
	cmd.SetArgs([]string{"--section", "no-such-thing"})
	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected error for unknown section; got nil")
	}
	if !strings.Contains(err.Error(), "no-such-thing") {
		t.Errorf("error should mention the bad name; got: %v", err)
	}
}
