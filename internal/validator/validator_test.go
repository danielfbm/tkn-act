package validator_test

import (
	"strings"
	"testing"

	"github.com/danielfbm/tkn-act/internal/loader"
	"github.com/danielfbm/tkn-act/internal/validator"
)

func TestRejectsUnknownTaskRef(t *testing.T) {
	b, err := loader.LoadBytes([]byte(`
apiVersion: tekton.dev/v1
kind: Pipeline
metadata: {name: p}
spec:
  tasks:
    - name: a
      taskRef: {name: doesnotexist}
`))
	if err != nil {
		t.Fatal(err)
	}
	errs := validator.Validate(b, "p", nil)
	if len(errs) == 0 {
		t.Fatal("want error for unknown taskRef")
	}
	if !strings.Contains(errs[0].Error(), "doesnotexist") {
		t.Errorf("err: %v", errs[0])
	}
}

func TestRejectsCycle(t *testing.T) {
	b, err := loader.LoadBytes([]byte(`
apiVersion: tekton.dev/v1
kind: Task
metadata: {name: t}
spec:
  steps: [{name: s, image: alpine, script: 'true'}]
---
apiVersion: tekton.dev/v1
kind: Pipeline
metadata: {name: p}
spec:
  tasks:
    - {name: a, taskRef: {name: t}, runAfter: [b]}
    - {name: b, taskRef: {name: t}, runAfter: [a]}
`))
	if err != nil {
		t.Fatal(err)
	}
	errs := validator.Validate(b, "p", nil)
	if len(errs) == 0 || !strings.Contains(errs[0].Error(), "cycle") {
		t.Errorf("want cycle, got %v", errs)
	}
}

func TestRejectsMissingWorkspaceBinding(t *testing.T) {
	b, err := loader.LoadBytes([]byte(`
apiVersion: tekton.dev/v1
kind: Task
metadata: {name: t}
spec:
  workspaces: [{name: src}]
  steps: [{name: s, image: alpine, script: 'true'}]
---
apiVersion: tekton.dev/v1
kind: Pipeline
metadata: {name: p}
spec:
  tasks:
    - name: a
      taskRef: {name: t}
      # no workspaces binding
`))
	if err != nil {
		t.Fatal(err)
	}
	errs := validator.Validate(b, "p", nil)
	if len(errs) == 0 {
		t.Fatal("expected workspace error")
	}
}

