// Package resolver performs Tekton-style variable substitution:
//   $(params.name)          – scalar param
//   $(params.obj.key)       – object param key
//   $(params.arr[*])        – array param expanded into multiple args (only via SubstituteArgs)
//   $(tasks.X.results.Y)    – named result from a previously-executed task
//   $(context.taskRun.name) – synthesized context var
//
// The double-dollar literal `$$` produces a single `$` and is not interpreted.
package resolver

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// errStepRefDeferred signals to SubstituteAllowStepRefs that a placeholder
// should be left intact (because the docker backend will resolve it per-step
// later). Other resolution errors still bubble out.
var errStepRefDeferred = errors.New("step ref deferred")

type Context struct {
	Params       map[string]string            // string params
	ArrayParams  map[string][]string          // array params (only used by SubstituteArgs for [*] expansion)
	ObjectParams map[string]map[string]string // object params
	Results      map[string]map[string]string // task name → result name → value
	ContextVars  map[string]string            // dotted name → value (without "context." prefix)
	// StepResults are the results produced by earlier steps within the same
	// Task. Populated by the docker backend right before each step launches;
	// nil during the engine's task-level substitution pass.
	StepResults map[string]map[string]string
	// CurrentStep is the step being resolved; required to resolve
	// $(step.results.<name>.path) to /tekton/steps/<step>/results/<name>.
	CurrentStep string
}

// First char allows digits to align with RFC 1123 names (Tekton accepts
// e.g. `1stcheckout` as a PipelineTask name, so $(tasks.1stcheckout...)
// must match). The remaining class already covers the dotted/bracketed
// reference grammar; \w includes digits.
var refPat = regexp.MustCompile(`\$\(([a-zA-Z0-9][\w.\[\]\*-]*)\)`)

// Substitute replaces $(...) references in s using ctx. Returns an error for
// unknown references.
func Substitute(s string, ctx Context) (string, error) {
	// Handle $$ escape first by replacing with a sentinel that contains no $,
	// then restoring after substitution.
	const sentinel = "\x00DOLLAR\x00"
	s = strings.ReplaceAll(s, "$$", sentinel)

	var firstErr error
	out := refPat.ReplaceAllStringFunc(s, func(m string) string {
		if firstErr != nil {
			return m
		}
		key := refPat.FindStringSubmatch(m)[1]
		v, err := lookup(key, ctx)
		if err != nil {
			firstErr = err
			return m
		}
		return v
	})
	if firstErr != nil {
		return "", firstErr
	}
	return strings.ReplaceAll(out, sentinel, "$"), nil
}

// SubstituteAllowStepRefs is like Substitute but leaves $(step.results.X.path)
// and $(steps.<step>.results.<name>) placeholders intact when the context
// hasn't populated StepResults / CurrentStep. Used by the engine's task-level
// pass; per-step substitution happens later in the docker backend.
func SubstituteAllowStepRefs(s string, ctx Context) (string, error) {
	const sentinel = "\x00DOLLAR\x00"
	s = strings.ReplaceAll(s, "$$", sentinel)

	var firstErr error
	out := refPat.ReplaceAllStringFunc(s, func(m string) string {
		if firstErr != nil {
			return m
		}
		key := refPat.FindStringSubmatch(m)[1]
		v, err := lookup(key, ctx)
		if err != nil {
			if errors.Is(err, errStepRefDeferred) {
				return m // leave placeholder for the docker backend
			}
			firstErr = err
			return m
		}
		return v
	})
	if firstErr != nil {
		return "", firstErr
	}
	return strings.ReplaceAll(out, sentinel, "$"), nil
}

