package validator_test

import (
	"strings"
	"testing"

	"github.com/danielfbm/tkn-act/internal/loader"
	"github.com/danielfbm/tkn-act/internal/validator"
)

func TestRejectsNegativeRetries(t *testing.T) {
	b, _ := loader.LoadBytes([]byte(`
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
    - {name: a, taskRef: {name: t}, retries: -1}
`))
	errs := validator.Validate(b, "p", nil)
	if !anyErrContains(errs, "retries must be non-negative") {
		t.Errorf("want retries error; got: %v", errs)
	}
}

func TestRejectsBadTimeout(t *testing.T) {
	b, _ := loader.LoadBytes([]byte(`
apiVersion: tekton.dev/v1
kind: Task
metadata: {name: t}
spec:
  timeout: "not-a-duration"
  steps: [{name: s, image: alpine, script: 'true'}]
---
apiVersion: tekton.dev/v1
kind: Pipeline
metadata: {name: p}
spec:
  tasks:
    - {name: a, taskRef: {name: t}}
`))
	errs := validator.Validate(b, "p", nil)
	if !anyErrContains(errs, "invalid timeout") {
		t.Errorf("want invalid-timeout error; got: %v", errs)
	}
}

func TestAcceptsValidTimeout(t *testing.T) {
	b, _ := loader.LoadBytes([]byte(`
apiVersion: tekton.dev/v1
kind: Task
metadata: {name: t}
spec:
  timeout: "30s"
  steps: [{name: s, image: alpine, script: 'true'}]
---
apiVersion: tekton.dev/v1
kind: Pipeline
metadata: {name: p}
spec:
  tasks:
    - {name: a, taskRef: {name: t}}
`))
	errs := validator.Validate(b, "p", nil)
	if anyErrContains(errs, "timeout") {
		t.Errorf("did not expect timeout error; got: %v", errs)
	}
}

func TestRejectsUnknownOnError(t *testing.T) {
	b, _ := loader.LoadBytes([]byte(`
apiVersion: tekton.dev/v1
kind: Task
metadata: {name: t}
spec:
  steps:
    - {name: s, image: alpine, script: 'true', onError: maybe}
---
apiVersion: tekton.dev/v1
kind: Pipeline
metadata: {name: p}
spec:
  tasks:
    - {name: a, taskRef: {name: t}}
`))
	errs := validator.Validate(b, "p", nil)
	if !anyErrContains(errs, "unsupported onError") {
		t.Errorf("want onError error; got: %v", errs)
	}
}

func TestAcceptsOnErrorContinue(t *testing.T) {
	b, _ := loader.LoadBytes([]byte(`
apiVersion: tekton.dev/v1
kind: Task
metadata: {name: t}
spec:
  steps:
    - {name: s, image: alpine, script: 'true', onError: continue}
---
apiVersion: tekton.dev/v1
kind: Pipeline
metadata: {name: p}
spec:
  tasks:
    - {name: a, taskRef: {name: t}}
`))
	errs := validator.Validate(b, "p", nil)
	for _, e := range errs {
		if strings.Contains(e.Error(), "onError") {
			t.Errorf("did not expect onError error; got: %v", e)
		}
	}
}

func anyErrContains(errs []error, sub string) bool {
	for _, e := range errs {
		if strings.Contains(e.Error(), sub) {
			return true
		}
	}
	return false
}
