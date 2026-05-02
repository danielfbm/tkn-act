package main

import (
	_ "embed"
	"fmt"

	"github.com/spf13/cobra"
)

//go:embed agentguide_data.md
var agentGuide string

// AgentGuideContent returns the embedded AGENTS.md content. Exported via this
// helper so tests can assert on it without re-reading the file from disk.
func AgentGuideContent() string { return agentGuide }

func newAgentGuideCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "agent-guide",
		Short: "Print the AI-agent guide (AGENTS.md) embedded in this binary",
		Long: `Print the embedded AGENTS.md to stdout. AI agents that don't have
filesystem access to the repo can still discover the conventions tkn-act
follows by running this command.`,
		Example: `  tkn-act agent-guide | head -40
  tkn-act agent-guide > AGENTS.md`,
		RunE: func(_ *cobra.Command, _ []string) error {
			fmt.Print(agentGuide)
			if len(agentGuide) == 0 || agentGuide[len(agentGuide)-1] != '\n' {
				fmt.Println()
			}
			return nil
		},
	}
}
