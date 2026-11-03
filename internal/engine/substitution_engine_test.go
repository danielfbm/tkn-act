package engine_test

import (
	"context"
	"reflect"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/danielfbm/tkn-act/internal/backend"
	"github.com/danielfbm/tkn-act/internal/engine"
	"github.com/danielfbm/tkn-act/internal/loader"
	"github.com/danielfbm/tkn-act/internal/reporter"
	"github.com/danielfbm/tkn-act/internal/tektontypes"
)

// recordingBackend captures every TaskInvocation (keyed by PipelineTask
// name, one entry per attempt) and lets a test script per-task outcomes.
type recordingBackend struct {
	mu      sync.Mutex
	invs    map[string][]backend.TaskInvocation
	outcome func(inv backend.TaskInvocation, attempt int) backend.TaskResult
}

func (r *recordingBackend) Prepare(_ context.Context, _ backend.RunSpec) error { return nil }
func (r *recordingBackend) Cleanup(_ context.Context) error                    { return nil }
func (r *recordingBackend) RunTask(_ context.Context, inv backend.TaskInvocation) (backend.TaskResult, error) {
	r.mu.Lock()
	if r.invs == nil {
		r.invs = map[string][]backend.TaskInvocation{}
	}
	r.invs[inv.TaskName] = append(r.invs[inv.TaskName], inv)
	attempt := len(r.invs[inv.TaskName])
	r.mu.Unlock()
	if r.outcome != nil {
		return r.outcome(inv, attempt), nil
	}
	return backend.TaskResult{Status: backend.TaskSucceeded}, nil
}

func (r *recordingBackend) inv(t *testing.T, task string) backend.TaskInvocation {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.invs[task]) == 0 {
		t.Fatalf("task %q never reached the backend (calls: %v)", task, r.invs)
	}
	return r.invs[task][len(r.invs[task])-1]
}

func runSubst(t *testing.T, yaml string, be *recordingBackend, params map[string]tektontypes.ParamValue, ws map[string]string) engine.RunResult {
	t.Helper()
	b, err := loader.LoadBytes([]byte(yaml))
	if err != nil {
		t.Fatal(err)
	}
	e := engine.New(be, reporter.NewJSON(&strings.Builder{}), engine.Options{MaxParallel: 4})
	res, err := e.RunPipeline(context.Background(), engine.PipelineInput{Bundle: b, Name: "p", Params: params, Workspaces: ws})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	return res
}

// C1.1: shell command substitution must reach the backend verbatim.
// Before, every field containing `$(date)` / `$(pwd)` / `$(hostname)`
// was silently replaced with "" (Command nil → image entrypoint ran) and
// the task reported succeeded.
func TestShellCommandSubstitutionLeftLiteral(t *testing.T) {
	be := &recordingBackend{}
	res := runSubst(t, `
apiVersion: tekton.dev/v1beta1
kind: StepAction
metadata: {name: sa}
spec:
  image: alpine
  script: 'echo $(date) $(params.msg)'
  params: [{name: msg, type: string}]
---
apiVersion: tekton.dev/v1
kind: Task
metadata: {name: t}
spec:
  params: [{name: who, default: me}]
  steps:
    - name: s1
      image: alpine
      script: 'touch /tmp/$(date).log; echo $(params.who)'
    - name: s2
      image: alpine
      command: [sh, -c, 'cd $(pwd) && make']
      env: [{name: E, value: '$(hostname)'}]
    - name: s3
      ref: {name: sa}
      params: [{name: msg, value: hi}]
---
apiVersion: tekton.dev/v1
kind: Pipeline
metadata: {name: p}
spec:
  tasks: [{name: a, taskRef: {name: t}}]
`, be, nil, nil)
	if res.Status != "succeeded" {
		t.Fatalf("status = %s (%+v)", res.Status, res.Tasks)
	}
	steps := be.inv(t, "a").Task.Steps
	if got := steps[0].Script; got != "touch /tmp/$(date).log; echo me" {
		t.Errorf("script = %q", got)
	}
	if got := steps[1].Command; !reflect.DeepEqual(got, []string{"sh", "-c", "cd $(pwd) && make"}) {
		t.Errorf("command = %q", got)
	}
	if got := steps[1].Env[0].Value; got != "$(hostname)" {
		t.Errorf("env = %q", got)
	}
	if got := steps[2].Script; got != "echo $(date) hi" {
		t.Errorf("stepaction script = %q", got)
	}
}

