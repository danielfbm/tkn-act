package engine

import (
	"reflect"
	"sort"
	"testing"

	"github.com/danielfbm/tkn-act/internal/loader"
)

func TestUniqueImagesIncludesSidecars(t *testing.T) {
	b, err := loader.LoadBytes([]byte(`
apiVersion: tekton.dev/v1
kind: Task
metadata: {name: t}
spec:
  sidecars:
    - {name: redis, image: redis:7-alpine}
  steps:
    - {name: s, image: alpine:3, script: 'true'}
---
apiVersion: tekton.dev/v1
kind: Pipeline
metadata: {name: p}
spec:
  tasks: [{name: t, taskRef: {name: t}}]
`))
	if err != nil {
		t.Fatal(err)
	}
	got := uniqueImages(b, b.Pipelines["p"])
	sort.Strings(got)
	want := []string{"alpine:3", "redis:7-alpine"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("uniqueImages = %v, want %v", got, want)
	}
}

// Image strings still containing `$(...)` (param refs, matrix-row refs,
// context refs) are not valid OCI references at pre-pull time. Pre-pull
// must skip them and let the per-step ensureImage path handle the
// substituted image at task-dispatch time, otherwise a pipeline with
// a parameterised image fails before any task starts with
// "invalid reference format".
func TestUniqueImagesSkipsUnsubstitutedRefs(t *testing.T) {
	b, err := loader.LoadBytes([]byte(`
apiVersion: tekton.dev/v1
kind: Task
metadata: {name: t}
spec:
  params:
    - {name: go-version, type: string, default: "1.25"}
  sidecars:
    - {name: db, image: "postgres:$(params.go-version)"}
  steps:
    - {name: literal, image: alpine:3, script: 'true'}
    - {name: parameterised, image: "golang:$(params.go-version)-alpine", script: 'true'}
---
apiVersion: tekton.dev/v1
kind: Pipeline
metadata: {name: p}
spec:
  tasks: [{name: t, taskRef: {name: t}}]
`))
	if err != nil {
		t.Fatal(err)
	}
	got := uniqueImages(b, b.Pipelines["p"])
	sort.Strings(got)
	want := []string{"alpine:3"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("uniqueImages = %v, want %v (must omit $(...) refs)", got, want)
	}
}
