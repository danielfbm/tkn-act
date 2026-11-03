package cluster_test

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/danielfbm/tkn-act/internal/backend"
	"github.com/danielfbm/tkn-act/internal/tektontypes"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/watch"
	clienttesting "k8s.io/client-go/testing"
)

func simplePipeline() (tektontypes.Pipeline, map[string]tektontypes.Task) {
	pl := tektontypes.Pipeline{Spec: tektontypes.PipelineSpec{
		Tasks: []tektontypes.PipelineTask{{Name: "a", TaskRef: &tektontypes.TaskRef{Name: "t"}}},
	}}
	pl.Metadata.Name = "p"
	tk := tektontypes.Task{Spec: tektontypes.TaskSpec{Steps: []tektontypes.Step{{Name: "s", Image: "alpine:3", Script: "true"}}}}
	tk.Metadata.Name = "t"
	return pl, map[string]tektontypes.Task{"t": tk}
}

// TestRunPipelineSurvivesWatchClose: the API server closes watches
// after --min-request-timeout. The first PipelineRun watch here is
// closed immediately; RunPipeline must re-establish it and still see
// the terminal status instead of failing with "watch closed before
// terminal status".
func TestRunPipelineSurvivesWatchClose(t *testing.T) {
	be, dyn, _, _, _ := fakeBackend(t)
	var prWatches atomic.Int32
	dyn.PrependWatchReactor("pipelineruns", func(clienttesting.Action) (bool, watch.Interface, error) {
		if prWatches.Add(1) == 1 {
			fw := watch.NewFake()
			fw.Stop()
			return true, fw, nil
		}
		return false, nil, nil
	})
	prName, ns := "p-aaaabbbb", "tkn-act-aaaabbbb"
	stop := flipStatusUntilStop(t, dyn, ns, prName, "True", "Succeeded")
	defer close(stop)

	pl, tasks := simplePipeline()
	res, err := be.RunPipeline(context.Background(), backend.PipelineRunInvocation{
		RunID: "aaaabbbb", PipelineRunName: prName, Pipeline: pl, Tasks: tasks,
	})
	if err != nil {
		t.Fatalf("RunPipeline: %v", err)
	}
	if res.Status != "succeeded" {
		t.Errorf("status = %q, want succeeded", res.Status)
	}
	if prWatches.Load() < 2 {
		t.Errorf("PipelineRun watch opened %d times, want a re-watch", prWatches.Load())
	}
}

// TestRunPipelineCancelPatchesPipelineRun: on context cancel (Ctrl-C)
// the backend asks Tekton to cancel the PipelineRun so its pods stop.
func TestRunPipelineCancelPatchesPipelineRun(t *testing.T) {
	be, dyn, _, _, _ := fakeBackend(t)
	patched := make(chan string, 1)
	dyn.PrependReactor("patch", "pipelineruns", func(a clienttesting.Action) (bool, runtime.Object, error) {
		pa := a.(clienttesting.PatchAction)
		select {
		case patched <- string(pa.GetPatch()):
		default:
		}
		return true, nil, nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(200 * time.Millisecond); cancel() }()

	pl, tasks := simplePipeline()
	_, err := be.RunPipeline(ctx, backend.PipelineRunInvocation{
		RunID: "ccccdddd", PipelineRunName: "p-ccccdddd", Pipeline: pl, Tasks: tasks,
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want wrapped context.Canceled", err)
	}
	select {
	case p := <-patched:
		if !strings.Contains(p, `"status":"Cancelled"`) {
			t.Errorf("patch = %s, want spec.status Cancelled", p)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("PipelineRun was not patched to Cancelled")
	}
}
