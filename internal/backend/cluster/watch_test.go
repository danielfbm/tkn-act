package cluster

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/danielfbm/tkn-act/internal/backend"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/watch"
	kubefake "k8s.io/client-go/kubernetes/fake"
)

func obj(rv string) *unstructured.Unstructured {
	u := &unstructured.Unstructured{Object: map[string]any{}}
	u.SetResourceVersion(rv)
	return u
}

// TestWatchLoop_ReestablishesFromLastResourceVersion: the API server
// closes every watch after --min-request-timeout; watchLoop must
// re-open it from the last seen resourceVersion instead of failing.
func TestWatchLoop_ReestablishesFromLastResourceVersion(t *testing.T) {
	old := watchRetryDelay
	watchRetryDelay = time.Millisecond
	defer func() { watchRetryDelay = old }()

	var rvs []string
	calls := 0
	open := func(_ context.Context, rv string) (watch.Interface, error) {
		calls++
		rvs = append(rvs, rv)
		fw := watch.NewFakeWithChanSize(4, false)
		switch calls {
		case 1:
			fw.Add(obj("10"))
			fw.Stop() // server-side close
		case 2:
			fw.Modify(obj("11"))
			fw.Error(&metav1.Status{Code: 410, Reason: metav1.StatusReasonExpired})
		default:
			fw.Modify(obj("99"))
		}
		return fw, nil
	}
	var seen []string
	err := watchLoop(context.Background(), open, func(_ watch.EventType, u *unstructured.Unstructured) bool {
		seen = append(seen, u.GetResourceVersion())
		return u.GetResourceVersion() == "99"
	})
	if err != nil {
		t.Fatalf("watchLoop: %v", err)
	}
	if want := []string{"", "10", ""}; len(rvs) != 3 || rvs[0] != want[0] || rvs[1] != want[1] || rvs[2] != want[2] {
		t.Errorf("resourceVersions opened = %q, want %q (410 Gone resets to a fresh watch)", rvs, want)
	}
	if len(seen) != 3 {
		t.Errorf("events handled = %q", seen)
	}
}

func TestWatchLoop_GivesUpOnPersistentOpenError(t *testing.T) {
	old := watchRetryDelay
	watchRetryDelay = time.Microsecond
	defer func() { watchRetryDelay = old }()
	boom := errors.New("boom")
	err := watchLoop(context.Background(), func(context.Context, string) (watch.Interface, error) {
		return nil, boom
	}, func(watch.EventType, *unstructured.Unstructured) bool { return false })
	if !errors.Is(err, boom) {
		t.Errorf("err = %v, want boom", err)
	}
}

func TestWatchLoop_ContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	fw := watch.NewFake()
	go func() { time.Sleep(20 * time.Millisecond); cancel() }()
	err := watchLoop(ctx, func(context.Context, string) (watch.Interface, error) { return fw, nil },
		func(watch.EventType, *unstructured.Unstructured) bool { return false })
	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
}

type lineSink struct {
	mu    sync.Mutex
	lines []string
}

func (s *lineSink) StepLog(_, step, _, _, line string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lines = append(s.lines, step+":"+line)
}

func (s *lineSink) SidecarLog(_, sc, _, line string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lines = append(s.lines, "sidecar/"+sc+":"+line)
}

func (s *lineSink) get() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.lines...)
}

// TestFollowContainerLogs_WaitsForContainerStart is the regression for
// slow pods: the old code tried GetLogs once ~500ms after the pod
// appeared and silently gave up on "waiting to start". Logs must be
// streamed once the container reaches running.
func TestFollowContainerLogs_WaitsForContainerStart(t *testing.T) {
	oldMax := logRetryMax
	logRetryMax = 20 * time.Millisecond
	defer func() { logRetryMax = oldMax }()

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "p", Namespace: "ns"},
		Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "step-build"}}},
		Status: corev1.PodStatus{Phase: corev1.PodPending, ContainerStatuses: []corev1.ContainerStatus{{
			Name:  "step-build",
			State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "PodInitializing"}},
		}}},
	}
	kube := kubefake.NewSimpleClientset(pod)
	b := NewWithClients(ClientBundle{Kube: kube})

	var got []string
	var mu sync.Mutex
	done := make(chan struct{})
	go func() {
		defer close(done)
		b.followContainerLogs(context.Background(), "ns", "p", "step-build", func(l string) {
			mu.Lock()
			got = append(got, l)
			mu.Unlock()
		})
	}()
	time.Sleep(100 * time.Millisecond)
	mu.Lock()
	early := len(got)
	mu.Unlock()
	if early != 0 {
		t.Fatalf("streamed %d lines while container still waiting", early)
	}
	pod.Status.Phase = corev1.PodRunning
	pod.Status.ContainerStatuses[0].State = corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}
	if _, err := kube.CoreV1().Pods("ns").UpdateStatus(context.Background(), pod, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("followContainerLogs never streamed after container started")
	}
	if len(got) != 1 || got[0] != "fake logs" {
		t.Errorf("lines = %q, want [fake logs]", got)
	}
}

func TestFollowContainerLogs_GivesUpWhenPodFinishedWithoutStart(t *testing.T) {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "p", Namespace: "ns"},
		Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "step-a"}}},
		Status:     corev1.PodStatus{Phase: corev1.PodFailed},
	}
	b := NewWithClients(ClientBundle{Kube: kubefake.NewSimpleClientset(pod)})
	done := make(chan struct{})
	go func() {
		defer close(done)
		b.followContainerLogs(context.Background(), "ns", "p", "step-a", func(string) {})
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("did not give up on a finished pod")
	}
	// Pod deleted entirely.
	b2 := NewWithClients(ClientBundle{Kube: kubefake.NewSimpleClientset()})
	b2.followContainerLogs(context.Background(), "ns", "gone", "step-a", func(string) {})
}

// TestTaskRunLogTracker_JoinsStreams verifies observe attaches every
// step and sidecar container and join waits for their output.
func TestTaskRunLogTracker_JoinsStreams(t *testing.T) {
	running := corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{}}
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "pod-1", Namespace: "ns"},
		Spec: corev1.PodSpec{Containers: []corev1.Container{
			{Name: "step-a"}, {Name: "sidecar-redis"}, {Name: "other"},
		}},
		Status: corev1.PodStatus{Phase: corev1.PodSucceeded, ContainerStatuses: []corev1.ContainerStatus{
			{Name: "step-a", State: running}, {Name: "sidecar-redis", State: running},
		}},
	}
	b := NewWithClients(ClientBundle{Kube: kubefake.NewSimpleClientset(pod)})
	sink := &lineSink{}
	tr := &unstructured.Unstructured{Object: map[string]any{
		"metadata": map[string]any{"name": "tr-1", "labels": map[string]any{"tekton.dev/pipelineTask": "t"}},
		"status":   map[string]any{"podName": "pod-1"},
	}}
	tk := newTaskRunLogTracker(context.Background(), b, backendInv(sink), "ns")
	tk.observe(tr)
	tk.observe(tr) // second observation must not double-attach
	tk.join()
	got := sink.get()
	if len(got) != 2 {
		t.Fatalf("lines = %q, want one per step/sidecar container", got)
	}
}

func backendInv(sink backend.LogSink) backend.PipelineRunInvocation {
	return backend.PipelineRunInvocation{LogSink: sink}
}
