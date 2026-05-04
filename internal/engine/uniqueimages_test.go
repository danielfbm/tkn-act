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
