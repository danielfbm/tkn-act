// Package discovery finds Tekton YAML files in a project directory using a
// fixed priority order: pipelinerun.yaml, pipeline.yaml, .tekton/*, tekton/*.
package discovery

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Find returns YAML files in dir that look like Tekton resources, in
// deterministic order. Returns an error if nothing is found.
func Find(dir string) ([]string, error) {
	var out []string

	for _, name := range []string{"pipelinerun.yaml", "pipeline.yaml"} {
		p := filepath.Join(dir, name)
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			out = append(out, p)
		}
	}
	for _, sub := range []string{".tekton", "tekton"} {
		subdir := filepath.Join(dir, sub)
		if st, err := os.Stat(subdir); err != nil || !st.IsDir() {
			continue
		}
		ents, err := os.ReadDir(subdir)
		if err != nil {
			return nil, err
		}
		var found []string
		for _, e := range ents {
			if e.IsDir() {
				continue
			}
			n := e.Name()
			if strings.HasSuffix(n, ".yaml") || strings.HasSuffix(n, ".yml") {
				found = append(found, filepath.Join(subdir, n))
			}
		}
		sort.Strings(found)
		out = append(out, found...)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no tekton YAML found in %s (looked for pipeline.yaml, pipelinerun.yaml, .tekton/, tekton/)", dir)
	}
	return out, nil
}