// C1.1: a substitution error in a step field must fail the task with a
// clear message instead of silently blanking the field.
func TestSubstitutionErrorFailsTask(t *testing.T) {
	be := &recordingBackend{}
	res := runSubst(t, `
apiVersion: tekton.dev/v1
kind: Task
metadata: {name: t}
spec:
  steps: [{name: s, image: alpine, script: 'echo $(params.missing)'}]
---
apiVersion: tekton.dev/v1
kind: Pipeline
metadata: {name: p}
spec:
  tasks: [{name: a, taskRef: {name: t}}]
`, be, nil, nil)
	if res.Status != "failed" {
		t.Fatalf("status = %s, want failed", res.Status)
	}
	oc := res.Tasks["a"]
	if oc.Status != "failed" || !strings.Contains(oc.Message, "missing") || !strings.Contains(oc.Message, `step "s"`) {
		t.Errorf("outcome = %+v", oc)
	}
	if len(be.invs["a"]) != 0 {
		t.Error("backend must not run a task whose substitution failed")
	}
}

func TestWorkspaceBound(t *testing.T) {
	be := &recordingBackend{}
	runSubst(t, `
apiVersion: tekton.dev/v1
kind: Task
metadata: {name: t}
spec:
  workspaces: [{name: src}, {name: cache, optional: true}]
  steps: [{name: s, image: alpine, script: 'echo $(workspaces.src.bound) $(workspaces.cache.bound)'}]
---
apiVersion: tekton.dev/v1
kind: Pipeline
metadata: {name: p}
spec:
  workspaces: [{name: ws}]
  tasks: [{name: a, taskRef: {name: t}, workspaces: [{name: src, workspace: ws}]}]
`, be, nil, map[string]string{"ws": t.TempDir()})
	if got := be.inv(t, "a").Task.Steps[0].Script; got != "echo true false" {
		t.Errorf("script = %q", got)
	}
}

// C1.2: array pass-through from a Pipeline array param into a Task array
// param, as a whole string value and as an array item.
func TestArrayParamPassThrough(t *testing.T) {
	be := &recordingBackend{}
	res := runSubst(t, `
apiVersion: tekton.dev/v1
kind: Task
metadata: {name: t}
spec:
  params: [{name: flags, type: array}]
  steps: [{name: s, image: alpine, args: ['$(params.flags[*])']}]
---
apiVersion: tekton.dev/v1
kind: Pipeline
metadata: {name: p}
spec:
  params: [{name: arr, type: array}]
  tasks:
    - {name: a, taskRef: {name: t}, params: [{name: flags, value: '$(params.arr[*])'}]}
    - {name: b, taskRef: {name: t}, params: [{name: flags, value: ['$(params.arr[*])', z]}]}
`, be, map[string]tektontypes.ParamValue{"arr": {Type: tektontypes.ParamTypeArray, ArrayVal: []string{"x", "y"}}}, nil)
	if res.Status != "succeeded" {
		t.Fatalf("status = %s (%+v)", res.Status, res.Tasks)
	}
	for task, want := range map[string][]string{"a": {"x", "y"}, "b": {"x", "y", "z"}} {
		inv := be.inv(t, task)
		p := inv.Params["flags"]
		if p.Type != tektontypes.ParamTypeArray || !reflect.DeepEqual(p.ArrayVal, want) {
			t.Errorf("%s: param = %+v, want array %v", task, p, want)
		}
		if got := inv.Task.Steps[0].Args; !reflect.DeepEqual(got, want) {
			t.Errorf("%s: args = %q, want %q", task, got, want)
		}
	}
}

// C1.3: $(tasks.status) / $(tasks.<t>.status) in finally params + when.
func TestFinallyTasksStatus(t *testing.T) {
	be := &recordingBackend{outcome: func(inv backend.TaskInvocation, _ int) backend.TaskResult {
		if inv.TaskName == "bad" {
			return backend.TaskResult{Status: backend.TaskFailed}
		}
		return backend.TaskResult{Status: backend.TaskSucceeded}
	}}
	res := runSubst(t, `
apiVersion: tekton.dev/v1
kind: Task
metadata: {name: t}
spec:
  params: [{name: m, default: ''}]
  steps: [{name: s, image: alpine, script: 'echo $(params.m)'}]
---
apiVersion: tekton.dev/v1
kind: Pipeline
metadata: {name: p}
spec:
  tasks:
    - {name: good, taskRef: {name: t}}
    - {name: bad, taskRef: {name: t}}
    - {name: after-bad, taskRef: {name: t}, runAfter: [bad]}
  finally:
    - name: report
      taskRef: {name: t}
      params: [{name: m, value: '$(tasks.status) $(tasks.good.status) $(tasks.bad.status) $(tasks.after-bad.status)'}]
    - name: on-failure
      taskRef: {name: t}
      when: [{input: '$(tasks.bad.status)', operator: in, values: [Failed]}]
    - name: on-success
      taskRef: {name: t}
      when: [{input: '$(tasks.status)', operator: in, values: [Succeeded]}]
`, be, nil, nil)
	if res.Status != "failed" {
		t.Fatalf("status = %s", res.Status)
	}
	if got := be.inv(t, "report").Params["m"].StringVal; got != "Failed Succeeded Failed None" {
		t.Errorf("report m = %q", got)
	}
	if res.Tasks["on-failure"].Status != "succeeded" {
		t.Errorf("on-failure = %+v, want succeeded", res.Tasks["on-failure"])
	}
	if res.Tasks["on-success"].Status != "skipped" {
		t.Errorf("on-success = %+v, want skipped", res.Tasks["on-success"])
	}
}

