# Load `kind: ConfigMap` / `kind: Secret` from `-f` Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let users pass Kubernetes `ConfigMap` and `Secret` resources in the same `-f` multi-doc YAML stream as their Tasks/Pipelines, so the on-disk Tekton manifest a user would normally `kubectl apply` works as-is for `tkn-act run`. Both backends benefit because the existing `volumes.Store` already feeds both.

**Architecture:** Pure parsing extension at the loader layer plus a single ingestion call from the CLI. The loader's `Bundle` grows two maps (`ConfigMaps` / `Secrets`) populated by extending `loadOne` to recognize `kind: ConfigMap` and `kind: Secret` (apiVersion `v1`). The CLI's existing `buildVolumeStores` gains a third source layer: it now feeds bundle-loaded resources into the Store *after* the on-disk dir is wired but *before* the inline `--configmap` / `--secret` flag entries, so the precedence becomes inline > dir > `-f`. The cluster backend sees no code change because its `applyVolumeSources` already walks the same `volumes.Store`.

**Tech Stack:** Go 1.25, no new dependencies. Reuses `sigs.k8s.io/yaml` (already imported by the loader), `internal/loader`, `internal/volumes`, the cross-backend `internal/e2e/fixtures` table, and the existing parity-check + tests-required CI gates.

---

## Track 1 #5 context

This closes Track 1 #5 of `docs/short-term-goals.md`. The Status column says: *"Deferred from v1.2. Loader and `volumes.Store` already in place; needs glue."*

The `feature-parity.md` row lives under **Core types & parsing** and reads:

```
| Loading `kind: ConfigMap` / `kind: Secret` from `-f` | `-f` | gap | both | none | none | docs/short-term-goals.md (Track 1 #5) |
```

The `docs/test-coverage.md` "What is NOT covered" / "By design" section currently has the line:

```
- **Loading `kind: ConfigMap` / `kind: Secret` manifests** via
  `tkn-act -f`. Use `--configmap` / `--configmap-dir` (and the
  `--secret` equivalents). Manifest support is deferred to v1.3.
```

This plan removes that bullet and adds two fixture rows in its place.

## Behavior we're shipping

Users currently pass CM/Secret data three ways:

- `--configmap name=k=v,...` (inline; CLI flag, repeatable)
- `--configmap-dir <path>` (on-disk layout `<path>/<name>/<key>`)
- (this plan) `-f path/to/cm.yaml` — the same multi-doc stream that holds
  Tasks/Pipelines may also include Kubernetes `ConfigMap` and `Secret`
  resources

The loader accepts the standard Kubernetes shape (apiVersion `v1`):

```yaml
apiVersion: v1
kind: ConfigMap
metadata: {name: app-config}
data:
  greeting: hello-from-yaml
---
apiVersion: v1
kind: Secret
metadata: {name: app-secret}
type: Opaque
data:
  token: aGVsbG8=        # base64-encoded per kube convention
stringData:
  raw-token: hello-plain  # also supported (kube projection rule)
```

### Precedence (across the three sources)

For a given `(name, key)` pair the value is resolved in this order, first
hit wins:

1. **Inline `--configmap` / `--secret` CLI flag** (highest).
2. **On-disk dir `--configmap-dir` / `--secret-dir`**.
3. **`-f`-loaded YAML manifest** (lowest).

Rationale: CLI overrides should beat anything authored in a checked-in
manifest, the same way the existing inline-vs-dir precedence works in
`volumes.Store`. The `-f`-loaded manifest is the most "static"/declarative
of the three, so it sits at the bottom.

### Secret-specific rules

- `data` values are base64-decoded per the standard Kubernetes Secret
  contract.
- `stringData` values are taken verbatim (UTF-8 plaintext).
- When the same key appears in both `data` and `stringData`, `stringData`
  wins per Kubernetes semantics.
- `type` is parsed-and-ignored: tkn-act always projects bytes as opaque.

### ConfigMap-specific rules

- `data` values are taken verbatim (UTF-8 strings).
- `binaryData` is rejected at load time with a clear error (out of scope;
  see below).

### Duplicate-name handling

A `kind: ConfigMap` (or `Secret`) with the same `metadata.name` appearing
twice across the loaded files is an error, mirroring the existing
duplicate-Task / duplicate-Pipeline behavior in the loader (`fmt.Errorf("duplicate ConfigMap %q across files", name)`).

A `kind: ConfigMap` whose name collides with an inline `--configmap NAME=...`
is *not* an error — the inline value just wins per the precedence above.

### Cluster backend

No code change in `internal/backend/cluster/`. The cluster backend's
`applyVolumeSources` already walks `volumes.Store` for every CM/Secret
referenced by the run and projects it into ephemeral kube
`ConfigMap`/`Secret` resources in the run namespace. Once the Store has
the bytes (from any of the three sources above), the cluster path runs
unchanged. Verified by reading `internal/backend/cluster/volumes.go:27`
(`applyVolumeSources`) which calls `b.opt.ConfigMaps.Resolve(name)` /
`b.opt.Secrets.Resolve(name)` — pure Store consumers.

## Files to create / modify

| File | Why |
|---|---|
| `internal/loader/loader.go` | Add `Bundle.ConfigMaps` / `Bundle.Secrets` maps; extend `loadOne` to accept `kind: ConfigMap` and `kind: Secret` at `apiVersion: v1`; extend `merge` for the new maps |
| `internal/loader/loader_test.go` | Round-trip tests: CM `data`, Secret `data` (base64), Secret `stringData`, `stringData`-wins-over-`data`, duplicate-name across files, `binaryData` rejected, unknown `apiVersion` for ConfigMap rejected |
| `internal/loader/testdata/cm-and-secret.yaml` (new) | Fixture file used by `TestLoadFiles_ConfigMapAndSecret` |
| `internal/volumes/store.go` | Add `(*Store).LoadFromBundle` (or equivalently a small ingestion helper) so the CLI can pour bundle resources into the Store while preserving the precedence rule documented above |
| `internal/volumes/store_test.go` | Test that bundle-loaded keys are visible via `Resolve` and that on-disk + inline values still win over bundle-loaded ones |
| `cmd/tkn-act/run.go` | In `buildVolumeStores`, after constructing the dir + inline layers, ingest `b.ConfigMaps` / `b.Secrets` into the stores. Refactor signature to accept the loaded `*loader.Bundle` |
| `cmd/tkn-act/run_test.go` (new — small) | Unit test for the precedence resolution at the CLI seam |
| `internal/validator/validator.go` | (optional, see Task 6) warn-not-fail when a Task references a CM/Secret name that isn't in any source after merging |
| `internal/validator/validator_test.go` | (optional, paired with above) |
| `testdata/e2e/configmap-from-yaml/pipeline.yaml` (new) | Cross-backend fixture: pipeline + Task that mounts a CM, with the CM declared inline in the same `-f` |
| `testdata/e2e/secret-from-yaml/pipeline.yaml` (new) | Companion fixture for Secret, exercising both `data` (base64) and `stringData` |
| `internal/e2e/fixtures/fixtures.go` | Two new entries (no inline `ConfigMaps:` map — the data is in the YAML) |
| `cmd/tkn-act/agentguide_data.md` | Note that `-f` accepts CM/Secret manifests; document precedence |
| `AGENTS.md` | Mirror the agentguide change |
| `docs/test-coverage.md` | Remove the deferred-feature bullet; add two fixture rows |
| `docs/short-term-goals.md` | Mark Track 1 #5 done |
| `docs/feature-parity.md` | Flip the `Loading kind: ConfigMap...` row from `gap` to `shipped`; populate `e2e fixture` |
| `README.md` | One-line addition under "Tekton features supported" pointing at the new capability |