// SubstituteArgs is like Substitute but operates on a []string and supports
// $(params.x[*]) — the entire arg is replaced by the array's elements.
func SubstituteArgs(args []string, ctx Context) ([]string, error) {
	var out []string
	for _, a := range args {
		if isArrayStarRef(a) {
			name := strings.TrimSuffix(strings.TrimPrefix(a, "$(params."), "[*])")
			vals, ok := ctx.ArrayParams[name]
			if !ok {
				return nil, fmt.Errorf("unknown array param %q", name)
			}
			out = append(out, vals...)
			continue
		}
		s, err := Substitute(a, ctx)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, nil
}

// SubstituteArgsAllowStepRefs is the AllowStepRefs counterpart of SubstituteArgs.
func SubstituteArgsAllowStepRefs(args []string, ctx Context) ([]string, error) {
	var out []string
	for _, a := range args {
		if isArrayStarRef(a) {
			name := strings.TrimSuffix(strings.TrimPrefix(a, "$(params."), "[*])")
			vals, ok := ctx.ArrayParams[name]
			if !ok {
				return nil, fmt.Errorf("unknown array param %q", name)
			}
			out = append(out, vals...)
			continue
		}
		s, err := SubstituteAllowStepRefs(a, ctx)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, nil
}

func isArrayStarRef(s string) bool {
	return strings.HasPrefix(s, "$(params.") && strings.HasSuffix(s, "[*])")
}

func lookup(key string, ctx Context) (string, error) {
	switch {
	case strings.HasPrefix(key, "params."):
		rest := strings.TrimPrefix(key, "params.")
		if dot := strings.Index(rest, "."); dot >= 0 {
			obj := rest[:dot]
			k := rest[dot+1:]
			if om, ok := ctx.ObjectParams[obj]; ok {
				if v, ok := om[k]; ok {
					return v, nil
				}
				return "", fmt.Errorf("object param %q has no key %q", obj, k)
			}
			return "", fmt.Errorf("unknown object param %q", obj)
		}
		if v, ok := ctx.Params[rest]; ok {
			return v, nil
		}
		return "", fmt.Errorf("unknown param %q", rest)
	case strings.HasPrefix(key, "tasks."):
		// tasks.<task>.results.<name>
		parts := strings.SplitN(key, ".", 4)
		if len(parts) != 4 || parts[2] != "results" {
			return "", fmt.Errorf("malformed task ref %q", key)
		}
		task, name := parts[1], parts[3]
		if rs, ok := ctx.Results[task]; ok {
			if v, ok := rs[name]; ok {
				return v, nil
			}
			return "", fmt.Errorf("task %q has no result %q", task, name)
		}
		return "", fmt.Errorf("no results for task %q", task)
	case strings.HasPrefix(key, "context."):
		rest := strings.TrimPrefix(key, "context.")
		if v, ok := ctx.ContextVars[rest]; ok {
			return v, nil
		}
		return "", fmt.Errorf("unknown context var %q", rest)
	case strings.HasPrefix(key, "results."):
		rest := strings.TrimPrefix(key, "results.")
		if strings.HasSuffix(rest, ".path") {
			name := strings.TrimSuffix(rest, ".path")
			return "/tekton/results/" + name, nil
		}
		return "", fmt.Errorf("unknown results ref %q", key)
	case strings.HasPrefix(key, "step.results."):
		// $(step.results.<name>.path) -> /tekton/steps/<current-step>/results/<name>
		rest := strings.TrimPrefix(key, "step.results.")
		if !strings.HasSuffix(rest, ".path") {
			return "", fmt.Errorf("unknown step.results ref %q", key)
		}
		if ctx.CurrentStep == "" {
			return "", errStepRefDeferred
		}
		name := strings.TrimSuffix(rest, ".path")
		return "/tekton/steps/" + ctx.CurrentStep + "/results/" + name, nil
	case strings.HasPrefix(key, "steps."):
		// $(steps.<step>.results.<name>) -> literal value of an earlier step's result
		parts := strings.SplitN(key, ".", 4)
		if len(parts) != 4 || parts[2] != "results" {
			return "", fmt.Errorf("malformed steps ref %q", key)
		}
		stepName, resName := parts[1], parts[3]
		if ctx.StepResults == nil {
			return "", errStepRefDeferred
		}
		if rs, ok := ctx.StepResults[stepName]; ok {
			if v, ok := rs[resName]; ok {
				return v, nil
			}
			return "", fmt.Errorf("step %q has no result %q", stepName, resName)
		}
		return "", fmt.Errorf("no results for step %q (must be a previous step in the same Task)", stepName)
	case strings.HasPrefix(key, "workspaces."):
		rest := strings.TrimPrefix(key, "workspaces.")
		if strings.HasSuffix(rest, ".path") {
			name := strings.TrimSuffix(rest, ".path")
			return "/workspace/" + name, nil
		}
		return "", fmt.Errorf("unknown workspaces ref %q", key)
	default:
		return "", fmt.Errorf("unknown reference $(%s)", key)
	}
}