func TestFinallyTasksStatusSucceededAndCompleted(t *testing.T) {
	for _, tc := range []struct {
		env, want string
	}{
		{"prod", "Succeeded"},
		{"dev", "Completed"}, // one task when-skipped, none failed
	} {
		be := &recordingBackend{}
		runSubst(t, `
apiVersion: tekton.dev/v1
kind: Task
metadata: {name: t}
spec:
  params: [{name: m, default: ''}]
  steps: [{name: s, image: alpine, script: 'echo $(params.m)'}]
---
apiVersion: tekton.dev/v1
kind: Pipeline
metadata: {name: p}
spec:
  params: [{name: env}]
  tasks:
    - {name: a, taskRef: {name: t}}
    - name: deploy
      taskRef: {name: t}
      when: [{input: $(params.env), operator: in, values: [prod]}]
  finally:
    - {name: f, taskRef: {name: t}, params: [{name: m, value: '$(tasks.status)/$(tasks.deploy.status)'}]}
`, be, map[string]tektontypes.ParamValue{"env": {Type: tektontypes.ParamTypeString, StringVal: tc.env}}, nil)
		wantDeploy := "Succeeded"
		if tc.env == "dev" {
			wantDeploy = "None"
		}
		if got := be.inv(t, "f").Params["m"].StringVal; got != tc.want+"/"+wantDeploy {
			t.Errorf("env=%s: m = %q, want %q", tc.env, got, tc.want+"/"+wantDeploy)
		}
	}
}

// $(tasks.status) is a finally-only variable; a main task using it fails.
func TestTasksStatusInMainTaskFails(t *testing.T) {
	be := &recordingBackend{}
	res := runSubst(t, `
apiVersion: tekton.dev/v1
kind: Task
metadata: {name: t}
spec:
  params: [{name: m, default: ''}]
  steps: [{name: s, image: alpine, script: 'true'}]
---
apiVersion: tekton.dev/v1
kind: Pipeline
metadata: {name: p}
spec:
  tasks:
    - {name: a, taskRef: {name: t}}
    - {name: b, taskRef: {name: t}, runAfter: [a], params: [{name: m, value: '$(tasks.status)'}]}
`, be, nil, nil)
	if oc := res.Tasks["b"]; oc.Status != "failed" || !strings.Contains(oc.Message, "finally") {
		t.Errorf("b = %+v, want failed (finally-only)", oc)
	}
}