## Out of scope (don't do here)

- **`binaryData` on ConfigMaps.** Loader rejects with a clear error; no
  use case yet for tkn-act and base64-binary leaking into a step's env or
  file is a footgun without a story for it.
- **`type` on Secrets beyond `Opaque`.** Field is parsed and silently
  ignored; tkn-act always projects bytes as opaque. (`type: kubernetes.io/tls`
  etc. are upstream concerns.)
- **`immutable: true` on ConfigMap / Secret.** Field is accepted (allows
  the same YAML to apply to a real cluster) and ignored. tkn-act runs are
  ephemeral; immutability has no semantic for us.
- **Loading Secrets from `KUBECONFIG`-targeted clusters or external
  secret stores.** This is `-f`-only.
- **Auto-discovery of CM/Secret YAML files.** Today `discovery.Find`
  picks up `pipeline.yaml` / `.tekton/`; we won't extend that in this
  plan. Users pass CM/Secret YAMLs explicitly via `-f`.
- **A new `ConfigMapKeyRef` / `SecretKeyRef` source on `Step.env`.**
  Separate feature; this plan only fills the `volumes.Store` for the
  existing `Task.spec.volumes` consumer.
- **PodSpec-style `apiVersion: core/v1` instead of `v1`.** We accept
  exactly `apiVersion: v1` (which is what `kubectl` writes by default for
  ConfigMap and Secret).

---

### Task 1: Loader accepts `kind: ConfigMap`

**Files:**
- Create: `internal/loader/testdata/cm-and-secret.yaml`
- Modify: `internal/loader/loader.go`
- Test: `internal/loader/loader_test.go`

- [ ] **Step 1: Write the failing test**

Create `internal/loader/testdata/cm-and-secret.yaml`:

```yaml
apiVersion: v1
kind: ConfigMap
metadata:
  name: app-config
data:
  greeting: hello-from-yaml
  lang: en
---
apiVersion: tekton.dev/v1
kind: Pipeline
metadata: {name: p}
spec:
  tasks:
    - name: a
      taskSpec:
        steps:
          - name: s
            image: alpine:3
            script: 'true'
```

Append to `internal/loader/loader_test.go`:

```go
func TestLoadConfigMap(t *testing.T) {
	bundle, err := loader.LoadFiles([]string{filepath.Join("testdata", "cm-and-secret.yaml")})
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	cm, ok := bundle.ConfigMaps["app-config"]
	if !ok {
		t.Fatalf("missing app-config ConfigMap; got %v", bundle.ConfigMaps)
	}
	if got := cm["greeting"]; string(got) != "hello-from-yaml" {
		t.Errorf("greeting = %q, want hello-from-yaml", string(got))
	}
	if got := cm["lang"]; string(got) != "en" {
		t.Errorf("lang = %q, want en", string(got))
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

```bash
go test -count=1 -run TestLoadConfigMap ./internal/loader/...
```

Expected: FAIL — either `bundle.ConfigMaps undefined`, or (if the loader now errors on `kind: ConfigMap`) `unsupported kind "ConfigMap"`.

- [ ] **Step 3: Add the field to `Bundle`**

In `internal/loader/loader.go`, modify the `Bundle` struct and both initializers (`LoadFiles` and `LoadBytes`). Final form:

```go
// Bundle holds all resources loaded from one or more files.
type Bundle struct {
	Tasks        map[string]tektontypes.Task
	Pipelines    map[string]tektontypes.Pipeline
	PipelineRuns []tektontypes.PipelineRun // ordered as found
	TaskRuns     []tektontypes.TaskRun
	// ConfigMaps and Secrets are bytes-by-key, populated from any
	// `kind: ConfigMap` / `kind: Secret` (apiVersion: v1) doc found in
	// the loaded YAML. They are intended to be poured into the
	// volumes.Store at run time, where the precedence layering with
	// inline (--configmap) and on-disk (--configmap-dir) overrides
	// happens. Map shape: name -> key -> bytes.
	ConfigMaps map[string]map[string][]byte
	Secrets    map[string]map[string][]byte
}
```

In `LoadFiles`:

```go
	out := &Bundle{
		Tasks:      map[string]tektontypes.Task{},
		Pipelines:  map[string]tektontypes.Pipeline{},
		ConfigMaps: map[string]map[string][]byte{},
		Secrets:    map[string]map[string][]byte{},
	}
```

In `LoadBytes`:

```go
	out := &Bundle{
		Tasks:      map[string]tektontypes.Task{},
		Pipelines:  map[string]tektontypes.Pipeline{},
		ConfigMaps: map[string]map[string][]byte{},
		Secrets:    map[string]map[string][]byte{},
	}
```

- [ ] **Step 4: Add a `ConfigMap` case to `loadOne`**

In `internal/loader/loader.go`, the existing `loadOne` rejects every
non-`tekton.dev/v1` apiVersion up front. Restructure that gate to allow
the two `v1` kinds we now accept, then add the `ConfigMap` case. Replace
the `loadOne` function body's apiVersion check + switch with this:

```go
func loadOne(out *Bundle, data []byte) error {
	var head tektontypes.Object
	if err := yaml.Unmarshal(data, &head); err != nil {
		return fmt.Errorf("parse head: %w", err)
	}
	switch head.APIVersion {
	case "tekton.dev/v1":
		// fall through to the Tekton-kind switch below
	case "v1":
		// Core Kubernetes kinds we accept: ConfigMap, Secret.
		switch head.Kind {
		case "ConfigMap":
			return loadConfigMap(out, data)
		case "Secret":
			return loadSecret(out, data)
		default:
			return fmt.Errorf("unsupported v1 kind %q (only ConfigMap and Secret accepted at apiVersion v1)", head.Kind)
		}
	default:
		return fmt.Errorf("unsupported apiVersion %q (only tekton.dev/v1, or v1 for ConfigMap/Secret)", head.APIVersion)
	}
	switch head.Kind {
	case "Task":
		var t tektontypes.Task
		if err := yaml.Unmarshal(data, &t); err != nil {
			return fmt.Errorf("task: %w", err)
		}
		if _, dup := out.Tasks[t.Metadata.Name]; dup {
			return fmt.Errorf("duplicate Task %q", t.Metadata.Name)
		}
		out.Tasks[t.Metadata.Name] = t
	case "Pipeline":
		var p tektontypes.Pipeline
		if err := yaml.Unmarshal(data, &p); err != nil {
			return fmt.Errorf("pipeline: %w", err)
		}
		if _, dup := out.Pipelines[p.Metadata.Name]; dup {
			return fmt.Errorf("duplicate Pipeline %q", p.Metadata.Name)
		}
		out.Pipelines[p.Metadata.Name] = p
	case "PipelineRun":
		var pr tektontypes.PipelineRun
		if err := yaml.Unmarshal(data, &pr); err != nil {
			return fmt.Errorf("PipelineRun: %w", err)
		}
		out.PipelineRuns = append(out.PipelineRuns, pr)
	case "TaskRun":
		var tr tektontypes.TaskRun
		if err := yaml.Unmarshal(data, &tr); err != nil {
			return fmt.Errorf("TaskRun: %w", err)
		}
		out.TaskRuns = append(out.TaskRuns, tr)
	default:
		return fmt.Errorf("unsupported kind %q", head.Kind)
	}
	return nil
}
```

Then add the `loadConfigMap` helper (and its envelope struct) right
below `loadOne`:

```go
// configMapDoc is the shape we pull out of a `kind: ConfigMap` doc.
// `binaryData` is parsed only so we can reject it explicitly.
// `immutable` is parsed-and-ignored so the same YAML can apply against
// a real cluster.
type configMapDoc struct {
	Metadata   tektontypes.Metadata `json:"metadata"`
	Data       map[string]string    `json:"data,omitempty"`
	BinaryData map[string]string    `json:"binaryData,omitempty"`
	Immutable  *bool                `json:"immutable,omitempty"`
}