func mustLoad(t *testing.T, yaml string) *loader.Bundle {
	t.Helper()
	b, err := loader.LoadBytes([]byte(yaml))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestValidateTimeoutsMalformed(t *testing.T) {
	b := mustLoad(t, `
apiVersion: tekton.dev/v1
kind: Pipeline
metadata: {name: p}
spec:
  timeouts: {pipeline: "1zz"}
  tasks:
    - {name: a, taskRef: {name: t}}
---
apiVersion: tekton.dev/v1
kind: Task
metadata: {name: t}
spec:
  steps: [{name: s, image: alpine, script: "true"}]
`)
	errs := validator.Validate(b, "p", nil)
	if len(errs) == 0 {
		t.Fatalf("expected error for malformed pipeline timeout")
	}
}

func TestValidateTimeoutsZeroRejected(t *testing.T) {
	b := mustLoad(t, `
apiVersion: tekton.dev/v1
kind: Pipeline
metadata: {name: p}
spec:
  timeouts: {pipeline: "0"}
  tasks:
    - {name: a, taskRef: {name: t}}
---
apiVersion: tekton.dev/v1
kind: Task
metadata: {name: t}
spec:
  steps: [{name: s, image: alpine, script: "true"}]
`)
	errs := validator.Validate(b, "p", nil)
	if len(errs) == 0 {
		t.Fatalf("expected error for zero timeout (use omission to mean no budget)")
	}
}

func TestValidateTimeoutsTasksPlusFinallyExceedsPipeline(t *testing.T) {
	b := mustLoad(t, `
apiVersion: tekton.dev/v1
kind: Pipeline
metadata: {name: p}
spec:
  timeouts: {pipeline: "10m", tasks: "8m", finally: "5m"}
  tasks:
    - {name: a, taskRef: {name: t}}
---
apiVersion: tekton.dev/v1
kind: Task
metadata: {name: t}
spec:
  steps: [{name: s, image: alpine, script: "true"}]
`)
	errs := validator.Validate(b, "p", nil)
	if len(errs) == 0 {
		t.Fatalf("expected error for tasks+finally > pipeline")
	}
}

func TestValidateTimeoutsAllValid(t *testing.T) {
	b := mustLoad(t, `
apiVersion: tekton.dev/v1
kind: Pipeline
metadata: {name: p}
spec:
  timeouts: {pipeline: "10m", tasks: "8m", finally: "2m"}
  tasks:
    - {name: a, taskRef: {name: t}}
---
apiVersion: tekton.dev/v1
kind: Task
metadata: {name: t}
spec:
  steps: [{name: s, image: alpine, script: "true"}]
`)
	if errs := validator.Validate(b, "p", nil); len(errs) != 0 {
		t.Errorf("unexpected errors: %v", errs)
	}
}

func TestValidateStepTemplateSuppliesImage(t *testing.T) {
	b := mustLoad(t, `
apiVersion: tekton.dev/v1
kind: Task
metadata: {name: t}
spec:
  stepTemplate:
    image: alpine:3
  steps:
    - {name: s, script: "true"}
---
apiVersion: tekton.dev/v1
kind: Pipeline
metadata: {name: p}
spec:
  tasks:
    - {name: a, taskRef: {name: t}}
`)
	if errs := validator.Validate(b, "p", nil); len(errs) != 0 {
		t.Errorf("unexpected errors: %v", errs)
	}
}

func TestValidatePipelineResultsReferencesUnknownTask(t *testing.T) {
	b := mustLoad(t, `
apiVersion: tekton.dev/v1
kind: Task
metadata: {name: t}
spec:
  results: [{name: v}]
  steps: [{name: s, image: alpine, script: "true"}]
---
apiVersion: tekton.dev/v1
kind: Pipeline
metadata: {name: p}
spec:
  results:
    - name: r
      value: $(tasks.notthere.results.v)
  tasks:
    - {name: a, taskRef: {name: t}}
`)
	errs := validator.Validate(b, "p", nil)
	if len(errs) == 0 {
		t.Fatalf("expected error for unknown task ref in spec.results")
	}
	var found bool
	for _, e := range errs {
		if strings.Contains(e.Error(), "notthere") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("error did not name the unknown task: %v", errs)
	}
}

func TestValidatePipelineResultsKnownTaskOK(t *testing.T) {
	b := mustLoad(t, `
apiVersion: tekton.dev/v1
kind: Task
metadata: {name: t}
spec:
  results: [{name: v}]
  steps: [{name: s, image: alpine, script: "true"}]
---
apiVersion: tekton.dev/v1
kind: Pipeline
metadata: {name: p}
spec:
  results:
    - name: from-main
      value: $(tasks.a.results.v)
    - name: from-finally
      value: $(tasks.f.results.v)
  tasks:
    - {name: a, taskRef: {name: t}}
  finally:
    - {name: f, taskRef: {name: t}}
`)
	if errs := validator.Validate(b, "p", nil); len(errs) != 0 {
		t.Errorf("unexpected errors: %v", errs)
	}
}

// Pipeline.spec.results entries must have unique names. Two entries
// with the same name silently collide in the resolved map (last write
// wins) and the user has no way to recover the dropped one — better
// to reject the spec at validation time. PR #18 reviewer Min-2.
func TestValidatePipelineResultsRejectsDuplicateNames(t *testing.T) {
	b := mustLoad(t, `
apiVersion: tekton.dev/v1
kind: Task
metadata: {name: t}
spec:
  results: [{name: v}]
  steps: [{name: s, image: alpine, script: "true"}]
---
apiVersion: tekton.dev/v1
kind: Pipeline
metadata: {name: p}
spec:
  results:
    - name: r
      value: $(tasks.a.results.v)
    - name: r
      value: $(tasks.a.results.v)
  tasks:
    - {name: a, taskRef: {name: t}}
`)
	errs := validator.Validate(b, "p", nil)
	if len(errs) == 0 {
		t.Fatalf("expected error for duplicate pipeline result name")
	}
	var found bool
	for _, e := range errs {
		if strings.Contains(e.Error(), "duplicate") && strings.Contains(e.Error(), `"r"`) {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("error did not flag duplicate %q: %v", "r", errs)
	}
}

func TestValidatePipelineResultsUniqueNamesOK(t *testing.T) {
	b := mustLoad(t, `
apiVersion: tekton.dev/v1
kind: Task
metadata: {name: t}
spec:
  results: [{name: v}]
  steps: [{name: s, image: alpine, script: "true"}]
---
apiVersion: tekton.dev/v1
kind: Pipeline
metadata: {name: p}
spec:
  results:
    - name: r1
      value: $(tasks.a.results.v)
    - name: r2
      value: $(tasks.a.results.v)
  tasks:
    - {name: a, taskRef: {name: t}}
`)
	if errs := validator.Validate(b, "p", nil); len(errs) != 0 {
		t.Errorf("unexpected errors for unique names: %v", errs)
	}
}

// Regression: RFC 1123 names allow leading digits, so a PipelineTask
// can legally be named "1stcheckout". The pipeline-results task-ref
// validator must catch refs to a leading-digit name that doesn't
// exist (and accept refs to one that does). Previously the regex
// silently skipped over digit-prefixed task names, so unknown refs
// to e.g. $(tasks.1nope.results.x) slipped past validation.
// See PR #18 review.
func TestValidatePipelineResultsLeadingDigitTaskNameUnknown(t *testing.T) {
	b := mustLoad(t, `
apiVersion: tekton.dev/v1
kind: Task
metadata: {name: t}
spec:
  results: [{name: x}]
  steps: [{name: s, image: alpine, script: "true"}]
---
apiVersion: tekton.dev/v1
kind: Pipeline
metadata: {name: p}
spec:
  results:
    - name: r
      value: $(tasks.1stcheckout.results.x)
  tasks:
    - {name: a, taskRef: {name: t}}
`)
	errs := validator.Validate(b, "p", nil)
	if len(errs) == 0 {
		t.Fatalf("expected error for unknown leading-digit task ref")
	}
	var found bool
	for _, e := range errs {
		if strings.Contains(e.Error(), "1stcheckout") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("error did not name the unknown leading-digit task: %v", errs)
	}
}

func TestValidatePipelineResultsLeadingDigitTaskNameKnown(t *testing.T) {
	b := mustLoad(t, `
apiVersion: tekton.dev/v1
kind: Task
metadata: {name: t}
spec:
  results: [{name: x}]
  steps: [{name: s, image: alpine, script: "true"}]
---
apiVersion: tekton.dev/v1
kind: Pipeline
metadata: {name: p}
spec:
  results:
    - name: r
      value: $(tasks.1stcheckout.results.x)
  tasks:
    - {name: 1stcheckout, taskRef: {name: t}}
`)
	if errs := validator.Validate(b, "p", nil); len(errs) != 0 {
		t.Errorf("unexpected errors when leading-digit task IS declared: %v", errs)
	}
}

func TestValidatePipelineResultsArrayAndObjectChecked(t *testing.T) {
	b := mustLoad(t, `
apiVersion: tekton.dev/v1
kind: Task
metadata: {name: t}
spec:
  results: [{name: v}]
  steps: [{name: s, image: alpine, script: "true"}]
---
apiVersion: tekton.dev/v1
kind: Pipeline
metadata: {name: p}
spec:
  results:
    - name: list
      value:
        - $(tasks.a.results.v)
        - $(tasks.unknown.results.v)
    - name: obj
      value:
        ok:  $(tasks.a.results.v)
        bad: $(tasks.alsomissing.results.v)
  tasks:
    - {name: a, taskRef: {name: t}}
`)
	errs := validator.Validate(b, "p", nil)
	if len(errs) < 2 {
		t.Fatalf("expected at least 2 errors (unknown + alsomissing), got %v", errs)
	}
	joined := ""
	for _, e := range errs {
		joined += e.Error() + "\n"
	}
	if !strings.Contains(joined, "unknown") || !strings.Contains(joined, "alsomissing") {
		t.Errorf("errors did not name both unknown tasks: %v", errs)
	}
}
