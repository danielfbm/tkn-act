package main

import (
	"bytes"
	"embed"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"strings"

	"github.com/spf13/cobra"

	"github.com/danielfbm/tkn-act/cmd/tkn-act/internal/agentguide"
	"github.com/danielfbm/tkn-act/internal/exitcode"
)

//go:embed all:agentguide_data
var agentGuideFS embed.FS

const agentGuideDataDir = "agentguide_data"

// AgentGuideContent returns the full embedded user guide as a single
// markdown string — sections concatenated in the curated order with a
// horizontal-rule block between adjacent files.
func AgentGuideContent() string {
	var b bytes.Buffer
	for i, section := range agentguide.Order {
		body, err := readSection(section)
		if err != nil {
			// Embed contract: every section in agentguide.Order is
			// present in the embedded FS (enforced by the generator
			// and by the freshness test). A miss here is a broken
			// build, not a runtime user error.
			panic(fmt.Sprintf("agent-guide: embedded section %q missing: %v", section, err))
		}
		if i > 0 {
			b.WriteString("\n---\n\n")
		}
		b.Write(body)
	}
	return b.String()
}

// AgentGuideSection returns one section's bytes from the embedded FS.
// The alias "overview" maps to README.md.
func AgentGuideSection(section string) (string, error) {
	body, err := readSection(section)
	if err != nil {
		return "", err
	}
	return string(body), nil
}

// AgentGuideSections returns the curated list of section names.
func AgentGuideSections() []string { return agentguide.Sections() }

func readSection(section string) ([]byte, error) {
	path := agentGuideDataDir + "/" + agentguide.FileName(section)
	return fs.ReadFile(agentGuideFS, path)
}

func newAgentGuideCmd() *cobra.Command {
	var list bool
	var section string

	cmd := &cobra.Command{
		Use:   "agent-guide",
		Short: "Print the AI-agent user guide embedded in this binary",
		Long: `Print the embedded user guide (docs/agent-guide/) to stdout. AI
agents that don't have filesystem access to the repo can still discover
how to call tkn-act, the JSON contracts, exit codes, and feature
semantics by running this command.

By default the full guide is printed in a curated order. Use --list to
see section names, and --section <name> to print one section.`,
		Example: `  tkn-act agent-guide | head -40
  tkn-act agent-guide --list
  tkn-act agent-guide --section resolvers
  tkn-act agent-guide > docs/agent-guide-snapshot.md`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			out := cmd.OutOrStdout()

			switch {
			case list:
				return runListSections(out)
			case section != "":
				body, err := AgentGuideSection(section)
				if err != nil {
					return exitcode.Wrap(exitcode.Usage,
						fmt.Errorf("unknown section %q; valid: %s",
							section, strings.Join(AgentGuideSections(), ", ")))
				}
				writeBody(out, body)
				return nil
			default:
				writeBody(out, AgentGuideContent())
				return nil
			}
		},
	}

	cmd.Flags().BoolVar(&list, "list", false, "print one section name per line and exit")
	cmd.Flags().StringVar(&section, "section", "", `print only the named section (e.g. "overview", "resolvers")`)
	cmd.MarkFlagsMutuallyExclusive("list", "section")
	return cmd
}

func runListSections(out io.Writer) error {
	sections := AgentGuideSections()
	if gf.output == "json" {
		payload := struct {
			Sections []string `json:"sections"`
		}{Sections: sections}
		buf, err := json.Marshal(payload)
		if err != nil {
			return err
		}
		if _, err := out.Write(append(buf, '\n')); err != nil {
			return err
		}
		return nil
	}
	for _, s := range sections {
		if _, err := fmt.Fprintln(out, s); err != nil {
			return err
		}
	}
	return nil
}

func writeBody(out io.Writer, body string) {
	fmt.Fprint(out, body)
	if !strings.HasSuffix(body, "\n") {
		fmt.Fprintln(out)
	}
}