func loadConfigMap(out *Bundle, data []byte) error {
	var cm configMapDoc
	if err := yaml.Unmarshal(data, &cm); err != nil {
		return fmt.Errorf("ConfigMap: %w", err)
	}
	if cm.Metadata.Name == "" {
		return fmt.Errorf("ConfigMap: metadata.name is required")
	}
	if len(cm.BinaryData) > 0 {
		return fmt.Errorf("ConfigMap %q: binaryData is not supported (out of scope for tkn-act; use data)", cm.Metadata.Name)
	}
	if _, dup := out.ConfigMaps[cm.Metadata.Name]; dup {
		return fmt.Errorf("duplicate ConfigMap %q", cm.Metadata.Name)
	}
	bytesByKey := make(map[string][]byte, len(cm.Data))
	for k, v := range cm.Data {
		bytesByKey[k] = []byte(v)
	}
	out.ConfigMaps[cm.Metadata.Name] = bytesByKey
	return nil
}
```

For Task 2 we'll add the matching `loadSecret` and `secretDoc`. For now,
add a stub so the package still compiles when the merge function we'll
update next references the field:

```go
func loadSecret(out *Bundle, data []byte) error {
	return fmt.Errorf("Secret loading not yet implemented (Task 2)")
}
```

(This stub is a placeholder for the next task; remove it in Task 2 step 3
when the real `loadSecret` lands.)

- [ ] **Step 5: Extend `merge` for the new maps**

In `internal/loader/loader.go`, find `merge` and add the CM/Secret cases.
Final form:

```go
func merge(into, from *Bundle) error {
	for k, v := range from.Tasks {
		if _, dup := into.Tasks[k]; dup {
			return fmt.Errorf("duplicate Task %q across files", k)
		}
		into.Tasks[k] = v
	}
	for k, v := range from.Pipelines {
		if _, dup := into.Pipelines[k]; dup {
			return fmt.Errorf("duplicate Pipeline %q across files", k)
		}
		into.Pipelines[k] = v
	}
	for k, v := range from.ConfigMaps {
		if _, dup := into.ConfigMaps[k]; dup {
			return fmt.Errorf("duplicate ConfigMap %q across files", k)
		}
		into.ConfigMaps[k] = v
	}
	for k, v := range from.Secrets {
		if _, dup := into.Secrets[k]; dup {
			return fmt.Errorf("duplicate Secret %q across files", k)
		}
		into.Secrets[k] = v
	}
	into.PipelineRuns = append(into.PipelineRuns, from.PipelineRuns...)
	into.TaskRuns = append(into.TaskRuns, from.TaskRuns...)
	return nil
}
```

- [ ] **Step 6: Run the test**

```bash
go test -count=1 -run TestLoadConfigMap ./internal/loader/...
```

Expected: PASS. Also run the existing loader tests to confirm no
regression:

```bash
go test -count=1 ./internal/loader/...
```

Expected: every test OK (TestLoadFiles, TestRejectsUnknownKind,
TestRejectsWrongAPIVersion, TestLimitationsFixturesParse, plus the new
TestLoadConfigMap).

Note: `TestRejectsWrongAPIVersion` uses `tekton.dev/v1beta1` for a Task,
which is still rejected (still hits the `default` branch of the new
apiVersion switch). This must continue to pass.

- [ ] **Step 7: Add tests for binary-data rejection and duplicate-name**

Append to `internal/loader/loader_test.go`:

```go
func TestRejectsConfigMapBinaryData(t *testing.T) {
	yaml := []byte(`
apiVersion: v1
kind: ConfigMap
metadata: {name: c}
binaryData:
  k: aGVsbG8=
`)
	_, err := loader.LoadBytes(yaml)
	if err == nil || !strings.Contains(err.Error(), "binaryData") {
		t.Fatalf("err = %v, want binaryData error", err)
	}
}

func TestRejectsDuplicateConfigMap(t *testing.T) {
	yaml := []byte(`
apiVersion: v1
kind: ConfigMap
metadata: {name: c}
data: {k: v1}
---
apiVersion: v1
kind: ConfigMap
metadata: {name: c}
data: {k: v2}
`)
	_, err := loader.LoadBytes(yaml)
	if err == nil || !strings.Contains(err.Error(), "duplicate ConfigMap") {
		t.Fatalf("err = %v, want duplicate-ConfigMap error", err)
	}
}

func TestRejectsConfigMapMissingName(t *testing.T) {
	yaml := []byte(`
apiVersion: v1
kind: ConfigMap
data: {k: v}
`)
	_, err := loader.LoadBytes(yaml)
	if err == nil || !strings.Contains(err.Error(), "metadata.name") {
		t.Fatalf("err = %v, want metadata.name error", err)
	}
}
```

If `strings` isn't yet imported by `loader_test.go`, add it to the
imports block.

- [ ] **Step 8: Run tests**

```bash
go test -count=1 ./internal/loader/...
```

Expected: PASS.

- [ ] **Step 9: Commit**

```bash
git add internal/loader/loader.go internal/loader/loader_test.go internal/loader/testdata/cm-and-secret.yaml
git commit -m "feat(loader): accept kind: ConfigMap from -f (Track 1 #5)"
```

---

### Task 2: Loader accepts `kind: Secret` (data + stringData)

**Files:**
- Modify: `internal/loader/loader.go` (replace the `loadSecret` stub from Task 1)
- Test: `internal/loader/loader_test.go`
- Modify: `internal/loader/testdata/cm-and-secret.yaml` (extend with a Secret doc)

- [ ] **Step 1: Write the failing test**

Append to `internal/loader/loader_test.go`:

```go
func TestLoadSecretData(t *testing.T) {
	yaml := []byte(`
apiVersion: v1
kind: Secret
metadata: {name: s}
type: Opaque
data:
  token: aGVsbG8=
`)
	b, err := loader.LoadBytes(yaml)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	sec, ok := b.Secrets["s"]
	if !ok {
		t.Fatalf("missing secret; got %v", b.Secrets)
	}
	if got := sec["token"]; string(got) != "hello" {
		t.Errorf("token = %q, want hello (base64-decoded)", string(got))
	}
}

func TestLoadSecretStringData(t *testing.T) {
	yaml := []byte(`
apiVersion: v1
kind: Secret
metadata: {name: s}
stringData:
  raw: hello-plain
`)
	b, err := loader.LoadBytes(yaml)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	sec, ok := b.Secrets["s"]
	if !ok {
		t.Fatalf("missing secret; got %v", b.Secrets)
	}
	if got := sec["raw"]; string(got) != "hello-plain" {
		t.Errorf("raw = %q, want hello-plain", string(got))
	}
}