// C1.4: [N] indexing and object keys for params and results, and [*]
// inside when.values.
func TestIndexAndObjectRefs(t *testing.T) {
	be := &recordingBackend{outcome: func(inv backend.TaskInvocation, _ int) backend.TaskResult {
		if inv.TaskName == "produce" {
			return backend.TaskResult{Status: backend.TaskSucceeded, Results: map[string]string{
				"list": `["r0","r1"]`,
				"img":  `{"url":"reg/x","digest":"sha256:abc"}`,
			}}
		}
		return backend.TaskResult{Status: backend.TaskSucceeded}
	}}
	res := runSubst(t, `
apiVersion: tekton.dev/v1
kind: Task
metadata: {name: producer}
spec:
  results: [{name: list, type: array}, {name: img, type: object, properties: {url: {type: string}, digest: {type: string}}}]
  steps: [{name: s, image: alpine, script: 'true'}]
---
apiVersion: tekton.dev/v1
kind: Task
metadata: {name: consumer}
spec:
  params:
    - {name: arr, type: array}
    - {name: obj, type: object, properties: {k: {type: string}}}
    - {name: second, default: ''}
    - {name: digest, default: ''}
  steps: [{name: s, image: alpine, script: 'echo $(params.arr[1]) $(params.obj.k) $(params.second) $(params.digest)'}]
---
apiVersion: tekton.dev/v1
kind: Pipeline
metadata: {name: p}
spec:
  params: [{name: arr, type: array}, {name: env}]
  tasks:
    - {name: produce, taskRef: {name: producer}}
    - name: consume
      taskRef: {name: consumer}
      params:
        - {name: arr, value: [a, b]}
        - {name: obj, value: {k: v}}
        - {name: second, value: '$(tasks.produce.results.list[1])'}
        - {name: digest, value: '$(tasks.produce.results.img.digest)'}
      when: [{input: $(params.env), operator: in, values: ['$(params.arr[*])']}]
`, be, map[string]tektontypes.ParamValue{
		"arr": {Type: tektontypes.ParamTypeArray, ArrayVal: []string{"dev", "prod"}},
		"env": {Type: tektontypes.ParamTypeString, StringVal: "prod"},
	}, nil)
	if res.Status != "succeeded" {
		t.Fatalf("status = %s (%+v)", res.Status, res.Tasks)
	}
	if got := be.inv(t, "consume").Task.Steps[0].Script; got != "echo b v r1 sha256:abc" {
		t.Errorf("script = %q", got)
	}
}

// C1.5: the $(context.*) variables Tekton provides.
func TestContextVars(t *testing.T) {
	be := &recordingBackend{outcome: func(inv backend.TaskInvocation, attempt int) backend.TaskResult {
		if inv.TaskName == "a" && attempt < 3 {
			return backend.TaskResult{Status: backend.TaskFailed}
		}
		return backend.TaskResult{Status: backend.TaskSucceeded}
	}}
	res := runSubst(t, `
apiVersion: tekton.dev/v1
kind: Task
metadata: {name: my-task}
spec:
  params: [{name: retries, default: ''}]
  steps:
    - name: s
      image: alpine
      script: |
        $(context.task.name)|$(context.task.retry-count)|$(params.retries)
        $(context.taskRun.name)|$(context.taskRun.namespace)|$(context.taskRun.uid)
        $(context.pipelineRun.name)|$(context.pipelineRun.namespace)|$(context.pipelineRun.uid)|$(context.pipeline.name)
---
apiVersion: tekton.dev/v1
kind: Pipeline
metadata: {name: p}
spec:
  tasks:
    - name: a
      retries: 2
      taskRef: {name: my-task}
      params: [{name: retries, value: '$(context.pipelineTask.retries)'}]
    - name: inline
      taskSpec:
        steps: [{name: s, image: alpine, script: '$(context.task.name)'}]
`, be, nil, nil)
	if res.Status != "succeeded" {
		t.Fatalf("status = %s (%+v)", res.Status, res.Tasks)
	}
	invs := be.invs["a"]
	if len(invs) != 3 {
		t.Fatalf("attempts = %d, want 3", len(invs))
	}
	uidPat := regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
	var firstTRUID string
	for i, inv := range invs {
		lines := strings.Split(strings.TrimSpace(inv.Task.Steps[0].Script), "\n")
		if len(lines) != 3 {
			t.Fatalf("script = %q", inv.Task.Steps[0].Script)
		}
		l0 := strings.Split(lines[0], "|")
		if l0[0] != "my-task" || l0[1] != string(rune('0'+i)) || l0[2] != "2" {
			t.Errorf("attempt %d: line0 = %q", i+1, lines[0])
		}
		l1 := strings.Split(lines[1], "|")
		if l1[0] != inv.TaskRunName || l1[1] != "default" || !uidPat.MatchString(l1[2]) {
			t.Errorf("attempt %d: line1 = %q", i+1, lines[1])
		}
		if firstTRUID == "" {
			firstTRUID = l1[2]
		} else if l1[2] != firstTRUID {
			t.Errorf("taskRun.uid must be stable across retries: %q vs %q", l1[2], firstTRUID)
		}
		l2 := strings.Split(lines[2], "|")
		if l2[0] != inv.PipelineRun || l2[1] != "default" || !uidPat.MatchString(l2[2]) || l2[2] == l1[2] || l2[3] != "p" {
			t.Errorf("attempt %d: line2 = %q", i+1, lines[2])
		}
	}
	// Inline taskSpec: Tekton's context.task.name is the TaskRun name.
	inl := be.inv(t, "inline")
	if got := inl.Task.Steps[0].Script; got != inl.TaskRunName {
		t.Errorf("inline task.name = %q, want %q", got, inl.TaskRunName)
	}
}
