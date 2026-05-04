package validator_test

import (
	"reflect"
	"strings"
	"testing"

	"github.com/danielfbm/tkn-act/internal/loader"
	"github.com/danielfbm/tkn-act/internal/tektontypes"
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

// TestValidateCMReferenceWithoutSourceIsNotAnError locks in that the
// validator does NOT statically check for "configMap volume references
// a name that no source declares". The runtime volume materializer
// reports the actual error post-merge across all three sources (inline
// flag, --configmap-dir, and -f-loaded YAML), where the message is
// useful and accurate; a static validator check would either duplicate
// it or get the precedence wrong. This test guards against an
// accidental future change to the validator that would tighten this.
func TestValidateCMReferenceWithoutSourceIsNotAnError(t *testing.T) {
	b := mustLoad(t, `
apiVersion: tekton.dev/v1
kind: Task
metadata: {name: t}
spec:
  volumes:
    - name: v
      configMap: {name: missing-cfg}
  steps:
    - name: s
      image: alpine:3
      volumeMounts: [{name: v, mountPath: /etc/x}]
      script: 'true'
---
apiVersion: tekton.dev/v1
kind: Pipeline
metadata: {name: p}
spec:
  tasks:
    - {name: a, taskRef: {name: t}}
`)
	if errs := validator.Validate(b, "p", nil); len(errs) != 0 {
		t.Errorf("unexpected errors: %v (validator should not own this check)", errs)
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

// TestValidateAcceptsResolverBackedTaskRef: a Pipeline whose taskRef
// uses a resolver name that's in the allow-list (the default direct
// set) validates cleanly when --offline is unset.
func TestValidateAcceptsResolverBackedTaskRef(t *testing.T) {
	b, err := loader.LoadBytes([]byte(`
apiVersion: tekton.dev/v1
kind: Pipeline
metadata: {name: p}
spec:
  tasks:
    - name: build
      taskRef:
        resolver: git
        params:
          - {name: url, value: https://github.com/x/y}
`))
	if err != nil {
		t.Fatal(err)
	}
	opts := validator.Options{
		RegisteredResolvers: []string{"git", "hub", "http", "bundles"},
	}
	errs := validator.ValidateWithOptions(b, "p", nil, opts)
	for _, e := range errs {
		// Resolver-backed tasks pass validation; the task body itself
		// isn't known yet, so the per-step / per-volume checks don't
		// run for it. Ensure the validator didn't reject it.
		if strings.Contains(e.Error(), "build") && strings.Contains(e.Error(), "unknown Task") {
			t.Errorf("rejected resolver-backed task as unknown Task: %v", e)
		}
		if strings.Contains(e.Error(), "git") && strings.Contains(e.Error(), "unknown") {
			t.Errorf("rejected git resolver: %v", e)
		}
	}
}

// TestValidateRejectsUnknownResolverInDirectMode: a resolver name not
// in the allow-list is rejected when RemoteResolverEnabled is false.
func TestValidateRejectsUnknownResolverInDirectMode(t *testing.T) {
	b, err := loader.LoadBytes([]byte(`
apiVersion: tekton.dev/v1
kind: Pipeline
metadata: {name: p}
spec:
  tasks:
    - name: build
      taskRef:
        resolver: made-up
        params:
          - {name: x, value: y}
`))
	if err != nil {
		t.Fatal(err)
	}
	opts := validator.Options{
		RegisteredResolvers: []string{"git", "hub", "http", "bundles"},
	}
	errs := validator.ValidateWithOptions(b, "p", nil, opts)
	if len(errs) == 0 {
		t.Fatal("expected error for unknown resolver in direct mode")
	}
	found := false
	for _, e := range errs {
		if strings.Contains(e.Error(), "made-up") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected made-up in error, got %v", errs)
	}
}

// TestValidateAcceptsAnyResolverNameInRemoteMode: when
// RemoteResolverEnabled is true, an arbitrary resolver name is allowed
// — the remote cluster's controller knows what to do with it.
func TestValidateAcceptsAnyResolverNameInRemoteMode(t *testing.T) {
	b, err := loader.LoadBytes([]byte(`
apiVersion: tekton.dev/v1
kind: Pipeline
metadata: {name: p}
spec:
  tasks:
    - name: build
      taskRef:
        resolver: my-private-resolver
        params:
          - {name: x, value: y}
`))
	if err != nil {
		t.Fatal(err)
	}
	opts := validator.Options{
		RegisteredResolvers:   []string{"git", "hub", "http", "bundles"},
		RemoteResolverEnabled: true,
	}
	errs := validator.ValidateWithOptions(b, "p", nil, opts)
	for _, e := range errs {
		if strings.Contains(e.Error(), "my-private-resolver") {
			t.Errorf("rejected custom name in remote mode: %v", e)
		}
	}
}

// TestValidateRejectsResolverBackedTaskRefOffline: with Offline=true
// and an empty cache, every resolver-backed ref must fail validation
// before any task runs.
func TestValidateRejectsResolverBackedTaskRefOffline(t *testing.T) {
	b, err := loader.LoadBytes([]byte(`
apiVersion: tekton.dev/v1
kind: Pipeline
metadata: {name: p}
spec:
  tasks:
    - name: build
      taskRef:
        resolver: git
        params:
          - {name: url, value: u}
`))
	if err != nil {
		t.Fatal(err)
	}
	opts := validator.Options{
		RegisteredResolvers: []string{"git", "hub", "http", "bundles"},
		Offline:             true,
		// No CacheCheck — defaults to "always miss" which is what
		// --offline expects when no cache is wired.
	}
	errs := validator.ValidateWithOptions(b, "p", nil, opts)
	if len(errs) == 0 {
		t.Fatal("expected error for offline + cache miss")
	}
	found := false
	for _, e := range errs {
		if strings.Contains(e.Error(), "offline") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected --offline mention, got %v", errs)
	}
}

// TestValidateOfflineWithCacheHit: same setup, but a CacheCheck
// callback that returns true short-circuits the offline rejection.
func TestValidateOfflineWithCacheHit(t *testing.T) {
	b, err := loader.LoadBytes([]byte(`
apiVersion: tekton.dev/v1
kind: Pipeline
metadata: {name: p}
spec:
  tasks:
    - name: build
      taskRef:
        resolver: git
        params:
          - {name: url, value: u}
`))
	if err != nil {
		t.Fatal(err)
	}
	opts := validator.Options{
		RegisteredResolvers: []string{"git", "hub", "http", "bundles"},
		Offline:             true,
		CacheCheck: func(_ validator.UnresolvedRef) bool {
			return true
		},
	}
	errs := validator.ValidateWithOptions(b, "p", nil, opts)
	for _, e := range errs {
		if strings.Contains(e.Error(), "offline") {
			t.Errorf("offline error fired despite cache hit: %v", e)
		}
	}
}

// TestValidateRejectsResolverParamWithUnknownTaskResultRef: a
// resolver.params containing $(tasks.does-not-exist.results.foo) must
// fail validation with the missing task name.
func TestValidateRejectsResolverParamWithUnknownTaskResultRef(t *testing.T) {
	b, err := loader.LoadBytes([]byte(`
apiVersion: tekton.dev/v1
kind: Pipeline
metadata: {name: p}
spec:
  tasks:
    - name: build
      taskRef:
        resolver: git
        params:
          - {name: pathInRepo, value: "$(tasks.does-not-exist.results.foo)"}
`))
	if err != nil {
		t.Fatal(err)
	}
	opts := validator.Options{
		RegisteredResolvers: []string{"git", "hub", "http", "bundles"},
	}
	errs := validator.ValidateWithOptions(b, "p", nil, opts)
	if len(errs) == 0 {
		t.Fatal("expected error for unknown task in resolver.params")
	}
	found := false
	for _, e := range errs {
		if strings.Contains(e.Error(), "does-not-exist") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected does-not-exist in error, got %v", errs)
	}
}

// TestValidateTaskSpec covers the per-Task helper used by the engine's
// lazy-dispatch path to validate resolver-returned bytes.
func TestValidateTaskSpec(t *testing.T) {
	tests := []struct {
		name    string
		spec    string
		wantErr string // substring; empty means no error
	}{
		{
			name: "valid",
			spec: `
steps:
  - {name: s, image: alpine, script: 'true'}
`,
		},
		{
			name: "no steps",
			spec: `{}
`,
			wantErr: "must have at least one step",
		},
		{
			name: "bad timeout",
			spec: `
steps: [{name: s, image: alpine, script: 'true'}]
timeout: not-a-duration
`,
			wantErr: "invalid timeout",
		},
		{
			name: "bad onError",
			spec: `
steps:
  - {name: s, image: alpine, script: 'true', onError: surrender}
`,
			wantErr: "unsupported onError",
		},
		{
			name: "volume without source",
			spec: `
steps: [{name: s, image: alpine, script: 'true'}]
volumes:
  - {name: v}
`,
			wantErr: "unsupported volume kind",
		},
		{
			name: "volume with two sources",
			spec: `
steps: [{name: s, image: alpine, script: 'true'}]
volumes:
  - {name: v, emptyDir: {}, hostPath: {path: /tmp}}
`,
			wantErr: "multiple sources set",
		},
		{
			name: "hostPath missing path",
			spec: `
steps: [{name: s, image: alpine, script: 'true'}]
volumes:
  - {name: v, hostPath: {}}
`,
			wantErr: "hostPath.path is required",
		},
		{
			name: "volumeMount references undeclared volume",
			spec: `
steps:
  - {name: s, image: alpine, script: 'true', volumeMounts: [{name: ghost, mountPath: /a}]}
`,
			wantErr: "references undeclared volume",
		},
		{
			name: "volumeMount with empty mountPath",
			spec: `
steps:
  - {name: s, image: alpine, script: 'true', volumeMounts: [{name: v, mountPath: ""}]}
volumes:
  - {name: v, emptyDir: {}}
`,
			wantErr: "empty mountPath",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b, err := loader.LoadBytes([]byte(`apiVersion: tekton.dev/v1
kind: Task
metadata: {name: x}
spec:
` + indent(tt.spec, "  ")))
			if err != nil {
				t.Fatalf("load: %v", err)
			}
			task := b.Tasks["x"]
			errs := validator.ValidateTaskSpec("x", task.Spec)
			if tt.wantErr == "" {
				if len(errs) != 0 {
					t.Errorf("unexpected errors: %v", errs)
				}
				return
			}
			if len(errs) == 0 {
				t.Fatal("expected error, got none")
			}
			found := false
			for _, e := range errs {
				if strings.Contains(e.Error(), tt.wantErr) {
					found = true
				}
			}
			if !found {
				t.Errorf("expected substring %q, got %v", tt.wantErr, errs)
			}
		})
	}
}

func indent(s, prefix string) string {
	out := ""
	for _, line := range strings.Split(s, "\n") {
		if line == "" {
			out += "\n"
			continue
		}
		out += prefix + line + "\n"
	}
	return out
}
func TestValidateSidecarRequiresImage(t *testing.T) {
	b := mustLoad(t, `
apiVersion: tekton.dev/v1
kind: Task
metadata: {name: t}
spec:
  sidecars:
    - {name: redis, image: ""}
  steps:
    - {name: s, image: alpine:3, script: 'true'}
---
apiVersion: tekton.dev/v1
kind: Pipeline
metadata: {name: p}
spec:
  tasks: [{name: a, taskRef: {name: t}}]
`)
	errs := validator.Validate(b, "p", nil)
	if len(errs) == 0 {
		t.Fatalf("expected error for empty sidecar image")
	}
}

func TestValidateSidecarNameUnique(t *testing.T) {
	b := mustLoad(t, `
apiVersion: tekton.dev/v1
kind: Task
metadata: {name: t}
spec:
  sidecars:
    - {name: redis, image: redis:7-alpine}
    - {name: redis, image: redis:7-alpine}
  steps:
    - {name: s, image: alpine:3, script: 'true'}
---
apiVersion: tekton.dev/v1
kind: Pipeline
metadata: {name: p}
spec:
  tasks: [{name: a, taskRef: {name: t}}]
`)
	errs := validator.Validate(b, "p", nil)
	if len(errs) == 0 {
		t.Fatalf("expected error for duplicate sidecar name")
	}
}

func TestValidateSidecarNameCollidesWithStep(t *testing.T) {
	b := mustLoad(t, `
apiVersion: tekton.dev/v1
kind: Task
metadata: {name: t}
spec:
  sidecars:
    - {name: shared, image: redis:7-alpine}
  steps:
    - {name: shared, image: alpine:3, script: 'true'}
---
apiVersion: tekton.dev/v1
kind: Pipeline
metadata: {name: p}
spec:
  tasks: [{name: a, taskRef: {name: t}}]
`)
	errs := validator.Validate(b, "p", nil)
	if len(errs) == 0 {
		t.Fatalf("expected error for sidecar name colliding with step name")
	}
}

func TestValidateSidecarVolumeMountResolves(t *testing.T) {
	b := mustLoad(t, `
apiVersion: tekton.dev/v1
kind: Task
metadata: {name: t}
spec:
  sidecars:
    - name: redis
      image: redis:7-alpine
      volumeMounts:
        - {name: undeclared, mountPath: /data}
  steps:
    - {name: s, image: alpine:3, script: 'true'}
---
apiVersion: tekton.dev/v1
kind: Pipeline
metadata: {name: p}
spec:
  tasks: [{name: a, taskRef: {name: t}}]
`)
	errs := validator.Validate(b, "p", nil)
	if len(errs) == 0 {
		t.Fatalf("expected error for sidecar volumeMount referencing undeclared volume")
	}
}

func TestValidateSidecarsValid(t *testing.T) {
	b := mustLoad(t, `
apiVersion: tekton.dev/v1
kind: Task
metadata: {name: t}
spec:
  volumes:
    - {name: shared, emptyDir: {}}
  sidecars:
    - name: redis
      image: redis:7-alpine
      volumeMounts:
        - {name: shared, mountPath: /data}
  steps:
    - {name: s, image: alpine:3, script: 'true'}
---
apiVersion: tekton.dev/v1
kind: Pipeline
metadata: {name: p}
spec:
  tasks: [{name: a, taskRef: {name: t}}]
`)
	if errs := validator.Validate(b, "p", nil); len(errs) != 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
}

// ---- StepAction validator rules (rules paired with Track 1 #8) ----

func TestValidateStepActionRefValid(t *testing.T) {
	b := mustLoad(t, `
apiVersion: tekton.dev/v1beta1
kind: StepAction
metadata: {name: greet}
spec:
  params: [{name: who, default: world}]
  image: alpine:3
  script: 'echo $(params.who)'
---
apiVersion: tekton.dev/v1
kind: Task
metadata: {name: t}
spec:
  steps:
    - {name: g, ref: {name: greet}}
---
apiVersion: tekton.dev/v1
kind: Pipeline
metadata: {name: p}
spec:
  tasks: [{name: a, taskRef: {name: t}}]
`)
	if errs := validator.Validate(b, "p", nil); len(errs) != 0 {
		t.Errorf("unexpected errors: %v", errs)
	}
}

func TestValidateStepActionUnknownRef(t *testing.T) {
	b := mustLoad(t, `
apiVersion: tekton.dev/v1
kind: Task
metadata: {name: t}
spec:
  steps: [{name: g, ref: {name: nope}}]
---
apiVersion: tekton.dev/v1
kind: Pipeline
metadata: {name: p}
spec:
  tasks: [{name: a, taskRef: {name: t}}]
`)
	errs := validator.Validate(b, "p", nil)
	if len(errs) == 0 {
		t.Fatal("want unknown-StepAction error, got none")
	}
	found := false
	for _, e := range errs {
		if strings.Contains(e.Error(), "unknown StepAction") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("want unknown-StepAction error, got %v", errs)
	}
}

func TestValidateStepActionRefAndInlineRejected(t *testing.T) {
	b := mustLoad(t, `
apiVersion: tekton.dev/v1beta1
kind: StepAction
metadata: {name: greet}
spec: {image: alpine:3, script: 'echo hi'}
---
apiVersion: tekton.dev/v1
kind: Task
metadata: {name: t}
spec:
  steps:
    - {name: g, ref: {name: greet}, image: busybox}
---
apiVersion: tekton.dev/v1
kind: Pipeline
metadata: {name: p}
spec:
  tasks: [{name: a, taskRef: {name: t}}]
`)
	errs := validator.Validate(b, "p", nil)
	if len(errs) == 0 {
		t.Fatal("want ref+inline-rejected error, got none")
	}
	found := false
	for _, e := range errs {
		if strings.Contains(e.Error(), "ref and inline body") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("want ref+inline-rejected error, got %v", errs)
	}
}

func TestValidateStepActionMissingRequiredParam(t *testing.T) {
	b := mustLoad(t, `
apiVersion: tekton.dev/v1beta1
kind: StepAction
metadata: {name: greet}
spec:
  params: [{name: who}]
  image: alpine:3
  script: 'echo $(params.who)'
---
apiVersion: tekton.dev/v1
kind: Task
metadata: {name: t}
spec:
  steps: [{name: g, ref: {name: greet}}]
---
apiVersion: tekton.dev/v1
kind: Pipeline
metadata: {name: p}
spec:
  tasks: [{name: a, taskRef: {name: t}}]
`)
	errs := validator.Validate(b, "p", nil)
	if len(errs) == 0 {
		t.Fatal("want missing-param error, got none")
	}
	found := false
	for _, e := range errs {
		if strings.Contains(e.Error(), `missing required StepAction param "who"`) {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("want missing-param error, got %v", errs)
	}
}

// Rule 15: a StepAction body itself must not contain `ref:`. Spec
// settles this with a structural guard (StepActionSpec has no Ref
// field); no runtime post-load scan. This reflection-based test is
// the canonical enforcement — if anyone adds Ref to StepActionSpec,
// this test goes red and the build breaks.
func TestValidateStepActionNoNestedRef(t *testing.T) {
	var sa tektontypes.StepActionSpec
	v := reflect.ValueOf(sa)
	for i := 0; i < v.NumField(); i++ {
		if name := v.Type().Field(i).Name; name == "Ref" {
			t.Fatal("StepActionSpec must not model Ref (would allow nested refs)")
		}
	}
}

// Rule 16: an inline Step (no ref:) must have a non-empty image after
// stepTemplate inheritance. Paired with the Step.Image JSON tag
// relaxation from required → omitempty.
func TestValidateInlineStepRequiresImage(t *testing.T) {
	b := mustLoad(t, `
apiVersion: tekton.dev/v1
kind: Task
metadata: {name: t}
spec:
  steps:
    - {name: bad, script: 'echo'}
---
apiVersion: tekton.dev/v1
kind: Pipeline
metadata: {name: p}
spec:
  tasks: [{name: a, taskRef: {name: t}}]
`)
	errs := validator.Validate(b, "p", nil)
	if len(errs) == 0 {
		t.Fatal("want no-image error, got none")
	}
	found := false
	for _, e := range errs {
		if strings.Contains(e.Error(), "inline step has no image") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("want no-image error, got %v", errs)
	}
}

// Rule 16 positive: image inherited from stepTemplate should pass.
func TestValidateInlineStepImageInheritedFromStepTemplate(t *testing.T) {
	b := mustLoad(t, `
apiVersion: tekton.dev/v1
kind: Task
metadata: {name: t}
spec:
  stepTemplate: {image: alpine:3}
  steps:
    - {name: ok, script: 'echo'}
---
apiVersion: tekton.dev/v1
kind: Pipeline
metadata: {name: p}
spec:
  tasks: [{name: a, taskRef: {name: t}}]
`)
	if errs := validator.Validate(b, "p", nil); len(errs) != 0 {
		t.Errorf("unexpected errors: %v", errs)
	}
}

// Rule 17: resolver-form ref: { resolver: hub, ... } must be
// rejected with a clear "not supported in this release" message,
// not the confusing "references unknown StepAction \"\"" from rule 12.
func TestValidateResolverFormRefRejected(t *testing.T) {
	b := mustLoad(t, `
apiVersion: tekton.dev/v1
kind: Task
metadata: {name: t}
spec:
  steps:
    - name: g
      ref: {resolver: hub, params: [{name: name, value: git-clone}]}
---
apiVersion: tekton.dev/v1
kind: Pipeline
metadata: {name: p}
spec:
  tasks: [{name: a, taskRef: {name: t}}]
`)
	errs := validator.Validate(b, "p", nil)
	if len(errs) == 0 {
		t.Fatal("want resolver-form rejection, got none")
	}
	found := false
	for _, e := range errs {
		if strings.Contains(e.Error(), "resolver-based StepAction refs") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("want resolver-form rejection, got %v", errs)
	}
}

// Rule 18: a StepAction param with array/object default is rejected.
func TestValidateStepActionArrayDefaultRejected(t *testing.T) {
	b := mustLoad(t, `
apiVersion: tekton.dev/v1beta1
kind: StepAction
metadata: {name: greet}
spec:
  params:
    - name: who
      type: array
      default: [a, b]
  image: alpine:3
  script: 'echo'
---
apiVersion: tekton.dev/v1
kind: Task
metadata: {name: t}
spec:
  steps: [{name: g, ref: {name: greet}, params: [{name: who, value: x}]}]
---
apiVersion: tekton.dev/v1
kind: Pipeline
metadata: {name: p}
spec:
  tasks: [{name: a, taskRef: {name: t}}]
`)
	errs := validator.Validate(b, "p", nil)
	if len(errs) == 0 {
		t.Fatal("want default-type error, got none")
	}
	found := false
	for _, e := range errs {
		if strings.Contains(e.Error(), "default type") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("want default-type error, got %v", errs)
	}
}

// ---- Matrix validator rules (Track 1 #3) ----

func TestValidateMatrixEmptyValueList(t *testing.T) {
	b, err := loader.LoadBytes([]byte(`
apiVersion: tekton.dev/v1
kind: Task
metadata: {name: t}
spec:
  params: [{name: os}]
  steps: [{name: s, image: alpine, script: 'true'}]
---
apiVersion: tekton.dev/v1
kind: Pipeline
metadata: {name: p}
spec:
  tasks:
    - name: build
      taskRef: {name: t}
      matrix:
        params:
          - {name: os, value: []}
`))
	if err != nil {
		t.Fatal(err)
	}
	errs := validator.Validate(b, "p", nil)
	if len(errs) == 0 {
		t.Fatal("want error for empty matrix value list, got none")
	}
	found := false
	for _, e := range errs {
		if strings.Contains(e.Error(), "non-empty string list") {
			found = true
		}
	}
	if !found {
		t.Errorf("errors = %v, want one mentioning 'non-empty string list'", errs)
	}
}

func TestValidateMatrixDuplicateParamName(t *testing.T) {
	b, err := loader.LoadBytes([]byte(`
apiVersion: tekton.dev/v1
kind: Task
metadata: {name: t}
spec:
  params: [{name: os}]
  steps: [{name: s, image: alpine, script: 'true'}]
---
apiVersion: tekton.dev/v1
kind: Pipeline
metadata: {name: p}
spec:
  tasks:
    - name: build
      taskRef: {name: t}
      matrix:
        params:
          - {name: os, value: [linux]}
          - {name: os, value: [darwin]}
`))
	if err != nil {
		t.Fatal(err)
	}
	errs := validator.Validate(b, "p", nil)
	found := false
	for _, e := range errs {
		if strings.Contains(e.Error(), "twice") {
			found = true
		}
	}
	if !found {
		t.Errorf("errors = %v, want one mentioning 'twice'", errs)
	}
}

func TestValidateMatrixCardinalityCap(t *testing.T) {
	// Build 17 × 17 matrix programmatically — 289 > 256.
	bigList := strings.Repeat("v, ", 16) + "v"
	yaml := `
apiVersion: tekton.dev/v1
kind: Task
metadata: {name: t}
spec:
  params: [{name: a}, {name: b}]
  steps: [{name: s, image: alpine, script: 'true'}]
---
apiVersion: tekton.dev/v1
kind: Pipeline
metadata: {name: p}
spec:
  tasks:
    - name: build
      taskRef: {name: t}
      matrix:
        params:
          - {name: a, value: [` + bigList + `]}
          - {name: b, value: [` + bigList + `]}
`
	b, err := loader.LoadBytes([]byte(yaml))
	if err != nil {
		t.Fatal(err)
	}
	errs := validator.Validate(b, "p", nil)
	found := false
	for _, e := range errs {
		if strings.Contains(e.Error(), "exceeding the cap") {
			found = true
		}
	}
	if !found {
		t.Errorf("errors = %v, want one mentioning 'exceeding the cap'", errs)
	}
}

func TestValidateMatrixIncludeNonStringRejected(t *testing.T) {
	b, err := loader.LoadBytes([]byte(`
apiVersion: tekton.dev/v1
kind: Task
metadata: {name: t}
spec:
  params: [{name: os}, {name: archs}]
  steps: [{name: s, image: alpine, script: 'true'}]
---
apiVersion: tekton.dev/v1
kind: Pipeline
metadata: {name: p}
spec:
  tasks:
    - name: build
      taskRef: {name: t}
      matrix:
        params:
          - {name: os, value: [linux]}
        include:
          - name: arm-extra
            params:
              - {name: archs, value: [arm64, armv7]}
`))
	if err != nil {
		t.Fatal(err)
	}
	errs := validator.Validate(b, "p", nil)
	found := false
	for _, e := range errs {
		if strings.Contains(e.Error(), "must be a string") {
			found = true
		}
	}
	if !found {
		t.Errorf("errors = %v, want one mentioning 'must be a string'", errs)
	}
}

func TestValidateMatrixIncludeOverlapsCrossProductRejected(t *testing.T) {
	b, err := loader.LoadBytes([]byte(`
apiVersion: tekton.dev/v1
kind: Task
metadata: {name: t}
spec:
  params: [{name: os}, {name: goversion}, {name: arch}]
  steps: [{name: s, image: alpine, script: 'true'}]
---
apiVersion: tekton.dev/v1
kind: Pipeline
metadata: {name: p}
spec:
  tasks:
    - name: build
      taskRef: {name: t}
      matrix:
        params:
          - {name: os, value: [linux, darwin]}
          - {name: goversion, value: ["1.21"]}
        include:
          - name: arm-extra
            params:
              - {name: os, value: linux}
              - {name: arch, value: arm64}
`))
	if err != nil {
		t.Fatal(err)
	}
	errs := validator.Validate(b, "p", nil)
	if len(errs) == 0 {
		t.Fatal("want overlap-rejection error, got none")
	}
	found := false
	for _, e := range errs {
		if strings.Contains(e.Error(), "overlaps a cross-product param") {
			found = true
		}
	}
	if !found {
		t.Errorf("errors = %v, want one mentioning 'overlaps a cross-product param'", errs)
	}
}

func TestValidateMatrixFannedTaskArrayResultRejected(t *testing.T) {
	b, err := loader.LoadBytes([]byte(`
apiVersion: tekton.dev/v1
kind: Task
metadata: {name: t}
spec:
  params: [{name: os}]
  results:
    - {name: tags, type: array}
  steps: [{name: s, image: alpine, script: 'true'}]
---
apiVersion: tekton.dev/v1
kind: Pipeline
metadata: {name: p}
spec:
  tasks:
    - name: build
      taskRef: {name: t}
      matrix:
        params:
          - {name: os, value: [linux, darwin]}
`))
	if err != nil {
		t.Fatal(err)
	}
	errs := validator.Validate(b, "p", nil)
	found := false
	for _, e := range errs {
		if strings.Contains(e.Error(), "may only emit string results") {
			found = true
		}
	}
	if !found {
		t.Errorf("errors = %v, want one mentioning 'may only emit string results'", errs)
	}
}

func TestValidateMatrixHappyPath(t *testing.T) {
	b, err := loader.LoadBytes([]byte(`
apiVersion: tekton.dev/v1
kind: Task
metadata: {name: t}
spec:
  params: [{name: os}, {name: goversion}]
  results:
    - {name: tag}
  steps: [{name: s, image: alpine, script: 'true'}]
---
apiVersion: tekton.dev/v1
kind: Pipeline
metadata: {name: p}
spec:
  tasks:
    - name: build
      taskRef: {name: t}
      matrix:
        params:
          - {name: os, value: [linux, darwin]}
          - {name: goversion, value: ["1.21", "1.22"]}
`))
	if err != nil {
		t.Fatal(err)
	}
	errs := validator.Validate(b, "p", nil)
	if len(errs) != 0 {
		t.Errorf("happy path got errors: %v", errs)
	}
}
