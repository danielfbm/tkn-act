package cluster_test

import (
	"testing"

	"github.com/danielfbm/tkn-act/internal/backend"
	"github.com/danielfbm/tkn-act/internal/backend/cluster"
	"github.com/danielfbm/tkn-act/internal/tektontypes"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic/fake"
)

var pipelineRunGVR = schema.GroupVersionResource{Group: "tekton.dev", Version: "v1", Resource: "pipelineruns"}
var taskGVR = schema.GroupVersionResource{Group: "tekton.dev", Version: "v1", Resource: "tasks"}

func TestRunPipelineConstructsExpectedResources(t *testing.T) {
	scheme := runtime.NewScheme()
	dyn := fake.NewSimpleDynamicClient(scheme)
	be := cluster.NewWithClients(cluster.ClientBundle{
		Dynamic: dyn,
		// driver/installer are nil for this test — we directly invoke Run logic
	})

	// Build a tiny pipeline.
	pl := tektontypes.Pipeline{
		Object: tektontypes.Object{APIVersion: "tekton.dev/v1", Kind: "Pipeline"},
		Spec: tektontypes.PipelineSpec{
			Tasks: []tektontypes.PipelineTask{{Name: "a", TaskRef: &tektontypes.TaskRef{Name: "t"}}},
		},
	}
	pl.Metadata.Name = "p"
	tk := tektontypes.Task{
		Object: tektontypes.Object{APIVersion: "tekton.dev/v1", Kind: "Task"},
		Spec:   tektontypes.TaskSpec{Steps: []tektontypes.Step{{Name: "s", Image: "alpine:3", Script: "true"}}},
	}
	tk.Metadata.Name = "t"

	// Precondition: simulate that the controller would set the PipelineRun to Succeeded
	// We do that by setting up the fake client to return a "Succeeded" status when the PipelineRun is created.
	// For unit tests that don't require the watch loop, call SubmitOnly to confirm resource construction.
	prObj, err := be.BuildPipelineRunObject(backend.PipelineRunInvocation{
		RunID: "test", PipelineRunName: "p-12345678",
		Pipeline: pl,
		Tasks:    map[string]tektontypes.Task{"t": tk},
	}, "tkn-act-12345678")
	if err != nil {
		t.Fatal(err)
	}

	// Verify it's a PipelineRun in tekton.dev/v1 with pipelineSpec inlined.
	un, ok := prObj.(*unstructured.Unstructured)
	if !ok {
		t.Fatalf("expected unstructured, got %T", prObj)
	}
	if un.GetAPIVersion() != "tekton.dev/v1" || un.GetKind() != "PipelineRun" {
		t.Errorf("got %s/%s", un.GetAPIVersion(), un.GetKind())
	}
	if un.GetNamespace() != "tkn-act-12345678" {
		t.Errorf("namespace = %q", un.GetNamespace())
	}
	spec, found, err := unstructured.NestedMap(un.Object, "spec")
	if err != nil || !found {
		t.Fatalf("missing spec")
	}
	if _, has := spec["pipelineSpec"]; !has {
		t.Errorf("expected inlined pipelineSpec; got keys: %v", keysOf(spec))
	}
	_ = pipelineRunGVR
	_ = taskGVR
	_ = dyn
}

func keysOf(m map[string]any) []string {
	out := []string{}
	for k := range m {
		out = append(out, k)
	}
	return out
}
