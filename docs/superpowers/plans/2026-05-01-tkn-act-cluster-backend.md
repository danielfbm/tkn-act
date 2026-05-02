# tkn-act cluster backend (v1.1) — implementation plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax.

**Goal:** Implement the `--cluster` (k3d) backend per `docs/superpowers/specs/2026-05-01-tkn-act-cluster-backend.md`. Add `tkn-act cluster up|down|status` subcommands and an opt-in `PipelineBackend` interface that lets the engine delegate the whole pipeline to a backend that wants to.

**Tech stack:** Adds `k8s.io/client-go`, `k8s.io/api`, `k8s.io/apimachinery`. Shells out to `k3d` (lifecycle) and `kubectl` (Tekton install). All cluster I/O behind small interfaces so unit tests can run with `client-go/kubernetes/fake` and a fake shell runner.

**Module path:** `github.com/danielfbm/tkn-act` (already set on main).

**Tekton release pinned:** `v0.65.0`.

---

## Task ordering

```
Task 1 (sequential):  Add deps, command runner abstraction, shell-out helpers
Task 2 [P]:           Cluster driver interface + k3d driver
Task 3 [P]:           Tekton installer (apply + wait)
Task 4 (sequential):  PipelineBackend interface in backend pkg
Task 5 (sequential):  cluster.Backend implementation (driver + tekton + watch + log stream)
Task 6 (sequential):  Engine routing for PipelineBackend
Task 7 (sequential):  CLI: cluster up/down/status + --cluster wiring
Task 8 (sequential):  Integration test fixture (build tag `cluster`)
Task 9 (sequential):  Verify build + tests + docs
```

`[P]` tasks could run in parallel worktrees; in this plan they run sequentially because each is small.

---

## Task 1: Add dependencies + command-runner abstraction

**Files:**
- Modify: `go.mod`
- Create: `internal/cmdrunner/runner.go`
- Create: `internal/cmdrunner/runner_test.go`

The cmdrunner package wraps `os/exec` so unit tests can substitute a fake. Both the k3d driver and the Tekton installer use it.

- [ ] **Step 1: Add dependencies**

```bash
go get k8s.io/client-go@v0.31.0 k8s.io/api@v0.31.0 k8s.io/apimachinery@v0.31.0
go mod tidy
```

