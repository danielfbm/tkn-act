package validator_test

import (
	"reflect"
	"strings"
	"testing"

	"github.com/danielfbm/tkn-act/internal/loader"
	"github.com/danielfbm/tkn-act/internal/tektontypes"
	"github.com/danielfbm/tkn-act/internal/validator"
)

const auditTask = `
apiVersion: tekton.dev/v1
kind: Task
metadata: {name: t}
spec:
  params: [{name: x, default: d}]
  results: [{name: r}]
  steps: [{name: s, image: alpine, script: 'echo $(params.x) $(date) $((1+2))'}]
`

func auditLoad(t *testing.T, y string) *loader.Bundle {
	t.Helper()
	b, err := loader.LoadBytes([]byte(y))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func errsContain(errs []error, sub string) bool {
	for _, e := range errs {
		if strings.Contains(e.Error(), sub) {
			return true
		}
	}
	return false
}

func TestAuditE_ValidBaselineHasNoErrors(t *testing.T) {
	b := auditLoad(t, auditTask+`---
apiVersion: tekton.dev/v1
kind: Pipeline
metadata: {name: p}
spec:
  params: [{name: rev, default: main}]
  tasks:
    - name: a
      taskRef: {name: t}
      params: [{name: x, value: "$(params.rev) $(context.pipelineRun.name)"}]
    - name: b
      taskRef: {name: t}
      params: [{name: x, value: "$(tasks.a.results.r)"}]
      when: [{input: "$(tasks.a.results.r)", operator: in, values: ["$(params.rev)"]}]
  finally:
    - name: f
      taskRef: {name: t}
      params: [{name: x, value: "$(tasks.a.results.r) $(tasks.status) $(finally.g.results.r)"}]
    - name: g
      taskSpec:
        steps: [{name: s, image: alpine, script: 'echo $(params.rev)'}]
`)
	if errs := validator.Validate(b, "p", map[string]bool{}); len(errs) != 0 {
		t.Fatalf("want no errors, got %v", errs)
	}
}

func TestAuditE_MissingRequiredPipelineParam(t *testing.T) {
	b := auditLoad(t, auditTask+`---
apiVersion: tekton.dev/v1
kind: Pipeline
metadata: {name: p}
spec:
  params: [{name: rev}, {name: opt, default: x}]
  tasks: [{name: a, taskRef: {name: t}, params: [{name: x, value: $(params.rev)}]}]
`)
	errs := validator.Validate(b, "p", map[string]bool{})
	if !errsContain(errs, `missing required param "rev"`) {
		t.Fatalf("want missing required param error, got %v", errs)
	}
	if errsContain(errs, `"opt"`) {
		t.Errorf("param with default must not be required: %v", errs)
	}
	if errs := validator.Validate(b, "p", map[string]bool{"rev": true}); len(errs) != 0 {
		t.Errorf("provided param: want no errors, got %v", errs)
	}
	// nil providedParams = unknown (validate without --param): skip.
	if errs := validator.Validate(b, "p", nil); len(errs) != 0 {
		t.Errorf("nil providedParams: want no errors, got %v", errs)
	}
}

func TestAuditE_MainTaskConsumingFinallyResult(t *testing.T) {
	b := auditLoad(t, auditTask+`---
apiVersion: tekton.dev/v1
kind: Pipeline
metadata: {name: p}
spec:
  tasks: [{name: a, taskRef: {name: t}, params: [{name: x, value: $(tasks.f.results.r)}]}]
  finally: [{name: f, taskRef: {name: t}}]
`)
	errs := validator.Validate(b, "p", nil)
	if !errsContain(errs, `references result of finally task "f"`) {
		t.Fatalf("want finally-result error, got %v", errs)
	}
}

func TestAuditE_WhenInputUnknownTask(t *testing.T) {
	b := auditLoad(t, auditTask+`---
apiVersion: tekton.dev/v1
kind: Pipeline
metadata: {name: p}
spec:
  tasks:
    - name: a
      taskRef: {name: t}
      when: [{input: "$(tasks.ghost.results.r)", operator: in, values: [x]}]
`)
	errs := validator.Validate(b, "p", nil)
	if !errsContain(errs, `when[0] references unknown task "ghost"`) {
		t.Fatalf("want unknown-task error, got %v", errs)
	}
}

func TestAuditE_MatrixUnknownTaskRef(t *testing.T) {
	b := auditLoad(t, auditTask+`---
apiVersion: tekton.dev/v1
kind: Pipeline
metadata: {name: p}
spec:
  tasks:
    - name: a
      taskRef: {name: t}
      matrix:
        params: [{name: x, value: ["$(tasks.ghost.results.r)"]}]
`)
	errs := validator.Validate(b, "p", nil)
	if !errsContain(errs, `references unknown task "ghost"`) {
		t.Fatalf("want unknown-task error, got %v", errs)
	}
}

func TestAuditE_EmptyWhenValues(t *testing.T) {
	b := auditLoad(t, auditTask+`---
apiVersion: tekton.dev/v1
kind: Pipeline
metadata: {name: p}
spec:
  tasks:
    - name: a
      taskRef: {name: t}
      when: [{input: "x", operator: in, values: []}]
`)
	if errs := validator.Validate(b, "p", nil); !errsContain(errs, "non-empty values") {
		t.Fatalf("want empty-values error, got %v", errs)
	}
}

func TestAuditE_DuplicateStepNames(t *testing.T) {
	b := auditLoad(t, `
apiVersion: tekton.dev/v1
kind: Pipeline
metadata: {name: p}
spec:
  tasks:
    - name: a
      taskSpec:
        steps:
          - {name: s, image: alpine, script: 'true'}
          - {name: s, image: alpine, script: 'true'}
          - {image: alpine, script: 'true'}
          - {image: alpine, script: 'true'}
`)
	errs := validator.Validate(b, "p", nil)
	if !errsContain(errs, `duplicate step name "s"`) {
		t.Fatalf("want duplicate step error, got %v", errs)
	}
	if len(errs) != 1 {
		t.Errorf("unnamed steps must not be flagged; got %v", errs)
	}
	spec := tektontypes.TaskSpec{Steps: []tektontypes.Step{{Name: "s", Image: "a"}, {Name: "s", Image: "a"}}}
	if es := validator.ValidateTaskSpec("t", spec); !errsContain(es, "duplicate step name") {
		t.Errorf("ValidateTaskSpec: want duplicate step error, got %v", es)
	}
}

func TestAuditE_UndeclaredParamRefs(t *testing.T) {
	b := auditLoad(t, `
apiVersion: tekton.dev/v1
kind: Task
metadata: {name: t}
spec:
  params: [{name: x}]
  steps:
    - name: s
      image: alpine
      env: [{name: A, value: "$(params.nope)"}]
      script: 'echo $(params.x) $(params["also-nope"]) $(date)'
---
apiVersion: tekton.dev/v1
kind: Pipeline
metadata: {name: p}
spec:
  params: [{name: rev, default: main}]
  tasks:
    - {name: a, taskRef: {name: t}, params: [{name: x, value: "$(params.missing)"}]}
`)
	errs := validator.Validate(b, "p", nil)
	for _, want := range []string{
		`references undeclared pipeline param "missing"`,
		`references undeclared param "nope"`,
		`references undeclared param "also-nope"`,
	} {
		if !errsContain(errs, want) {
			t.Errorf("want %q in %v", want, errs)
		}
	}
	if len(errs) != 3 {
		t.Errorf("want exactly 3 errors (shell $(date) is legal in scripts), got %v", errs)
	}
}

func TestAuditE_NegativeTaskTimeout(t *testing.T) {
	b := auditLoad(t, `
apiVersion: tekton.dev/v1
kind: Pipeline
metadata: {name: p}
spec:
  tasks:
    - name: a
      taskSpec:
        timeout: -1s
        steps: [{name: s, image: alpine, script: 'true'}]
`)
	if errs := validator.Validate(b, "p", nil); !errsContain(errs, "must not be negative") {
		t.Fatalf("want negative timeout error, got %v", errs)
	}
	if es := validator.ValidateTaskSpec("t", tektontypes.TaskSpec{
		Timeout: "-5m", Steps: []tektontypes.Step{{Name: "s", Image: "a"}},
	}); !errsContain(es, "must not be negative") {
		t.Errorf("ValidateTaskSpec: got %v", es)
	}
	// 0s means "no timeout" in Tekton and stays legal.
	if es := validator.ValidateTaskSpec("t", tektontypes.TaskSpec{
		Timeout: "0s", Steps: []tektontypes.Step{{Name: "s", Image: "a"}},
	}); len(es) != 0 {
		t.Errorf("0s timeout must be accepted, got %v", es)
	}
}

func TestAuditE_VarPrefixRule(t *testing.T) {
	b := auditLoad(t, auditTask+`---
apiVersion: tekton.dev/v1
kind: Pipeline
metadata: {name: p}
spec:
  tasks:
    - name: a
      taskRef: {name: t}
      params: [{name: x, value: "$(date) $(workspaces.w.bound) $(tt.params.y)"}]
      when: [{input: "$(HOME)", operator: in, values: [x]}]
      matrix:
        include: [{name: i, params: [{name: x, value: "$(uname)"}]}]
`)
	errs := validator.Validate(b, "p", nil)
	for _, want := range []string{`"$(date)"`, `"$(HOME)"`, `"$(uname)"`} {
		if !errsContain(errs, "invalid variable reference "+want) {
			t.Errorf("want prefix error for %s, got %v", want, errs)
		}
	}
	if errsContain(errs, "workspaces.w.bound") || errsContain(errs, "tt.params.y") {
		t.Errorf("allowed prefixes flagged: %v", errs)
	}
	if !errsContain(errs, "use ${VAR}") {
		t.Errorf("want shell-variable hint, got %v", errs)
	}
}

func TestAuditE_DeterministicErrorOrder(t *testing.T) {
	y := `
apiVersion: tekton.dev/v1
kind: Pipeline
metadata: {name: p}
spec:
  tasks:
`
	for _, n := range []string{"a", "b", "c", "d", "e", "f", "g", "h"} {
		y += "    - {name: " + n + ", taskSpec: {timeout: bogus, steps: [{name: s, image: alpine, onError: bad}]}}\n"
	}
	b := auditLoad(t, y)
	first := validator.Validate(b, "p", nil)
	for i := 0; i < 20; i++ {
		got := validator.Validate(b, "p", nil)
		if !reflect.DeepEqual(errStrings(first), errStrings(got)) {
			t.Fatalf("error order not deterministic:\n%v\nvs\n%v", errStrings(first), errStrings(got))
		}
	}
	// Pipeline-task order within a rule.
	if !strings.Contains(first[0].Error(), `"a"`) {
		t.Errorf("first error should be for task a, got %v", first[0])
	}
}

func errStrings(errs []error) []string {
	out := make([]string, len(errs))
	for i, e := range errs {
		out[i] = e.Error()
	}
	return out
}
