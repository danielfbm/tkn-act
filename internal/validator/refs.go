package validator

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/danielfbm/tkn-act/internal/tektontypes"
)

// allowedVarPrefixes mirrors Tekton v1.14+'s validateVarSubstitutionExpressions
// prefix set for PipelineTask params / when / matrix values
// (pkg/apis/pipeline/v1/pipeline_validation.go).
var allowedVarPrefixes = []string{"params", "tasks", "finally", "context", "workspaces", "results", "tt.params"}

// varExprPat matches a $(...) expression without nested parentheses.
// Shell arithmetic `$((...))` never matches (the char after `$(` is
// `(`), so it is not flagged.
var varExprPat = regexp.MustCompile(`\$\(([^()]*)\)`)

// extractVarExprs returns the inner text of every $(...) in s.
func extractVarExprs(s string) []string {
	matches := varExprPat.FindAllStringSubmatch(s, -1)
	out := make([]string, 0, len(matches))
	for _, m := range matches {
		out = append(out, m[1])
	}
	return out
}

// hasAllowedVarPrefix reports whether expr starts with one of
// allowedVarPrefixes followed by `.` or `[` (or is exactly the prefix;
// tt.params needs a following segment, as upstream).
func hasAllowedVarPrefix(expr string) bool {
	expr = strings.TrimSpace(expr)
	for _, p := range allowedVarPrefixes {
		if expr == p && p != "tt.params" {
			return true
		}
		if strings.HasPrefix(expr, p+".") && len(expr) > len(p)+1 {
			return true
		}
		if strings.HasPrefix(expr, p+"[") {
			return true
		}
	}
	return false
}

// paramRefPat matches $(params.NAME...), $(params["NAME"]...) and
// $(params['NAME']...). Group 1 is the dotted form's name (first
// segment only — object keys / [*] / [N] may follow), groups 2/3 the
// bracketed forms.
var paramRefPat = regexp.MustCompile(`\$\(\s*params(?:\.([A-Za-z0-9_-]+)|\["([^"]+)"\]|\['([^']+)'\])`)

// extractParamRefs returns every param name referenced in s.
func extractParamRefs(s string) []string {
	matches := paramRefPat.FindAllStringSubmatch(s, -1)
	out := make([]string, 0, len(matches))
	for _, m := range matches {
		for _, g := range m[1:] {
			if g != "" {
				out = append(out, g)
				break
			}
		}
	}
	return out
}

// forEachWhenAndMatrixString calls fn for every string in pt's when
// expressions (input + values) and matrix (params + include params).
func forEachWhenAndMatrixString(pt tektontypes.PipelineTask, fn func(field, s string)) {
	for i, w := range pt.When {
		field := fmt.Sprintf("when[%d]", i)
		fn(field, w.Input)
		for _, v := range w.Values {
			fn(field, v)
		}
	}
	if pt.Matrix == nil {
		return
	}
	for _, mp := range pt.Matrix.Params {
		for _, v := range mp.Value {
			fn("matrix.params."+mp.Name, v)
		}
	}
	for i, inc := range pt.Matrix.Include {
		for _, p := range inc.Params {
			field := fmt.Sprintf("matrix.include[%d].params.%s", i, p.Name)
			collectStrings(p.Value, func(s string) { fn(field, s) })
		}
	}
}

// forEachPipelineTaskString calls fn for every PipelineTask-level
// string Tekton substitutes: params, when, matrix.
func forEachPipelineTaskString(pt tektontypes.PipelineTask, fn func(field, s string)) {
	for _, p := range pt.Params {
		field := "params." + p.Name
		collectStrings(p.Value, func(s string) { fn(field, s) })
	}
	forEachWhenAndMatrixString(pt, fn)
}

// forEachTaskSpecString calls fn for every string inside a TaskSpec
// that Tekton substitutes $(params.X) into: step / sidecar /
// stepTemplate image, command, args, script, env values, workingDir,
// and step StepAction params.
func forEachTaskSpecString(spec tektontypes.TaskSpec, fn func(field, s string)) {
	each := func(field string, ss []string) {
		for _, s := range ss {
			fn(field, s)
		}
	}
	for _, st := range spec.Steps {
		field := fmt.Sprintf("step %q", st.Name)
		fn(field, st.Image)
		fn(field, st.Script)
		fn(field, st.WorkingDir)
		each(field, st.Command)
		each(field, st.Args)
		for _, e := range st.Env {
			fn(field, e.Value)
		}
		for _, p := range st.Params {
			collectStrings(p.Value, func(s string) { fn(field, s) })
		}
	}
	for _, sc := range spec.Sidecars {
		field := fmt.Sprintf("sidecar %q", sc.Name)
		fn(field, sc.Image)
		fn(field, sc.Script)
		fn(field, sc.WorkingDir)
		each(field, sc.Command)
		each(field, sc.Args)
		for _, e := range sc.Env {
			fn(field, e.Value)
		}
	}
	if t := spec.StepTemplate; t != nil {
		const field = "stepTemplate"
		fn(field, t.Image)
		fn(field, t.WorkingDir)
		each(field, t.Command)
		each(field, t.Args)
		for _, e := range t.Env {
			fn(field, e.Value)
		}
	}
}

// duplicateStepNameErrors reports non-empty step names that occur more
// than once in spec (Tekton requires unique step names per Task).
func duplicateStepNameErrors(subject string, spec tektontypes.TaskSpec) []error {
	var errs []error
	seen := map[string]bool{}
	for _, st := range spec.Steps {
		if st.Name == "" {
			continue
		}
		if seen[st.Name] {
			errs = append(errs, fmt.Errorf("%s has duplicate step name %q (step names must be unique within a Task)", subject, st.Name))
		}
		seen[st.Name] = true
	}
	return errs
}