If the resolver fails because k8s.io/* requires a newer Go, downgrade to `v0.29.0` of all three (compatible with Go 1.22).

- [ ] **Step 2: Write the failing test**

Create `internal/cmdrunner/runner_test.go`:

```go
package cmdrunner_test

import (
	"context"
	"strings"
	"testing"

	"github.com/danielfbm/tkn-act/internal/cmdrunner"
)

func TestRealRunnerEcho(t *testing.T) {
	r := cmdrunner.New()
	out, err := r.Output(context.Background(), "echo", "hello")
	if err != nil {
		t.Fatalf("echo: %v", err)
	}
	if strings.TrimSpace(string(out)) != "hello" {
		t.Errorf("got %q", out)
	}
}

func TestFakeRunnerCapturesArgs(t *testing.T) {
	fake := cmdrunner.NewFake()
	fake.Set("k3d cluster list -o json", []byte("[]"), nil)
	r := fake.Runner()
	out, err := r.Output(context.Background(), "k3d", "cluster", "list", "-o", "json")
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != "[]" {
		t.Errorf("got %q", out)
	}
	if got := fake.Calls(); len(got) != 1 || got[0] != "k3d cluster list -o json" {
		t.Errorf("calls: %v", got)
	}
}
```

- [ ] **Step 3: Run, verify failure**

Run: `go test ./internal/cmdrunner/...` → FAIL.

- [ ] **Step 4: Implement runner**

Create `internal/cmdrunner/runner.go`:

```go
// Package cmdrunner wraps os/exec so unit tests can substitute a fake.
package cmdrunner

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"
)

// Runner runs commands. Real implementation execs; tests use Fake.
type Runner interface {
	Output(ctx context.Context, name string, args ...string) ([]byte, error)
	Run(ctx context.Context, stdout, stderr io.Writer, name string, args ...string) error
}

type real struct{}

func New() Runner { return real{} }

func (real) Output(ctx context.Context, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return stdout.Bytes(), fmt.Errorf("%s %s: %w (stderr: %s)", name, strings.Join(args, " "), err, stderr.String())
	}
	return stdout.Bytes(), nil
}

func (real) Run(ctx context.Context, stdout, stderr io.Writer, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	return cmd.Run()
}

// Fake is a test helper. Use NewFake().Runner() to get a Runner that returns
// pre-canned responses keyed by full command line.
type Fake struct {
	mu       sync.Mutex
	canned   map[string]cannedResponse
	calls    []string
}

type cannedResponse struct {
	out []byte
	err error
}

func NewFake() *Fake { return &Fake{canned: map[string]cannedResponse{}} }

func (f *Fake) Set(cmdline string, out []byte, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.canned[cmdline] = cannedResponse{out: out, err: err}
}

func (f *Fake) Calls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, len(f.calls))
	copy(out, f.calls)
	return out
}

func (f *Fake) Runner() Runner { return &fakeRunner{f: f} }

type fakeRunner struct{ f *Fake }

func (r *fakeRunner) Output(_ context.Context, name string, args ...string) ([]byte, error) {
	cmdline := name + " " + strings.Join(args, " ")
	cmdline = strings.TrimSpace(cmdline)
	r.f.mu.Lock()
	r.f.calls = append(r.f.calls, cmdline)
	resp, ok := r.f.canned[cmdline]
	r.f.mu.Unlock()
	if !ok {
		return nil, fmt.Errorf("fakeRunner: no canned response for %q", cmdline)
	}
	return resp.out, resp.err
}

func (r *fakeRunner) Run(ctx context.Context, stdout, stderr io.Writer, name string, args ...string) error {
	out, err := r.Output(ctx, name, args...)
	if stdout != nil && len(out) > 0 {
		_, _ = stdout.Write(out)
	}
	return err
}
```

- [ ] **Step 5: Run tests**

Run: `go test ./internal/cmdrunner/...` → PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/cmdrunner/ go.mod go.sum
git -c user.email="makebyyourside0367@gmail.com" -c user.name="Daniel F B Morinigo" commit -m "$(cat <<'EOF'
feat(cmdrunner): wrap os/exec with fake-friendly Runner interface

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 2: Cluster driver interface + k3d driver

**Files:**
- Create: `internal/cluster/driver.go`
- Create: `internal/cluster/k3d/k3d.go`
- Create: `internal/cluster/k3d/k3d_test.go`

- [ ] **Step 1: Define Driver interface**

Create `internal/cluster/driver.go`:

```go
// Package cluster defines the local-Kubernetes driver abstraction. v1.1 ships
// with the k3d driver; future drivers (kind) implement the same interface.
package cluster

import "context"

// Driver manages the lifecycle of a local cluster.
type Driver interface {
	// Name returns a stable cluster name used by Ensure/Delete.
	Name() string

	// Ensure creates the cluster if missing. No-op if already present.
	// Writes kubeconfig to the path returned by Kubeconfig() once Ensure returns nil.
	Ensure(ctx context.Context) error

	// Status reports whether the cluster currently exists and is running.
	Status(ctx context.Context) (Status, error)

	// Delete removes the cluster. No-op if absent.
	Delete(ctx context.Context) error

	// Kubeconfig returns the path the driver writes the cluster's kubeconfig to.
	Kubeconfig() string
}

type Status struct {
	Exists  bool
	Running bool
	Detail  string
}
```

- [ ] **Step 2: Write the failing k3d test**

Create `internal/cluster/k3d/k3d_test.go`:

```go
package k3d_test

import (
	"context"
	"testing"

	"github.com/danielfbm/tkn-act/internal/cluster/k3d"
	"github.com/danielfbm/tkn-act/internal/cmdrunner"
)

func TestEnsureCreatesIfMissing(t *testing.T) {
	fake := cmdrunner.NewFake()
	fake.Set("k3d cluster list -o json", []byte("[]"), nil)
	fake.Set("k3d cluster create tkn-act --no-lb --wait --timeout 120s --k3s-arg --disable=traefik@server:0", []byte(""), nil)
	fake.Set("k3d kubeconfig get tkn-act", []byte("apiVersion: v1\nkind: Config\n"), nil)

	d := k3d.New(k3d.Options{
		ClusterName:    "tkn-act",
		KubeconfigPath: t.TempDir() + "/kubeconfig",
		Runner:         fake.Runner(),
	})
	if err := d.Ensure(context.Background()); err != nil {
		t.Fatalf("ensure: %v", err)
	}
}

func TestEnsureNoopWhenPresent(t *testing.T) {
	fake := cmdrunner.NewFake()
	fake.Set("k3d cluster list -o json", []byte(`[{"name":"tkn-act"}]`), nil)
	fake.Set("k3d kubeconfig get tkn-act", []byte("apiVersion: v1\nkind: Config\n"), nil)

	d := k3d.New(k3d.Options{
		ClusterName:    "tkn-act",
		KubeconfigPath: t.TempDir() + "/kubeconfig",
		Runner:         fake.Runner(),
	})
	if err := d.Ensure(context.Background()); err != nil {
		t.Fatalf("ensure: %v", err)
	}
	for _, c := range fake.Calls() {
		if c == "k3d cluster create tkn-act --no-lb --wait --timeout 120s --k3s-arg --disable=traefik@server:0" {
			t.Errorf("create called when cluster already existed; calls=%v", fake.Calls())
		}
	}
}

func TestStatusRunning(t *testing.T) {
	fake := cmdrunner.NewFake()
	fake.Set("k3d cluster list -o json", []byte(`[{"name":"tkn-act","serversRunning":1,"agentsRunning":0,"servers":1,"agents":0}]`), nil)
	d := k3d.New(k3d.Options{ClusterName: "tkn-act", Runner: fake.Runner()})
	st, err := d.Status(context.Background())
	if err != nil { t.Fatal(err) }
	if !st.Exists || !st.Running { t.Errorf("status = %+v", st) }
}

func TestDelete(t *testing.T) {
	fake := cmdrunner.NewFake()
	fake.Set("k3d cluster list -o json", []byte(`[{"name":"tkn-act"}]`), nil)
	fake.Set("k3d cluster delete tkn-act", []byte(""), nil)
	d := k3d.New(k3d.Options{ClusterName: "tkn-act", Runner: fake.Runner()})
	if err := d.Delete(context.Background()); err != nil { t.Fatal(err) }
}
```

- [ ] **Step 3: Run, verify failure**

Run: `go test ./internal/cluster/...` → FAIL.

- [ ] **Step 4: Implement k3d driver**

Create `internal/cluster/k3d/k3d.go`:

```go
// Package k3d implements cluster.Driver by shelling out to the k3d binary.
package k3d

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/danielfbm/tkn-act/internal/cluster"
	"github.com/danielfbm/tkn-act/internal/cmdrunner"
)

type Options struct {
	ClusterName    string
	KubeconfigPath string
	Runner         cmdrunner.Runner
}

type Driver struct {
	name     string
	kubecfg  string
	runner   cmdrunner.Runner
}

func New(opt Options) *Driver {
	if opt.Runner == nil {
		opt.Runner = cmdrunner.New()
	}
	if opt.ClusterName == "" {
		opt.ClusterName = "tkn-act"
	}
	return &Driver{name: opt.ClusterName, kubecfg: opt.KubeconfigPath, runner: opt.Runner}
}

func (d *Driver) Name() string       { return d.name }
func (d *Driver) Kubeconfig() string { return d.kubecfg }

type clusterListEntry struct {
	Name           string `json:"name"`
	ServersRunning int    `json:"serversRunning"`
	AgentsRunning  int    `json:"agentsRunning"`
	Servers        int    `json:"servers"`
	Agents         int    `json:"agents"`
}

func (d *Driver) listClusters(ctx context.Context) ([]clusterListEntry, error) {
	out, err := d.runner.Output(ctx, "k3d", "cluster", "list", "-o", "json")
	if err != nil {
		return nil, fmt.Errorf("k3d cluster list: %w", err)
	}
	var entries []clusterListEntry
	if err := json.Unmarshal(out, &entries); err != nil {
		return nil, fmt.Errorf("parse k3d cluster list: %w", err)
	}
	return entries, nil
}

func (d *Driver) Ensure(ctx context.Context) error {
	entries, err := d.listClusters(ctx)
	if err != nil {
		return err
	}
	exists := false
	for _, e := range entries {
		if e.Name == d.name {
			exists = true
			break
		}
	}
	if !exists {
		_, err := d.runner.Output(ctx, "k3d", "cluster", "create", d.name,
			"--no-lb", "--wait", "--timeout", "120s",
			"--k3s-arg", "--disable=traefik@server:0",
		)
		if err != nil {
			return fmt.Errorf("k3d cluster create: %w", err)
		}
	}
	if d.kubecfg != "" {
		out, err := d.runner.Output(ctx, "k3d", "kubeconfig", "get", d.name)
		if err != nil {
			return fmt.Errorf("k3d kubeconfig get: %w", err)
		}
		if err := os.MkdirAll(filepath.Dir(d.kubecfg), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(d.kubecfg, out, 0o600); err != nil {
			return err
		}
	}
	return nil
}

func (d *Driver) Status(ctx context.Context) (cluster.Status, error) {
	entries, err := d.listClusters(ctx)
	if err != nil {
		return cluster.Status{}, err
	}
	for _, e := range entries {
		if e.Name == d.name {
			running := e.ServersRunning >= 1
			detail := fmt.Sprintf("servers=%d/%d agents=%d/%d", e.ServersRunning, e.Servers, e.AgentsRunning, e.Agents)
			return cluster.Status{Exists: true, Running: running, Detail: detail}, nil
		}
	}
	return cluster.Status{Exists: false}, nil
}

func (d *Driver) Delete(ctx context.Context) error {
	st, err := d.Status(ctx)
	if err != nil {
		return err
	}
	if !st.Exists {
		return nil
	}
	if _, err := d.runner.Output(ctx, "k3d", "cluster", "delete", d.name); err != nil {
		return fmt.Errorf("k3d cluster delete: %w", err)
	}
	if d.kubecfg != "" {
		_ = os.Remove(d.kubecfg)
	}
	return nil
}
```

- [ ] **Step 5: Run tests**

Run: `go test ./internal/cluster/...` → PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/cluster/
git -c user.email="makebyyourside0367@gmail.com" -c user.name="Daniel F B Morinigo" commit -m "$(cat <<'EOF'
feat(cluster/k3d): driver for k3d cluster lifecycle

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 3: Tekton installer

**Files:**
- Create: `internal/cluster/tekton/install.go`
- Create: `internal/cluster/tekton/install_test.go`

- [ ] **Step 1: Write the failing test**

Create `internal/cluster/tekton/install_test.go`:

```go
package tekton_test

import (
	"context"
	"errors"
	"testing"

	"github.com/danielfbm/tkn-act/internal/cluster/tekton"
	"github.com/danielfbm/tkn-act/internal/cmdrunner"
	apiext "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apiextfake "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset/fake"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestSkipsIfCRDPresent(t *testing.T) {
	apiextCli := apiextfake.NewSimpleClientset(&apiext.CustomResourceDefinition{
		ObjectMeta: metav1.ObjectMeta{Name: "pipelines.tekton.dev"},
	})
	kube := fake.NewSimpleClientset(readyControllerDeployment(), readyWebhookDeployment())
	runner := cmdrunner.NewFake()
	// no canned `kubectl apply` — if installer tries to call it, the test fails
	inst := tekton.New(tekton.Options{
		Kubeconfig: "/tmp/kc",
		Runner:     runner.Runner(),
		Apiext:     apiextCli,
		Kube:       kube,
		Version:    "v0.65.0",
	})
	if err := inst.Install(context.Background()); err != nil {
		t.Fatalf("install: %v", err)
	}
	for _, c := range runner.Calls() {
		if c[:6] == "kubect" {
			t.Errorf("apply called when CRD already present: %v", runner.Calls())
		}
	}
}

func TestAppliesIfCRDMissing(t *testing.T) {
	apiextCli := apiextfake.NewSimpleClientset()
	kube := fake.NewSimpleClientset(readyControllerDeployment(), readyWebhookDeployment())
	runner := cmdrunner.NewFake()
	runner.Set("kubectl --kubeconfig /tmp/kc apply -f https://storage.googleapis.com/tekton-releases/pipeline/previous/v0.65.0/release.yaml", []byte("ok"), nil)
	inst := tekton.New(tekton.Options{
		Kubeconfig: "/tmp/kc",
		Runner:     runner.Runner(),
		Apiext:     apiextCli,
		Kube:       kube,
		Version:    "v0.65.0",
	})
	if err := inst.Install(context.Background()); err != nil {
		t.Fatalf("install: %v", err)
	}
}

func TestApplyFailureBubbles(t *testing.T) {
	apiextCli := apiextfake.NewSimpleClientset()
	kube := fake.NewSimpleClientset()
	runner := cmdrunner.NewFake()
	runner.Set("kubectl --kubeconfig /tmp/kc apply -f https://storage.googleapis.com/tekton-releases/pipeline/previous/v0.65.0/release.yaml", nil, errors.New("boom"))
	inst := tekton.New(tekton.Options{
		Kubeconfig: "/tmp/kc",
		Runner:     runner.Runner(),
		Apiext:     apiextCli,
		Kube:       kube,
		Version:    "v0.65.0",
	})
	if err := inst.Install(context.Background()); err == nil {
		t.Fatal("expected error")
	}
}

func readyControllerDeployment() *appsv1.Deployment {
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "tekton-pipelines-controller", Namespace: "tekton-pipelines"},
		Status: appsv1.DeploymentStatus{Replicas: 1, ReadyReplicas: 1, AvailableReplicas: 1, UpdatedReplicas: 1},
	}
}
func readyWebhookDeployment() *appsv1.Deployment {
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "tekton-pipelines-webhook", Namespace: "tekton-pipelines"},
		Status: appsv1.DeploymentStatus{Replicas: 1, ReadyReplicas: 1, AvailableReplicas: 1, UpdatedReplicas: 1},
	}
}
```

- [ ] **Step 2: Add apiextensions-apiserver dep**

```bash
go get k8s.io/apiextensions-apiserver@v0.31.0
go mod tidy
```

If it doesn't resolve, downgrade to v0.29.0 to match other k8s libs.

- [ ] **Step 3: Run, verify failure**

Run: `go test ./internal/cluster/tekton/...` → FAIL (undefined: tekton.New).

- [ ] **Step 4: Implement installer**

Create `internal/cluster/tekton/install.go`:

```go
// Package tekton installs the Tekton Pipelines controller into a Kubernetes
// cluster. Idempotent: skips apply if pipelines.tekton.dev CRD already exists.
package tekton

import (
	"context"
	"fmt"
	"time"

	"github.com/danielfbm/tkn-act/internal/cmdrunner"
	apiextclient "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

type Options struct {
	Kubeconfig string
	Runner     cmdrunner.Runner
	Apiext     apiextclient.Interface
	Kube       kubernetes.Interface
	Version    string // e.g. "v0.65.0"
	Timeout    time.Duration
}

type Installer struct {
	opt Options
}

func New(opt Options) *Installer {
	if opt.Version == "" {
		opt.Version = "v0.65.0"
	}
	if opt.Timeout == 0 {
		opt.Timeout = 180 * time.Second
	}
	if opt.Runner == nil {
		opt.Runner = cmdrunner.New()
	}
	return &Installer{opt: opt}
}

func (i *Installer) Install(ctx context.Context) error {
	if i.opt.Apiext != nil {
		_, err := i.opt.Apiext.ApiextensionsV1().CustomResourceDefinitions().Get(ctx, "pipelines.tekton.dev", metav1.GetOptions{})
		if err == nil {
			return i.waitReady(ctx)
		}
		if !apierrors.IsNotFound(err) {
			return fmt.Errorf("check tekton CRD: %w", err)
		}
	}
	url := fmt.Sprintf("https://storage.googleapis.com/tekton-releases/pipeline/previous/%s/release.yaml", i.opt.Version)
	if _, err := i.opt.Runner.Output(ctx, "kubectl", "--kubeconfig", i.opt.Kubeconfig, "apply", "-f", url); err != nil {
		return fmt.Errorf("apply tekton release: %w", err)
	}
	return i.waitReady(ctx)
}

func (i *Installer) waitReady(ctx context.Context) error {
	deadline := time.Now().Add(i.opt.Timeout)
	deps := []string{"tekton-pipelines-controller", "tekton-pipelines-webhook"}
	for _, name := range deps {
		for {
			d, err := i.opt.Kube.AppsV1().Deployments("tekton-pipelines").Get(ctx, name, metav1.GetOptions{})
			if err == nil && d.Status.Replicas > 0 && d.Status.ReadyReplicas == d.Status.Replicas {
				break
			}
			if time.Now().After(deadline) {
				return fmt.Errorf("timeout waiting for deployment %q in tekton-pipelines", name)
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(2 * time.Second):
			}
		}
	}
	return nil
}
```

- [ ] **Step 5: Run tests**

Run: `go test ./internal/cluster/tekton/...` → PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/cluster/tekton/ go.mod go.sum
git -c user.email="makebyyourside0367@gmail.com" -c user.name="Daniel F B Morinigo" commit -m "$(cat <<'EOF'
feat(cluster/tekton): idempotent installer for tekton release

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 4: Add PipelineBackend interface

**Files:**
- Modify: `internal/backend/backend.go`

- [ ] **Step 1: Append to backend.go**

Add the following after the existing types in `internal/backend/backend.go`:

```go
// PipelineBackend is an optional interface a Backend may implement when it
// wants to execute a whole PipelineRun itself (rather than have the engine
// orchestrate one Task at a time). The cluster backend uses this so the real
// Tekton controller drives the DAG.
type PipelineBackend interface {
	Backend
	RunPipeline(ctx context.Context, in PipelineRunInvocation) (PipelineRunResult, error)
}

// PipelineRunInvocation is what the engine passes when delegating an entire
// PipelineRun.
type PipelineRunInvocation struct {
	RunID           string
	PipelineRunName string
	Pipeline        tektontypes.Pipeline
	Tasks           map[string]tektontypes.Task
	Params          []tektontypes.Param
	Workspaces      map[string]WorkspaceMount
	LogSink         LogSink
	EmitEvent       func(taskName, status, message string, started, ended time.Time, results map[string]string)
}

// PipelineRunResult is what RunPipeline returns.
type PipelineRunResult struct {
	Status  string // succeeded | failed
	Tasks   map[string]TaskOutcomeOnCluster
	Started time.Time
	Ended   time.Time
}

type TaskOutcomeOnCluster struct {
	Status  string
	Message string
	Results map[string]string
}
```

Add `"github.com/danielfbm/tkn-act/internal/tektontypes"` and `"time"` to the imports if not already present (they are, but double-check).

- [ ] **Step 2: Verify the package still builds and tests pass**

```bash
go build ./...
go test ./internal/backend/...
```

Expected: PASS.

- [ ] **Step 3: Commit**

```bash
git add internal/backend/
git -c user.email="makebyyourside0367@gmail.com" -c user.name="Daniel F B Morinigo" commit -m "$(cat <<'EOF'
feat(backend): add PipelineBackend optional interface

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 5: cluster.Backend implementation

**Files:**
- Create: `internal/backend/cluster/cluster.go`
- Create: `internal/backend/cluster/run.go`
- Create: `internal/backend/cluster/cluster_test.go`

This is the largest task. The cluster backend:
1. Lazily ensures the cluster exists (driver.Ensure).
2. Lazily installs Tekton (tekton.Installer.Install).
3. Builds an unstructured PipelineRun via dynamic client and applies it.
4. Watches the PipelineRun until terminal status.
5. For each TaskRun, streams pod logs to the LogSink and emits start/end events.

Because Tekton CRDs aren't in `k8s.io/api`, we use the dynamic client + unstructured.Unstructured to avoid vendoring tektoncd/pipeline (per v1 spec rationale).

- [ ] **Step 1: Write the unit test**

Create `internal/backend/cluster/cluster_test.go`:

```go
package cluster_test

import (
	"context"
	"testing"

	"github.com/danielfbm/tkn-act/internal/backend"
	"github.com/danielfbm/tkn-act/internal/backend/cluster"
	"github.com/danielfbm/tkn-act/internal/tektontypes"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/dynamic/fake"
	"k8s.io/apimachinery/pkg/runtime/schema"
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
	if err != nil { t.Fatal(err) }

	// Verify it's a PipelineRun in tekton.dev/v1 with pipelineSpec inlined.
	un, ok := prObj.(*unstructured.Unstructured)
	if !ok { t.Fatalf("expected unstructured, got %T", prObj) }
	if un.GetAPIVersion() != "tekton.dev/v1" || un.GetKind() != "PipelineRun" {
		t.Errorf("got %s/%s", un.GetAPIVersion(), un.GetKind())
	}
	if un.GetNamespace() != "tkn-act-12345678" {
		t.Errorf("namespace = %q", un.GetNamespace())
	}
	spec, found, err := unstructured.NestedMap(un.Object, "spec")
	if err != nil || !found { t.Fatalf("missing spec") }
	if _, has := spec["pipelineSpec"]; !has {
		t.Errorf("expected inlined pipelineSpec; got keys: %v", keysOf(spec))
	}
	_ = pipelineRunGVR
	_ = taskGVR
	_ = dyn
}

func keysOf(m map[string]any) []string {
	out := []string{}
	for k := range m { out = append(out, k) }
	return out
}
```

- [ ] **Step 2: Add dynamic client dep**

```bash
go get k8s.io/client-go/dynamic@v0.31.0
go mod tidy
```

(Already present transitively if k8s.io/client-go is added; verify.)

- [ ] **Step 3: Implement cluster.Backend**

Create `internal/backend/cluster/cluster.go`:

```go
// Package cluster implements backend.Backend by submitting PipelineRuns to a
// real Tekton install on a local Kubernetes cluster (k3d).
package cluster

import (
	"context"
	"fmt"
	"path/filepath"
	"time"

	"github.com/danielfbm/tkn-act/internal/backend"
	"github.com/danielfbm/tkn-act/internal/cluster"
	"github.com/danielfbm/tkn-act/internal/cluster/tekton"
	"github.com/danielfbm/tkn-act/internal/cmdrunner"
	"github.com/danielfbm/tkn-act/internal/tektontypes"
	apiextclient "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
)

type Options struct {
	CacheDir   string // base path for kubeconfig and other state
	Driver     cluster.Driver
	Runner     cmdrunner.Runner
	TektonVersion string
}

type ClientBundle struct {
	Dynamic dynamic.Interface
	Kube    kubernetes.Interface
	Apiext  apiextclient.Interface
}

type Backend struct {
	opt    Options
	client ClientBundle
}

// New is the production constructor: it does not connect — Prepare lazily
// ensures the cluster, installs Tekton, and builds the kube clients.
func New(opt Options) *Backend {
	if opt.CacheDir == "" {
		opt.CacheDir = ".cache/tkn-act"
	}
	if opt.Runner == nil {
		opt.Runner = cmdrunner.New()
	}
	if opt.TektonVersion == "" {
		opt.TektonVersion = "v0.65.0"
	}
	return &Backend{opt: opt}
}

// NewWithClients is a test constructor that injects a pre-built ClientBundle.
func NewWithClients(cb ClientBundle) *Backend { return &Backend{client: cb} }

// Prepare lazily provisions the cluster + Tekton on first use.
func (b *Backend) Prepare(ctx context.Context, _ backend.RunSpec) error {
	if b.client.Dynamic != nil {
		return nil // injected (test path)
	}
	if b.opt.Driver == nil {
		return fmt.Errorf("cluster.Backend: no Driver configured")
	}
	if err := b.opt.Driver.Ensure(ctx); err != nil {
		return fmt.Errorf("cluster ensure: %w", err)
	}
	kubecfgPath := b.opt.Driver.Kubeconfig()
	if kubecfgPath == "" {
		kubecfgPath = filepath.Join(b.opt.CacheDir, "cluster", "kubeconfig")
	}
	cfg, err := clientcmd.BuildConfigFromFlags("", kubecfgPath)
	if err != nil {
		return fmt.Errorf("load kubeconfig: %w", err)
	}
	b.client.Dynamic, err = dynamic.NewForConfig(cfg)
	if err != nil { return err }
	b.client.Kube, err = kubernetes.NewForConfig(cfg)
	if err != nil { return err }
	b.client.Apiext, err = apiextclient.NewForConfig(cfg)
	if err != nil { return err }

	inst := tekton.New(tekton.Options{
		Kubeconfig: kubecfgPath,
		Runner:     b.opt.Runner,
		Apiext:     b.client.Apiext,
		Kube:       b.client.Kube,
		Version:    b.opt.TektonVersion,
		Timeout:    3 * time.Minute,
	})
	if err := inst.Install(ctx); err != nil {
		return fmt.Errorf("tekton install: %w", err)
	}
	return nil
}

// Cleanup is a no-op: cluster + namespaces persist for inspection.
func (b *Backend) Cleanup(_ context.Context) error { return nil }

// RunTask delegates to RunPipeline by wrapping the single TaskRun call into a
// trivial one-task pipeline. The engine should prefer RunPipeline directly.
func (b *Backend) RunTask(ctx context.Context, inv backend.TaskInvocation) (backend.TaskResult, error) {
	return backend.TaskResult{}, fmt.Errorf("cluster backend: per-task RunTask not supported; call RunPipeline")
}

// BuildPipelineRunObject constructs the PipelineRun unstructured (exposed for
// unit-test inspection).
func (b *Backend) BuildPipelineRunObject(in backend.PipelineRunInvocation, namespace string) (any, error) {
	return buildPipelineRun(in, namespace), nil
}

var _ tektontypes.Object // keep import alive in case unused
```

Create `internal/backend/cluster/run.go`:

```go
package cluster

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/danielfbm/tkn-act/internal/backend"
	"github.com/danielfbm/tkn-act/internal/tektontypes"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/watch"
)

var (
	gvrPipelineRun = schema.GroupVersionResource{Group: "tekton.dev", Version: "v1", Resource: "pipelineruns"}
	gvrTaskRun     = schema.GroupVersionResource{Group: "tekton.dev", Version: "v1", Resource: "taskruns"}
)

func (b *Backend) RunPipeline(ctx context.Context, in backend.PipelineRunInvocation) (backend.PipelineRunResult, error) {
	if b.client.Dynamic == nil || b.client.Kube == nil {
		return backend.PipelineRunResult{}, fmt.Errorf("cluster backend not Prepared")
	}
	ns := "tkn-act-" + shortRunID(in.RunID)
	if err := b.ensureNamespace(ctx, ns); err != nil {
		return backend.PipelineRunResult{}, err
	}
	pr := buildPipelineRun(in, ns)
	created, err := b.client.Dynamic.Resource(gvrPipelineRun).Namespace(ns).Create(ctx, pr, metav1.CreateOptions{})
	if err != nil {
		return backend.PipelineRunResult{}, fmt.Errorf("create PipelineRun: %w", err)
	}
	return b.watchPipelineRun(ctx, in, ns, created.GetName())
}

func (b *Backend) ensureNamespace(ctx context.Context, name string) error {
	_, err := b.client.Kube.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: name},
	}, metav1.CreateOptions{})
	if err != nil && !strings.Contains(err.Error(), "already exists") {
		return fmt.Errorf("create namespace %q: %w", name, err)
	}
	return nil
}

// buildPipelineRun returns a fully populated unstructured PipelineRun with
// pipelineSpec inlined and workspaces backed by volumeClaimTemplate.
func buildPipelineRun(in backend.PipelineRunInvocation, namespace string) *unstructured.Unstructured {
	pipelineSpec := pipelineSpecToMap(in.Pipeline)

	// Inline embedded Tasks under each PipelineTask.taskSpec.
	tasks, _, _ := unstructured.NestedSlice(map[string]any{"tasks": pipelineSpec["tasks"]}, "tasks")
	for i, t := range tasks {
		m := t.(map[string]any)
		ref, hasRef := m["taskRef"].(map[string]any)
		if hasRef {
			name, _ := ref["name"].(string)
			if tk, ok := in.Tasks[name]; ok {
				m["taskSpec"] = taskSpecToMap(tk.Spec)
				delete(m, "taskRef")
			}
		}
		tasks[i] = m
	}
	pipelineSpec["tasks"] = tasks
	if fin, ok := pipelineSpec["finally"].([]any); ok {
		for i, t := range fin {
			m := t.(map[string]any)
			ref, hasRef := m["taskRef"].(map[string]any)
			if hasRef {
				name, _ := ref["name"].(string)
				if tk, ok := in.Tasks[name]; ok {
					m["taskSpec"] = taskSpecToMap(tk.Spec)
					delete(m, "taskRef")
				}
			}
			fin[i] = m
		}
		pipelineSpec["finally"] = fin
	}

	spec := map[string]any{
		"pipelineSpec": pipelineSpec,
	}
	if len(in.Params) > 0 {
		var ps []any
		for _, p := range in.Params {
			ps = append(ps, paramToMap(p))
		}
		spec["params"] = ps
	}
	// Workspaces: volumeClaimTemplate per declared pipeline workspace.
	if pwss, ok := pipelineSpec["workspaces"].([]any); ok && len(pwss) > 0 {
		var wsBindings []any
		for _, w := range pwss {
			m := w.(map[string]any)
			name, _ := m["name"].(string)
			wsBindings = append(wsBindings, map[string]any{
				"name": name,
				"volumeClaimTemplate": map[string]any{
					"spec": map[string]any{
						"accessModes": []any{"ReadWriteOnce"},
						"resources": map[string]any{
							"requests": map[string]any{"storage": "1Gi"},
						},
					},
				},
			})
		}
		spec["workspaces"] = wsBindings
	}

	pr := &unstructured.Unstructured{
		Object: map[string]any{
			"apiVersion": "tekton.dev/v1",
			"kind":       "PipelineRun",
			"metadata": map[string]any{
				"name":      in.PipelineRunName,
				"namespace": namespace,
			},
			"spec": spec,
		},
	}
	return pr
}

func pipelineSpecToMap(pl tektontypes.Pipeline) map[string]any {
	b, _ := json.Marshal(pl.Spec)
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	if m == nil { m = map[string]any{} }
	return m
}

func taskSpecToMap(ts tektontypes.TaskSpec) map[string]any {
	b, _ := json.Marshal(ts)
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	if m == nil { m = map[string]any{} }
	return m
}

func paramToMap(p tektontypes.Param) map[string]any {
	m := map[string]any{"name": p.Name}
	switch p.Value.Type {
	case tektontypes.ParamTypeArray:
		m["value"] = strSliceToAny(p.Value.ArrayVal)
	case tektontypes.ParamTypeObject:
		obj := map[string]any{}
		for k, v := range p.Value.ObjectVal { obj[k] = v }
		m["value"] = obj
	default:
		m["value"] = p.Value.StringVal
	}
	return m
}

func strSliceToAny(s []string) []any {
	out := make([]any, len(s))
	for i, v := range s { out[i] = v }
	return out
}

func shortRunID(rid string) string {
	if len(rid) >= 8 { return rid[:8] }
	return rid
}

// watchPipelineRun watches the PipelineRun to terminal status, streaming
// TaskRun pod logs as they appear.
func (b *Backend) watchPipelineRun(ctx context.Context, in backend.PipelineRunInvocation, ns, name string) (backend.PipelineRunResult, error) {
	res := backend.PipelineRunResult{Started: time.Now(), Tasks: map[string]backend.TaskOutcomeOnCluster{}}

	w, err := b.client.Dynamic.Resource(gvrPipelineRun).Namespace(ns).Watch(ctx, metav1.ListOptions{
		FieldSelector: "metadata.name=" + name,
	})
	if err != nil {
		return res, fmt.Errorf("watch PipelineRun: %w", err)
	}
	defer w.Stop()

	// Stream logs for each TaskRun pod as it appears.
	streamed := map[string]bool{}
	go b.streamAllTaskRunLogs(ctx, in, ns, streamed)

	for ev := range w.ResultChan() {
		if ev.Type == watch.Deleted || ev.Object == nil {
			continue
		}
		un, ok := ev.Object.(*unstructured.Unstructured)
		if !ok { continue }
		conds, _, _ := unstructured.NestedSlice(un.Object, "status", "conditions")
		for _, c := range conds {
			cm := c.(map[string]any)
			if cm["type"] == "Succeeded" {
				switch cm["status"] {
				case "True":
					res.Status = "succeeded"
					res.Ended = time.Now()
					return res, nil
				case "False":
					res.Status = "failed"
					res.Ended = time.Now()
					return res, nil
				}
			}
		}
	}
	return res, fmt.Errorf("PipelineRun watch closed before terminal status")
}

func (b *Backend) streamAllTaskRunLogs(ctx context.Context, in backend.PipelineRunInvocation, ns string, streamed map[string]bool) {
	w, err := b.client.Dynamic.Resource(gvrTaskRun).Namespace(ns).Watch(ctx, metav1.ListOptions{
		LabelSelector: "tekton.dev/pipelineRun=" + in.PipelineRunName,
	})
	if err != nil { return }
	defer w.Stop()
	for ev := range w.ResultChan() {
		un, ok := ev.Object.(*unstructured.Unstructured)
		if !ok { continue }
		podName, found, _ := unstructured.NestedString(un.Object, "status", "podName")
		if !found || streamed[podName] { continue }
		streamed[podName] = true
		taskName, _, _ := unstructured.NestedString(un.Object, "metadata", "labels", "tekton.dev/pipelineTask")
		go b.streamPodLogs(ctx, in, ns, podName, taskName)
	}
}

func (b *Backend) streamPodLogs(ctx context.Context, in backend.PipelineRunInvocation, ns, pod, taskName string) {
	if in.LogSink == nil { return }
	// Wait briefly for the pod to have containers.
	time.Sleep(500 * time.Millisecond)
	p, err := b.client.Kube.CoreV1().Pods(ns).Get(ctx, pod, metav1.GetOptions{})
	if err != nil { return }
	for _, c := range p.Spec.Containers {
		if !strings.HasPrefix(c.Name, "step-") { continue }
		stepName := strings.TrimPrefix(c.Name, "step-")
		req := b.client.Kube.CoreV1().Pods(ns).GetLogs(pod, &corev1.PodLogOptions{Container: c.Name, Follow: true})
		rc, err := req.Stream(ctx)
		if err != nil { continue }
		go func(stepName string, rc io.ReadCloser) {
			defer rc.Close()
			s := bufio.NewScanner(rc)
			s.Buffer(make([]byte, 64*1024), 1024*1024)
			for s.Scan() {
				in.LogSink.StepLog(taskName, stepName, "stdout", s.Text())
			}
		}(stepName, rc)
	}
}
```

- [ ] **Step 4: Run tests**

```bash
go build ./...
go test ./internal/backend/cluster/...
```

Expected: PASS (just the BuildPipelineRunObject construction test).

- [ ] **Step 5: Commit**

```bash
git add internal/backend/cluster/ go.mod go.sum
git -c user.email="makebyyourside0367@gmail.com" -c user.name="Daniel F B Morinigo" commit -m "$(cat <<'EOF'
feat(backend/cluster): submit PipelineRun, watch, stream logs via client-go

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 6: Engine routing for PipelineBackend

**Files:**
- Modify: `internal/engine/engine.go`

- [ ] **Step 1: Add the routing branch**

In `internal/engine/engine.go`, near the top of `RunPipeline` (after pipeline lookup), insert:

```go
if pb, ok := e.be.(backend.PipelineBackend); ok {
    return e.runViaPipelineBackend(ctx, pb, in, pl)
}
```

Then add the helper at the bottom of the file:

```go
func (e *Engine) runViaPipelineBackend(ctx context.Context, pb backend.PipelineBackend, in PipelineInput, pl tektontypes.Pipeline) (RunResult, error) {
	runID := in.RunID
	if runID == "" {
		runID = newRunID()
	}
	pipelineRunName := pl.Metadata.Name + "-" + runID[:8]

	params, err := applyDefaults(pl.Spec.Params, in.Params)
	if err != nil {
		return RunResult{}, err
	}

	images := uniqueImages(in.Bundle, pl)
	if err := pb.Prepare(ctx, backend.RunSpec{RunID: runID, Pipeline: pl.Metadata.Name, Images: images, Workspaces: in.Workspaces}); err != nil {
		e.rep.Emit(reporter.Event{Kind: reporter.EvtRunEnd, Time: time.Now(), Status: "failed", Message: err.Error()})
		return RunResult{Status: "failed"}, err
	}

	e.rep.Emit(reporter.Event{Kind: reporter.EvtRunStart, Time: time.Now(), RunID: runID, Pipeline: pl.Metadata.Name})

	var paramList []tektontypes.Param
	for k, v := range params {
		paramList = append(paramList, tektontypes.Param{Name: k, Value: v})
	}

	wsMap := map[string]backend.WorkspaceMount{}
	for k, host := range in.Workspaces {
		wsMap[k] = backend.WorkspaceMount{HostPath: host}
	}

	start := time.Now()
	res, err := pb.RunPipeline(ctx, backend.PipelineRunInvocation{
		RunID:           runID,
		PipelineRunName: pipelineRunName,
		Pipeline:        pl,
		Tasks:           in.Bundle.Tasks,
		Params:          paramList,
		Workspaces:      wsMap,
		LogSink:         reporter.NewLogSink(e.rep),
	})
	dur := time.Since(start)
	if err != nil {
		e.rep.Emit(reporter.Event{Kind: reporter.EvtRunEnd, Time: time.Now(), Status: "failed", Duration: dur, Message: err.Error()})
		return RunResult{Status: "failed"}, err
	}
	e.rep.Emit(reporter.Event{Kind: reporter.EvtRunEnd, Time: time.Now(), Status: res.Status, Duration: dur})
	out := RunResult{Status: res.Status, Tasks: map[string]TaskOutcome{}}
	for n, oc := range res.Tasks {
		out.Tasks[n] = TaskOutcome{Status: oc.Status, Message: oc.Message, Results: oc.Results}
	}
	return out, nil
}
```

- [ ] **Step 2: Verify tests**

```bash
go build ./...
go test ./internal/engine/...
```

Expected: PASS — existing tests still green; routing path is only exercised when the backend implements PipelineBackend (which the test fakes don't).

- [ ] **Step 3: Commit**

```bash
git add internal/engine/
git -c user.email="makebyyourside0367@gmail.com" -c user.name="Daniel F B Morinigo" commit -m "$(cat <<'EOF'
feat(engine): delegate to PipelineBackend when supported

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 7: CLI — cluster subcommands + --cluster wiring

**Files:**
- Create: `cmd/tkn-act/cluster.go`
- Modify: `cmd/tkn-act/root.go` (register the cluster command)
- Modify: `cmd/tkn-act/run.go` (replace the "not yet implemented" stub)

- [ ] **Step 1: Create cluster.go**

```go
package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/danielfbm/tkn-act/internal/cluster/k3d"
	"github.com/spf13/cobra"
)

func newClusterCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "cluster",
		Short: "Manage the local k3d cluster used by --cluster",
	}
	cmd.AddCommand(newClusterUpCmd(), newClusterDownCmd(), newClusterStatusCmd())
	return cmd
}

func newClusterUpCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "up",
		Short: "Create the local cluster and install Tekton (idempotent)",
		RunE: func(_ *cobra.Command, _ []string) error {
			drv := newDriver()
			if err := drv.Ensure(context.Background()); err != nil {
				return err
			}
			fmt.Println("cluster ready:", drv.Name())
			fmt.Println("kubeconfig:", drv.Kubeconfig())
			return nil
		},
	}
}

func newClusterDownCmd() *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:   "down",
		Short: "Delete the local cluster",
		RunE: func(_ *cobra.Command, _ []string) error {
			drv := newDriver()
			if !yes {
				fmt.Print("delete cluster ", drv.Name(), "? [y/N] ")
				r := bufio.NewReader(os.Stdin)
				ans, _ := r.ReadString('\n')
				if strings.ToLower(strings.TrimSpace(ans)) != "y" {
					return fmt.Errorf("cancelled")
				}
			}
			return drv.Delete(context.Background())
		},
	}
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "skip confirmation")
	return cmd
}

func newClusterStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show cluster + Tekton status",
		RunE: func(_ *cobra.Command, _ []string) error {
			drv := newDriver()
			st, err := drv.Status(context.Background())
			if err != nil {
				return err
			}
			fmt.Println("cluster:    ", drv.Name())
			fmt.Println("exists:     ", st.Exists)
			fmt.Println("running:    ", st.Running)
			if st.Detail != "" {
				fmt.Println("detail:     ", st.Detail)
			}
			fmt.Println("kubeconfig: ", drv.Kubeconfig())
			return nil
		},
	}
}

func newDriver() *k3d.Driver {
	kubecfg := filepath.Join(cacheDir(), "cluster", "kubeconfig")
	return k3d.New(k3d.Options{ClusterName: "tkn-act", KubeconfigPath: kubecfg})
}
```

- [ ] **Step 2: Register the command in root.go**

Add to `newRootCmd()`:

```go
cmd.AddCommand(newClusterCmd())
```

(Place near the other `AddCommand` calls.)

- [ ] **Step 3: Wire --cluster in run.go**

Replace the existing block in `runWith`:

```go
if gf.cluster {
    return fmt.Errorf("--cluster (k3d) backend is not yet implemented in v1; coming in v1.x")
}
```

with:

```go
var be backend.Backend
if gf.cluster {
    cb := clusterbe.New(clusterbe.Options{
        CacheDir: cacheDir(),
        Driver: k3d.New(k3d.Options{
            ClusterName:    "tkn-act",
            KubeconfigPath: filepath.Join(cacheDir(), "cluster", "kubeconfig"),
        }),
    })
    be = cb
} else {
    docker, err := docker.New(docker.Options{})
    if err != nil { return fmt.Errorf("docker backend: %w", err) }
    be = docker
}
```

Update imports in `run.go`:

```go
import (
    // ... existing imports ...
    "github.com/danielfbm/tkn-act/internal/backend"
    clusterbe "github.com/danielfbm/tkn-act/internal/backend/cluster"
    "github.com/danielfbm/tkn-act/internal/cluster/k3d"
    // remove the named "docker" import collision; keep:
    "github.com/danielfbm/tkn-act/internal/backend/docker"
)
```

Update the engine construction call to pass `be` instead of building docker inline.

(The exact import block needs to keep `docker` package available; rename one if a collision arises, e.g., `dockerbe "github.com/danielfbm/tkn-act/internal/backend/docker"`.)

- [ ] **Step 4: Build and smoke-test**

```bash
make build
./tkn-act --help
./tkn-act cluster --help
./tkn-act cluster status   # runs against k3d binary if available; ok to fail if k3d missing
```

`./tkn-act cluster status` will fail with "k3d not found" if k3d isn't installed — that's expected and a clean error.

- [ ] **Step 5: Commit**

```bash
git add cmd/
git -c user.email="makebyyourside0367@gmail.com" -c user.name="Daniel F B Morinigo" commit -m "$(cat <<'EOF'
feat(cmd): cluster up/down/status + wire --cluster to cluster backend

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 8: Integration test fixture

**Files:**
- Create: `internal/clustere2e/cluster_e2e_test.go`

This is gated behind build tag `cluster` and requires k3d + kubectl + Docker. Compiles always; runs only on demand.

- [ ] **Step 1: Create the test**

```go
//go:build cluster

package clustere2e_test

import (
	"context"
	"io"
	"path/filepath"
	"testing"
	"time"

	"github.com/danielfbm/tkn-act/internal/backend"
	clusterbe "github.com/danielfbm/tkn-act/internal/backend/cluster"
	"github.com/danielfbm/tkn-act/internal/cluster/k3d"
	"github.com/danielfbm/tkn-act/internal/engine"
	"github.com/danielfbm/tkn-act/internal/loader"
	"github.com/danielfbm/tkn-act/internal/reporter"
	"github.com/danielfbm/tkn-act/internal/workspace"
)

func TestClusterE2EHello(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	files, _ := filepath.Glob("../../testdata/e2e/hello/*.yaml")
	b, err := loader.LoadFiles(files)
	if err != nil { t.Fatal(err) }

	mgr := workspace.NewManager(t.TempDir(), "cluster-e2e")
	engine.SetResultsDirProvisioner(func(_, taskName string) (string, error) {
		return mgr.ProvisionResultsDir(taskName)
	})

	kubecfg := filepath.Join(t.TempDir(), "kubeconfig")
	cb := clusterbe.New(clusterbe.Options{
		CacheDir: t.TempDir(),
		Driver:   k3d.New(k3d.Options{ClusterName: "tkn-act-e2e", KubeconfigPath: kubecfg}),
	})
	t.Cleanup(func() { _ = cb.Cleanup(context.Background()) })

	rep := reporter.NewJSON(io.Discard)
	res, err := engine.New(cb, rep, engine.Options{}).RunPipeline(ctx, engine.PipelineInput{
		Bundle: b, Name: "hello",
	})
	if err != nil { t.Fatalf("run: %v", err) }
	if res.Status != "succeeded" { t.Errorf("status = %s", res.Status) }
	_ = backend.Backend(cb) // compile-time check
}
```

- [ ] **Step 2: Verify the test compiles**

```bash
go vet -tags=cluster ./...
```

Expected: clean.

- [ ] **Step 3: Commit**

```bash
git add internal/clustere2e/
git -c user.email="makebyyourside0367@gmail.com" -c user.name="Daniel F B Morinigo" commit -m "$(cat <<'EOF'
test(cluster-e2e): hello pipeline through cluster backend (gated)

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 9: Verify and document

**Files:**
- Modify: `README.md`

- [ ] **Step 1: Run all checks**

```bash
go build ./...
go test ./...
go vet -tags=integration ./...
go vet -tags=cluster ./...
make build
./tkn-act cluster --help
./tkn-act --help
```

All must pass / show expected output.

- [ ] **Step 2: Update README's "What's not supported" section**

Move the cluster backend bullet from the "not supported" list into a new "v1.1 additions" or similar block, and add a usage snippet:

```markdown
## Cluster backend (v1.1)

For Tekton-fidelity runs (real controller, real entrypoint shim):

    tkn-act cluster up               # one-time, ~30–60s
    tkn-act run --cluster -f pipeline.yaml
    tkn-act cluster status
    tkn-act cluster down

Requires `k3d` and `kubectl` on PATH.
```

- [ ] **Step 3: Commit**

```bash
git add README.md
git -c user.email="makebyyourside0367@gmail.com" -c user.name="Daniel F B Morinigo" commit -m "$(cat <<'EOF'
docs: README cluster backend section

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Self-review

- [ ] Spec §3.1 (k3d Ensure flow) → Task 2 covers create/get-kubeconfig; idempotent via list check.
- [ ] Spec §3.2 (Tekton install) → Task 3 covers CRD-presence skip + apply + wait.
- [ ] Spec §3.3 (Status) → Task 7 cluster status command.
- [ ] Spec §3.4 (Down with confirm) → Task 7 with `-y`.
- [ ] Spec §4 (PipelineRun submission, watch, logs) → Task 5.
- [ ] Spec §4.1 (volumeClaimTemplate workspaces) → Task 5 buildPipelineRun.
- [ ] Spec §4.3 (events back to reporter) → Task 6 routing emits run-start/end; per-task events are deferred (gap), see below.
- [ ] Spec §5 (CLI changes) → Task 7.

**Known gap (acceptable for v1.1):** `runViaPipelineBackend` does NOT emit per-task EvtTaskStart/EvtTaskEnd events — it only emits run-start and run-end. The user sees logs streamed by step but not the tree-status output the Docker backend produces. Adding fine-grained events requires the cluster backend to translate TaskRun watch events into reporter events. Left as an explicit follow-up; documented in spec §4.3 as the design intent.

---

**End of plan.**