func TestLoadSecretStringDataWinsOverData(t *testing.T) {
	yaml := []byte(`
apiVersion: v1
kind: Secret
metadata: {name: s}
data:
  k: dmFsLWZyb20tZGF0YQ==     # "val-from-data"
stringData:
  k: val-from-stringData
`)
	b, err := loader.LoadBytes(yaml)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got := b.Secrets["s"]["k"]; string(got) != "val-from-stringData" {
		t.Errorf("k = %q, want val-from-stringData (stringData wins)", string(got))
	}
}

func TestRejectsSecretMalformedBase64(t *testing.T) {
	yaml := []byte(`
apiVersion: v1
kind: Secret
metadata: {name: s}
data:
  k: "!!!not-base64!!!"
`)
	_, err := loader.LoadBytes(yaml)
	if err == nil || !strings.Contains(err.Error(), "base64") {
		t.Fatalf("err = %v, want base64 decode error", err)
	}
}

func TestRejectsDuplicateSecret(t *testing.T) {
	yaml := []byte(`
apiVersion: v1
kind: Secret
metadata: {name: s}
stringData: {k: a}
---
apiVersion: v1
kind: Secret
metadata: {name: s}
stringData: {k: b}
`)
	_, err := loader.LoadBytes(yaml)
	if err == nil || !strings.Contains(err.Error(), "duplicate Secret") {
		t.Fatalf("err = %v, want duplicate-Secret error", err)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

```bash
go test -count=1 -run 'TestLoadSecret|TestRejectsSecret' ./internal/loader/...
```

Expected: FAIL — every Secret test errors with "Secret loading not yet implemented (Task 2)" (the Task 1 stub).

- [ ] **Step 3: Replace the stub with the real `loadSecret`**

In `internal/loader/loader.go`, add the import `encoding/base64` to the
import block, then replace the placeholder `loadSecret` and add a sibling
`secretDoc` envelope. Final form:

```go
// secretDoc is the shape we pull out of a `kind: Secret` doc.
// `type` is parsed-and-ignored (tkn-act always projects bytes opaquely).
// `immutable` is parsed-and-ignored so the same YAML can apply against
// a real cluster.
type secretDoc struct {
	Metadata   tektontypes.Metadata `json:"metadata"`
	Type       string               `json:"type,omitempty"`
	Data       map[string]string    `json:"data,omitempty"`
	StringData map[string]string    `json:"stringData,omitempty"`
	Immutable  *bool                `json:"immutable,omitempty"`
}

func loadSecret(out *Bundle, data []byte) error {
	var s secretDoc
	if err := yaml.Unmarshal(data, &s); err != nil {
		return fmt.Errorf("Secret: %w", err)
	}
	if s.Metadata.Name == "" {
		return fmt.Errorf("Secret: metadata.name is required")
	}
	if _, dup := out.Secrets[s.Metadata.Name]; dup {
		return fmt.Errorf("duplicate Secret %q", s.Metadata.Name)
	}
	bytesByKey := make(map[string][]byte, len(s.Data)+len(s.StringData))
	for k, v := range s.Data {
		dec, err := base64.StdEncoding.DecodeString(v)
		if err != nil {
			return fmt.Errorf("Secret %q: data[%q] is not valid base64: %w", s.Metadata.Name, k, err)
		}
		bytesByKey[k] = dec
	}
	// stringData wins over data on the same key (kube projection rule).
	for k, v := range s.StringData {
		bytesByKey[k] = []byte(v)
	}
	out.Secrets[s.Metadata.Name] = bytesByKey
	return nil
}
```

- [ ] **Step 4: Extend the loader testdata fixture with a Secret doc**

In `internal/loader/testdata/cm-and-secret.yaml`, append:

```yaml
---
apiVersion: v1
kind: Secret
metadata:
  name: app-secret
type: Opaque
data:
  token: aGVsbG8=
stringData:
  raw-token: hello-plain
```

Then add a top-level test that exercises the on-disk fixture in
`internal/loader/loader_test.go`:

```go
func TestLoadConfigMapAndSecretFromFile(t *testing.T) {
	bundle, err := loader.LoadFiles([]string{filepath.Join("testdata", "cm-and-secret.yaml")})
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if _, ok := bundle.ConfigMaps["app-config"]; !ok {
		t.Errorf("missing app-config CM; got %v", bundle.ConfigMaps)
	}
	sec, ok := bundle.Secrets["app-secret"]
	if !ok {
		t.Fatalf("missing app-secret; got %v", bundle.Secrets)
	}
	if string(sec["token"]) != "hello" {
		t.Errorf("token = %q, want hello (base64-decoded)", sec["token"])
	}
	if string(sec["raw-token"]) != "hello-plain" {
		t.Errorf("raw-token = %q, want hello-plain", sec["raw-token"])
	}
}
```

- [ ] **Step 5: Run tests**

```bash
go test -count=1 ./internal/loader/...
```

Expected: PASS. Re-run the prior task's CM tests too (they should still
pass — pure additive).

- [ ] **Step 6: Commit**

```bash
git add internal/loader/loader.go internal/loader/loader_test.go internal/loader/testdata/cm-and-secret.yaml
git commit -m "feat(loader): accept kind: Secret (data + stringData) from -f (Track 1 #5)"
```

---

### Task 3: `volumes.Store` ingests bundle-loaded resources

**Files:**
- Modify: `internal/volumes/store.go` (add `LoadBytes` method)
- Test: `internal/volumes/store_test.go`

The Store currently exposes `Add(name, key, value string)` for inline
overrides. We add a sibling `LoadBytes(name string, bytesByKey map[string][]byte)`
that records bundle-loaded data in a *separate* lower-precedence map,
so the existing inline-vs-dir precedence is preserved and we slot bundle
data underneath both.

- [ ] **Step 1: Write the failing test**

Append to `internal/volumes/store_test.go`:

```go
func TestStoreBundleIsLowestPrecedence(t *testing.T) {
	dir := t.TempDir()
	// On-disk dir: cfg/k = from-disk
	if err := os.MkdirAll(filepath.Join(dir, "cfg"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "cfg", "k"), []byte("from-disk"), 0o644); err != nil {
		t.Fatal(err)
	}
	s := volumes.NewStore(dir)
	// Bundle (lowest) loads cfg/k = from-bundle
	s.LoadBytes("cfg", map[string][]byte{"k": []byte("from-bundle")})

	got, err := s.Resolve("cfg")
	if err != nil {
		t.Fatal(err)
	}
	if string(got["k"]) != "from-disk" {
		t.Errorf("k = %q, want from-disk (dir beats bundle)", got["k"])
	}

	// Inline (highest) overrides both
	s.Add("cfg", "k", "from-inline")
	got, err = s.Resolve("cfg")
	if err != nil {
		t.Fatal(err)
	}
	if string(got["k"]) != "from-inline" {
		t.Errorf("k = %q, want from-inline (inline beats dir beats bundle)", got["k"])
	}
}

func TestStoreBundleOnlyResolves(t *testing.T) {
	s := volumes.NewStore("")
	s.LoadBytes("cfg", map[string][]byte{
		"a": []byte("alpha"),
		"b": []byte("beta"),
	})
	got, err := s.Resolve("cfg")
	if err != nil {
		t.Fatal(err)
	}
	if string(got["a"]) != "alpha" || string(got["b"]) != "beta" {
		t.Errorf("got %+v, want a=alpha b=beta", got)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

```bash
go test -count=1 -run TestStoreBundle ./internal/volumes/...
```

Expected: FAIL with `s.LoadBytes undefined`.

- [ ] **Step 3: Implement `LoadBytes` and the precedence chain**

In `internal/volumes/store.go`, add a new map field and method. Final
form (extends the existing struct without breaking callers):

```go
// Store is a configMap-or-secret bytes-source.
//
// Lookup precedence (highest first):
//
//  1. Inline overrides (Add) — typically from `--configmap` / `--secret`.
//  2. On-disk Dir layout — typically from `--configmap-dir` / `--secret-dir`.
//  3. Bundle-loaded bytes (LoadBytes) — typically from `kind: ConfigMap`
//     / `kind: Secret` resources found in the `-f` YAML stream.
//
// Each layer is checked per (name, key); a key present at a higher
// layer hides the same key at lower layers.
type Store struct {
	Dir    string                       // <root>/<name>/<key> per source
	Inline map[string]map[string]string // name -> key -> value
	Bundle map[string]map[string][]byte // name -> key -> bytes
}

// NewStore returns an empty Store rooted at dir. Inline overrides may be
// added via Add and bundle-loaded data via LoadBytes. dir may be empty
// (no on-disk layout).
func NewStore(dir string) *Store {
	return &Store{
		Dir:    dir,
		Inline: map[string]map[string]string{},
		Bundle: map[string]map[string][]byte{},
	}
}
```

(The struct gains one field and the constructor initializes one extra
map — neither change is breaking for callers because `Inline` is the
only field consumers reach into directly today.)

Then add the new method right under `Add`:

```go
// LoadBytes records bundle-loaded bytes for every key under `name`.
// These sit at the lowest precedence layer: a key present here is
// shadowed by the same key in either the on-disk Dir layout or the
// inline overrides.
//
// Calling LoadBytes twice for the same name is a merge (later keys
// overwrite earlier ones); duplicate-name detection happens at the
// loader layer, not here.
func (s *Store) LoadBytes(name string, bytesByKey map[string][]byte) {
	if s.Bundle[name] == nil {
		s.Bundle[name] = map[string][]byte{}
	}
	for k, v := range bytesByKey {
		// Copy the slice so later mutations of the caller's map don't
		// leak into the Store.
		cp := make([]byte, len(v))
		copy(cp, v)
		s.Bundle[name][k] = cp
	}
}
```

Now update `Resolve` to consult Bundle as the lowest layer. Find the
existing function and replace it with this version:

```go
// Resolve returns the bytes for every key declared by source `name`.
// Layers are merged with this precedence (higher beats lower):
//
//  1. Inline (Add)
//  2. On-disk Dir
//  3. Bundle (LoadBytes)
//
// An error is returned only when no layer produced any keys.
func (s *Store) Resolve(name string) (map[string][]byte, error) {
	out := map[string][]byte{}
	// 3. Bundle (lowest).
	for k, v := range s.Bundle[name] {
		cp := make([]byte, len(v))
		copy(cp, v)
		out[k] = cp
	}
	// 2. On-disk dir.
	if s.Dir != "" {
		base := filepath.Join(s.Dir, name)
		entries, err := os.ReadDir(base)
		if err == nil {
			for _, e := range entries {
				if e.IsDir() {
					continue
				}
				data, rerr := os.ReadFile(filepath.Join(base, e.Name()))
				if rerr != nil {
					return nil, fmt.Errorf("read %s/%s: %w", name, e.Name(), rerr)
				}
				out[e.Name()] = data
			}
		} else if !os.IsNotExist(err) {
			return nil, fmt.Errorf("read source %s: %w", name, err)
		}
	}
	// 1. Inline (highest).
	for k, v := range s.Inline[name] {
		out[k] = []byte(v)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("source %q has no keys (looked in %s, inline overrides, and -f-loaded resources; pass --configmap/--secret, populate the dir, or include kind: ConfigMap/Secret in -f)", name, s.Dir)
	}
	return out, nil
}
```

- [ ] **Step 4: Run tests**

```bash
go test -count=1 ./internal/volumes/...
```

Expected: PASS — both new tests + every existing test (the inline-wins
test is unchanged in semantics; the no-keys-error message changed but
the assertion uses `strings.Contains(err.Error(), "no keys")` which
still matches).

- [ ] **Step 5: Commit**

```bash
git add internal/volumes/store.go internal/volumes/store_test.go
git commit -m "feat(volumes): Store.LoadBytes; bundle layer beneath dir/inline (Track 1 #5)"
```

---

### Task 4: CLI feeds bundle-loaded CM/Secret into the stores

**Files:**
- Modify: `cmd/tkn-act/run.go` — refactor `buildVolumeStores` to accept the loaded `*loader.Bundle`; call `LoadBytes` for each entry
- Test: new `cmd/tkn-act/run_test.go` (small unit test for the precedence wiring at the CLI seam)

- [ ] **Step 1: Write the failing test**

Create `cmd/tkn-act/run_test.go`:

```go
package main

import (
	"testing"

	"github.com/danielfbm/tkn-act/internal/loader"
)

// TestBuildVolumeStoresIngestsBundle asserts that bundle-loaded
// ConfigMap/Secret bytes flow into the Store and are visible via
// Resolve, while inline overrides still win on the same key.
func TestBuildVolumeStoresIngestsBundle(t *testing.T) {
	// Reset any global flag state from other tests in this package.
	gf = globalFlags{
		configMaps: []string{"app-config=greeting=from-inline"},
	}
	b := &loader.Bundle{
		ConfigMaps: map[string]map[string][]byte{
			"app-config": {
				"greeting": []byte("from-bundle"),
				"lang":     []byte("en"),
			},
			"other-cfg": {
				"k": []byte("v"),
			},
		},
		Secrets: map[string]map[string][]byte{
			"app-secret": {"token": []byte("hunter2")},
		},
	}

	cm, sec, err := buildVolumeStores(t.TempDir(), b)
	if err != nil {
		t.Fatalf("buildVolumeStores: %v", err)
	}
	got, err := cm.Resolve("app-config")
	if err != nil {
		t.Fatalf("resolve app-config: %v", err)
	}
	if s := string(got["greeting"]); s != "from-inline" {
		t.Errorf("greeting = %q, want from-inline (inline beats bundle)", s)
	}
	if s := string(got["lang"]); s != "en" {
		t.Errorf("lang = %q, want en (bundle-only key)", s)
	}
	got2, err := cm.Resolve("other-cfg")
	if err != nil {
		t.Fatalf("resolve other-cfg: %v", err)
	}
	if s := string(got2["k"]); s != "v" {
		t.Errorf("other-cfg.k = %q, want v", s)
	}
	gotSec, err := sec.Resolve("app-secret")
	if err != nil {
		t.Fatalf("resolve app-secret: %v", err)
	}
	if s := string(gotSec["token"]); s != "hunter2" {
		t.Errorf("token = %q, want hunter2 (bundle)", s)
	}
}

// TestBuildVolumeStoresNilBundleIsBackwardCompatible ensures the
// old call signature semantics survive when no `-f`-loaded
// resources are present.
func TestBuildVolumeStoresNilBundleIsBackwardCompatible(t *testing.T) {
	gf = globalFlags{}
	cm, sec, err := buildVolumeStores(t.TempDir(), nil)
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if cm == nil || sec == nil {
		t.Fatal("expected non-nil stores")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

```bash
go test -count=1 -run 'TestBuildVolumeStores' ./cmd/tkn-act/...
```

Expected: FAIL — `buildVolumeStores` only accepts `(cacheRoot string)`
today; the test passes a `*loader.Bundle` second arg.

- [ ] **Step 3: Refactor `buildVolumeStores` to accept the bundle**

In `cmd/tkn-act/run.go`, change the signature and body of
`buildVolumeStores`:

```go
// buildVolumeStores assembles the configMap / secret stores from the
// global flags AND from any kind: ConfigMap / kind: Secret resources
// loaded from the -f YAML stream (b may be nil).
//
// Layered precedence inside each Store (highest first):
//   1. inline --configmap / --secret entries
//   2. on-disk --configmap-dir / --secret-dir layout
//   3. -f-loaded `kind: ConfigMap` / `kind: Secret` resources
//
// See internal/volumes/store.go for the layer mechanics.
func buildVolumeStores(cacheRoot string, b *loader.Bundle) (cm *volumes.Store, sec *volumes.Store, err error) {
	cmDir := gf.configMapDir
	if cmDir == "" {
		cmDir = filepath.Join(cacheRoot, "configmaps")
	}
	secDir := gf.secretDir
	if secDir == "" {
		secDir = filepath.Join(cacheRoot, "secrets")
	}
	cm = volumes.NewStore(cmDir)
	sec = volumes.NewStore(secDir)

	// 3. Bundle-loaded resources first (lowest layer; they get shadowed
	// by anything in dir or inline that names the same key).
	if b != nil {
		for name, bytesByKey := range b.ConfigMaps {
			cm.LoadBytes(name, bytesByKey)
		}
		for name, bytesByKey := range b.Secrets {
			sec.LoadBytes(name, bytesByKey)
		}
	}
	// 1. Inline flags last in this constructor — but they sit at the
	// HIGHEST precedence in Store.Resolve, which iterates layers in
	// inverse order. Order of Add() calls doesn't determine precedence;
	// the Store does.
	if err := parseInlineFlags(cm, gf.configMaps, "configmap"); err != nil {
		return nil, nil, err
	}
	if err := parseInlineFlags(sec, gf.secrets, "secret"); err != nil {
		return nil, nil, err
	}
	return cm, sec, nil
}
```

- [ ] **Step 4: Update the one caller of `buildVolumeStores`**

In `cmd/tkn-act/run.go`, find the call inside `runWith`:

```go
	// ConfigMap / Secret stores for volumes.
	cmStore, secStore, err := buildVolumeStores(cacheRoot)
```

Replace with:

```go
	// ConfigMap / Secret stores for volumes. Layered precedence:
	// inline flags > --*-dir > kind: ConfigMap/Secret loaded from -f.
	cmStore, secStore, err := buildVolumeStores(cacheRoot, b)
```

(The `b` here is the `*loader.Bundle` already in scope from the earlier
`loader.LoadFiles(files)` call.)

- [ ] **Step 5: Run the new tests**

```bash
go test -count=1 -run 'TestBuildVolumeStores' ./cmd/tkn-act/...
```

Expected: PASS.

- [ ] **Step 6: Run the full cmd/tkn-act suite to confirm no regressions**

```bash
go test -count=1 ./cmd/tkn-act/...
```

Expected: every test OK.

- [ ] **Step 7: Commit**

```bash
git add cmd/tkn-act/run.go cmd/tkn-act/run_test.go
git commit -m "feat(cli): feed -f-loaded ConfigMap/Secret into volume stores (Track 1 #5)"
```

---

### Task 5: Cross-backend e2e fixture for ConfigMap-from-YAML

**Files:**
- Create: `testdata/e2e/configmap-from-yaml/pipeline.yaml`
- Modify: `internal/e2e/fixtures/fixtures.go`

This fixture is the proof point: a single `-f`-loadable file ships a
Pipeline + Task + ConfigMap, and the Task mounts the CM. No CLI flags,
no on-disk dir, no inline `ConfigMaps:` map in the fixture descriptor —
the CM data lives entirely in the YAML.

- [ ] **Step 1: Write the fixture YAML**

Create `testdata/e2e/configmap-from-yaml/pipeline.yaml`:

```yaml
apiVersion: v1
kind: ConfigMap
metadata:
  name: app-config
data:
  greeting: hello-from-yaml
---
apiVersion: tekton.dev/v1
kind: Task
metadata:
  name: cm-eater
spec:
  volumes:
    - name: app-config
      configMap:
        name: app-config
  steps:
    - name: read
      image: alpine:3
      volumeMounts:
        - { name: app-config, mountPath: /etc/app-config }
      script: |
        cat /etc/app-config/greeting
        test "$(cat /etc/app-config/greeting)" = "hello-from-yaml"
---
apiVersion: tekton.dev/v1
kind: Pipeline
metadata:
  name: configmap-from-yaml
spec:
  tasks:
    - name: t
      taskRef: { name: cm-eater }
```

- [ ] **Step 2: Add the fixture to the shared table**

In `internal/e2e/fixtures/fixtures.go`, inside the `All()` table, add
this entry just after the `volumes` fixture (around line 99):

```go
		{Dir: "configmap-from-yaml", Pipeline: "configmap-from-yaml", WantStatus: "succeeded"},
```

Note: this entry deliberately has NO `ConfigMaps:` map. The CM data
lives in the YAML; the fixture descriptor stays minimal.

- [ ] **Step 3: Compile-check both tag builds**

```bash
go vet -tags integration ./...
go vet -tags cluster ./...
```

Expected: both exit 0.

- [ ] **Step 4: Run docker e2e locally if Docker is available (optional)**

```bash
docker info >/dev/null 2>&1 && go test -tags integration -run TestE2E/configmap-from-yaml -count=1 ./internal/e2e/... || echo "no docker; CI will run it"
```

Expected: PASS if Docker is up.

- [ ] **Step 5: Commit**

```bash
git add testdata/e2e/configmap-from-yaml/pipeline.yaml internal/e2e/fixtures/fixtures.go
git commit -m "test(e2e): configmap-from-yaml fixture (cross-backend)"
```

---

### Task 6: Cross-backend e2e fixture for Secret-from-YAML

**Files:**
- Create: `testdata/e2e/secret-from-yaml/pipeline.yaml`
- Modify: `internal/e2e/fixtures/fixtures.go`

The Secret fixture exercises both `data` (base64) and `stringData`
(plaintext) so the loader's two code paths are both touched in e2e.

- [ ] **Step 1: Write the fixture YAML**

Create `testdata/e2e/secret-from-yaml/pipeline.yaml`:

```yaml
apiVersion: v1
kind: Secret
metadata:
  name: app-secret
type: Opaque
data:
  token: aGVsbG8=          # base64("hello")
stringData:
  raw-token: hello-plain
---
apiVersion: tekton.dev/v1
kind: Task
metadata:
  name: secret-eater
spec:
  volumes:
    - name: app-secret
      secret:
        secretName: app-secret
  steps:
    - name: read
      image: alpine:3
      volumeMounts:
        - { name: app-secret, mountPath: /etc/app-secret }
      script: |
        cat /etc/app-secret/token
        test "$(cat /etc/app-secret/token)" = "hello"
        cat /etc/app-secret/raw-token
        test "$(cat /etc/app-secret/raw-token)" = "hello-plain"
---
apiVersion: tekton.dev/v1
kind: Pipeline
metadata:
  name: secret-from-yaml
spec:
  tasks:
    - name: t
      taskRef: { name: secret-eater }
```

- [ ] **Step 2: Add the fixture to the shared table**

In `internal/e2e/fixtures/fixtures.go`, right after the
`configmap-from-yaml` entry from Task 5:

```go
		{Dir: "secret-from-yaml", Pipeline: "secret-from-yaml", WantStatus: "succeeded"},
```

- [ ] **Step 3: Compile-check both tag builds**

```bash
go vet -tags integration ./...
go vet -tags cluster ./...
```

Expected: both exit 0.

- [ ] **Step 4: Run docker e2e locally if Docker is available (optional)**

```bash
docker info >/dev/null 2>&1 && go test -tags integration -run TestE2E/secret-from-yaml -count=1 ./internal/e2e/... || echo "no docker; CI will run it"
```

Expected: PASS if Docker is up.

- [ ] **Step 5: Commit**

```bash
git add testdata/e2e/secret-from-yaml/pipeline.yaml internal/e2e/fixtures/fixtures.go
git commit -m "test(e2e): secret-from-yaml fixture, exercising data + stringData (cross-backend)"
```

---

### Task 7: (Optional) Validator warning for unresolvable CM/Secret references

**Files:**
- Modify: `internal/validator/validator.go`
- Test: `internal/validator/validator_test.go`

This task is gated on a design ambiguity flagged in the self-review
section below ("How should the validator surface 'CM not present in any
source'?"). If the reviewer's answer is "skip for now," delete this task
before execution. If the answer is "warn but don't fail," follow the
steps below.

The reason it's hard to make this a hard error: the docker side already
errors at `MaterializeForTask` time with a useful message, and the
cluster side errors at `applyVolumeSources` time. A static validator
warning is mostly a UX nicety for the case where the user has neither a
CM in `-f` nor a `--configmap` flag and didn't realize it.

- [ ] **Step 1: Decide whether to ship this task**

Re-read the open question in the self-review section. If the answer is
"skip," delete this entire task and renumber Task 8/9 accordingly. If
the answer is "warn-not-fail" (default proposal), proceed to Step 2.

If the answer is "skip," the implementation agent must NOT touch
`internal/validator/`.

- [ ] **Step 2: Write the test**

Append to `internal/validator/validator_test.go`:

```go
func TestValidateCMReferenceWithoutSourceIsNotAnError(t *testing.T) {
	// A Task references a configMap volume but no source declares
	// "missing-cfg" yet. The validator should NOT fail (the runtime
	// volume materializer reports the actual error with a more useful
	// message). Provided here to lock in the negative-case behavior.
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
```

If `mustLoad` doesn't already exist in `validator_test.go`, copy it
from the pipelinerun-timeouts plan's helper.

- [ ] **Step 3: Run the test**

```bash
go test -count=1 -run TestValidateCMReferenceWithoutSource ./internal/validator/...
```

Expected: PASS (the validator doesn't currently check this; the test
is regression-locking).

- [ ] **Step 4: Commit (if anything changed)**

```bash
git add internal/validator/validator_test.go
git commit -m "test(validator): assert CM-reference-without-source is not a validator error"
```

(If only the test was added with no production code change, the
`tests-required` rule is satisfied because the change is test-only.)

---

### Task 8: Documentation convergence

**Files:**
- Modify: `AGENTS.md`
- Modify: `cmd/tkn-act/agentguide_data.md` (regenerated mirror of AGENTS.md)
- Modify: `docs/test-coverage.md`
- Modify: `docs/short-term-goals.md`
- Modify: `docs/feature-parity.md`
- Modify: `README.md`

- [ ] **Step 1: Update `AGENTS.md` — Environment-variables section**

In `AGENTS.md`, find the existing line under "Environment variables"
that documents `--configmap-dir` / `--secret-dir`:

```markdown
- `--configmap-dir <path>` / `--secret-dir <path>` — directory layout
  `<path>/<name>/<key>` per source for configMap and secret volumes.
  Defaults: `$XDG_CACHE_HOME/tkn-act/{configmaps,secrets}/`. Inline form:
  `--configmap <name>=<k1>=<v1>[,<k2>=<v2>...]` (repeatable; same shape
  for `--secret`). Inline overrides win over the on-disk dir per key.
```

Replace with the three-layer documentation:

```markdown
- `--configmap-dir <path>` / `--secret-dir <path>` — directory layout
  `<path>/<name>/<key>` per source for configMap and secret volumes.
  Defaults: `$XDG_CACHE_HOME/tkn-act/{configmaps,secrets}/`. Inline form:
  `--configmap <name>=<k1>=<v1>[,<k2>=<v2>...]` (repeatable; same shape
  for `--secret`). Three sources, layered (highest precedence first):
  1. inline `--configmap` / `--secret` flag,
  2. on-disk `--configmap-dir` / `--secret-dir`,
  3. `kind: ConfigMap` / `kind: Secret` (apiVersion `v1`) resources
     embedded in the same `-f` YAML stream as the Tasks/Pipelines.
  A higher layer overrides a lower layer per `(name, key)`. Both
  ConfigMap `data` and Secret `data` (base64) / `stringData` (plain)
  fields are honored; `stringData` wins over `data` for the same key
  per Kubernetes semantics. ConfigMap `binaryData` is rejected at load
  time. `Secret.type` is parsed-and-ignored — bytes are projected
  opaquely.
```

- [ ] **Step 2: Mirror `AGENTS.md` into `cmd/tkn-act/agentguide_data.md`**

```bash
go generate ./cmd/tkn-act/
diff cmd/tkn-act/agentguide_data.md AGENTS.md
```

Expected: no diff.

- [ ] **Step 3: Run the embedded-guide test**

```bash
go test -count=1 ./cmd/tkn-act/...
```

Expected: PASS.

- [ ] **Step 4: Update `docs/test-coverage.md`**

In `docs/test-coverage.md`, find the "By design (won't be tested
locally)" subsection and DELETE these three lines (the deferral note no
longer applies):

```markdown
- **Loading `kind: ConfigMap` / `kind: Secret` manifests** via
  `tkn-act -f`. Use `--configmap` / `--configmap-dir` (and the
  `--secret` equivalents). Manifest support is deferred to v1.3.
```

In the same file, find the `### -tags integration` table of fixtures
and add two rows after the existing `step-template/` row (the most
recent v1.4 addition):

```markdown
| `configmap-from-yaml/` | `kind: ConfigMap` (apiVersion v1) embedded in `-f`; mounted as a volume |
| `secret-from-yaml/`    | `kind: Secret` (apiVersion v1) embedded in `-f`; both `data` (base64) and `stringData` exercised |
```

- [ ] **Step 5: Mark Track 1 #5 done in `docs/short-term-goals.md`**

In the Track 1 table, find the row 5 Status cell:

```
| Deferred from v1.2. Loader and `volumes.Store` already in place; needs glue. |
```

Replace with:

```
| Done in v1.5 (PR for `feat: load ConfigMap/Secret from -f`). Loader accepts `kind: ConfigMap` / `kind: Secret` (apiVersion v1); `volumes.Store` gained a third lowest-precedence layer fed by the bundle. |
```

- [ ] **Step 6: Flip the `feature-parity.md` row**

In `docs/feature-parity.md` under `### Core types & parsing`, change:

```
| Loading `kind: ConfigMap` / `kind: Secret` from `-f` | `-f` | gap | both | none | none | docs/short-term-goals.md (Track 1 #5) |
```

to:

```
| Loading `kind: ConfigMap` / `kind: Secret` from `-f` | `-f` | shipped | both | configmap-from-yaml | none | docs/superpowers/plans/2026-05-03-cm-secret-from-yaml.md (Track 1 #5); also covered by `secret-from-yaml` |
```

- [ ] **Step 7: Run parity-check**

```bash
bash .github/scripts/parity-check.sh
```

Expected: `parity-check: docs/feature-parity.md, testdata/e2e/, and testdata/limitations/ are consistent.`

- [ ] **Step 8: Update `README.md`**

In `README.md`, find the "Tekton features supported" section and the
existing volumes line:

```markdown
- Volumes: `emptyDir`, `hostPath`, `configMap`, `secret`
  (inline via `--configmap`/`--secret` or directory layout)
```

Replace with:

```markdown
- Volumes: `emptyDir`, `hostPath`, `configMap`, `secret`
  (inline via `--configmap`/`--secret`, directory layout via
  `--configmap-dir`/`--secret-dir`, or `kind: ConfigMap` /
  `kind: Secret` resources embedded directly in the `-f` YAML stream)
```

- [ ] **Step 9: Commit**

```bash
git add cmd/tkn-act/agentguide_data.md AGENTS.md docs/test-coverage.md docs/short-term-goals.md docs/feature-parity.md README.md
git commit -m "docs: document CM/Secret-from-yaml; flip Track 1 #5 to shipped"
```

---

### Task 9: Final verification and PR

- [ ] **Step 1: Full local verification**

```bash
go vet ./... && go vet -tags integration ./... && go vet -tags cluster ./...
go build ./...
go test -race -count=1 ./...
bash .github/scripts/parity-check.sh
.github/scripts/tests-required.sh main HEAD
```

Expected: all exit 0; every test package OK or no-test-files.

- [ ] **Step 2: Push branch and open PR**

```bash
git push -u origin feat/cm-secret-from-yaml
gh pr create --title "feat: load kind: ConfigMap / kind: Secret from -f (Track 1 #5)" --body "$(cat <<'EOF'
## Summary

Closes Track 1 #5 of `docs/short-term-goals.md`. The same multi-doc
YAML stream that holds Tasks/Pipelines may now also include Kubernetes
`ConfigMap` and `Secret` resources (apiVersion `v1`), and `tkn-act run`
honors them as a third source for the `volumes.Store`.

- Loader: `kind: ConfigMap` and `kind: Secret` (apiVersion `v1`) are
  parsed into `Bundle.ConfigMaps` / `Bundle.Secrets` (name -> key ->
  bytes). `data` / `stringData` both honored; `stringData` wins on
  conflict. `binaryData` rejected at load time.
- `volumes.Store`: gained a `LoadBytes` layer beneath the existing
  on-disk dir and inline-flag layers. Final precedence (highest first):
  `--configmap` flag > `--configmap-dir` > `-f`-loaded YAML.
- CLI: `buildVolumeStores` now takes the loaded `*loader.Bundle` and
  calls `LoadBytes` for every entry before parsing inline flags.
- Cluster backend: no code change. Its `applyVolumeSources` already
  walks the same Store, so the new layer is consumed transparently.
- Two cross-backend fixtures (`configmap-from-yaml`, `secret-from-yaml`)
  exercise the new path on both the docker and cluster harnesses;
  neither uses any inline / on-disk source.

Implements `docs/superpowers/plans/2026-05-03-cm-secret-from-yaml.md`.

## Test plan

- [x] `go vet ./...` × {default, integration, cluster}
- [x] `go build ./...`
- [x] `go test -race -count=1 ./...`
- [x] `bash .github/scripts/parity-check.sh`
- [x] tests-required script
- [ ] docker-integration CI — runs the two new fixtures
- [ ] cluster-integration CI — same fixtures on real Tekton

🤖 Generated with [Claude Code](https://claude.com/claude-code)
EOF
)"
```

- [ ] **Step 3: Wait for CI green, then merge per project default**

```bash
gh pr merge <num> --squash --delete-branch
```

---

## Self-review notes

- **Spec coverage:** Loader CM → Task 1. Loader Secret (data + stringData
  + base64 errors + duplicate-name) → Task 2. Store ingestion + new
  precedence layer → Task 3. CLI wiring → Task 4. Cross-backend fidelity
  via two fixtures → Tasks 5 + 6 (both land in `fixtures.All()`, both
  harnesses iterate). Validator behavior → Task 7 (gated on the open
  question). Docs convergence → Task 8 (AGENTS.md + agentguide mirror,
  test-coverage delta, short-term-goals, feature-parity, README). Final
  ship → Task 9.
- **No placeholders:** every step has actual code or commands. Task 7
  is the only conditional task and includes an explicit "decide first"
  step with a default proposal.
- **Type consistency:** `Bundle.ConfigMaps` / `Bundle.Secrets` carry
  `map[string]map[string][]byte`; `Store.LoadBytes(name string,
  bytesByKey map[string][]byte)` matches; `Store.Resolve` returns the
  same shape. `loadConfigMap` / `loadSecret` / `configMapDoc` /
  `secretDoc` are package-internal symbols stable across Tasks 1 and 2.
- **Cluster pass-through "free":** `internal/backend/cluster/volumes.go`
  consumes `Store` via `Resolve`, so the new bundle layer is absorbed
  with zero new code on the cluster side. Verified by reading
  `applyVolumeSources` (line 27) and `resolveConfigMap` / `resolveSecret`
  (lines 85-97). The two new fixtures in `fixtures.All()` will run on
  the cluster harness as the regression lock.
- **Docs are atomic with the code:** Task 8 lands AGENTS.md ↔ embedded
  guide convergence, test-coverage.md, short-term-goals.md, feature-parity
  row flip, AND README.md in one commit so `parity-check` is satisfied
  at every commit boundary.
- **Tests-required rule:** every commit that touches non-test Go code
  also touches a `_test.go` file in the same commit (Task 1: loader code
  + loader_test; Task 2: loader code + loader_test; Task 3: store code +
  store_test; Task 4: run.go + run_test.go). Task 7 is test-only; Task 8
  is docs-only; both are independently fine.

### Open questions (please resolve before execution)

1. **Validator warning behavior (Task 7).** Current proposal: do NOT
   add a static "CM referenced but not present in any source" check;
   the runtime materializer's error is more accurate (post-merge across
   all three layers) and the static check would either duplicate it or
   get the precedence wrong. Task 7 reduces to a regression-locking
   negative test only. Alternative: emit a warning (not error) via a
   new return channel from `Validate`, but the current `Validate`
   signature is `[]error` with no warning slot, and adding one is a
   small but real API change. **Recommendation: keep Task 7 as
   regression-only; if the reviewer wants a real warning channel,
   that's a separate plan.**

2. **Should bundle CM/Secret resources also flow into the cluster
   backend's per-run namespace via the same `applyVolumeSources` path?**
   Verified by reading: yes, automatically. `applyVolumeSources` calls
   `b.opt.ConfigMaps.Resolve(name)`, which after Task 3 returns the
   bundle bytes when no higher-precedence layer overrides them. So the
   submitted Tekton `ConfigMap` / `Secret` carry the YAML-loaded data.
   No code change required. Confirming this in the cross-backend e2e
   fixtures (Tasks 5 + 6) is the proof.

3. **Should `discovery.Find` auto-pick up `*-cm.yaml` / `*-secret.yaml`?**
   Out of scope per the prompt; users pass them explicitly via `-f`.
   Worth a short note in AGENTS.md if the reviewer wants explicitness.
   Currently not added — the precedence + loader docs make the `-f`
   requirement obvious enough.
