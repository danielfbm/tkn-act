// Package discovery finds Tekton YAML files in a project directory using a
// fixed priority order: pipelinerun.yaml, pipeline.yaml, .tekton/*, tekton/*.
package discovery

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// ErrNoneFound is returned (wrapped) by Find when the directory contains no
// Tekton YAML. Callers that treat "nothing here" as a valid empty result
// (e.g. `list`) detect it with errors.Is; callers that need a pipeline to act
// on (`run`, `validate`) surface it as a usage error.
var ErrNoneFound = errors.New("no tekton YAML found")

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
		return nil, fmt.Errorf("%w in %s (looked for pipeline.yaml, pipelinerun.yaml, .tekton/, tekton/)", ErrNoneFound, dir)
	}
	return out, nil
}
