package main

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/danielfbm/tkn-act/internal/exitcode"
)

const pipeTmpl = `apiVersion: tekton.dev/v1
kind: Pipeline
metadata: {name: NAME}
spec:
  tasks:
    - name: a
      taskSpec:
        steps: [{name: s, image: alpine, script: 'true'}]
`

func writeFile(t *testing.T, dir, name, body string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// isolateEnv points every tkn-act state/cache location at a tempdir.
func isolateEnv(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", filepath.Join(root, "cache"))
	t.Setenv("TKN_ACT_STATE_DIR", filepath.Join(root, "state"))
	return root
}

func decodeValidate(t *testing.T, stdout string) validateResult {
	t.Helper()
	var res validateResult
	if err := json.Unmarshal([]byte(stdout), &res); err != nil {
		t.Fatalf("decode: %v\n%s", err, stdout)
	}
	return res
}

// E1: positional args must be used (not silently ignored in favour of
// the cwd's pipeline.yaml).
func TestValidate_PositionalArgIsUsed(t *testing.T) {
	isolateEnv(t)
	cwd := t.TempDir()
	writeFile(t, cwd, "pipeline.yaml", strings.ReplaceAll(pipeTmpl, "NAME", "cwd-pipe"))
	other := writeFile(t, t.TempDir(), "other.yaml", strings.ReplaceAll(pipeTmpl, "NAME", "other-pipe"))
	t.Chdir(cwd)

	stdout, _, err := runRoot(t, []string{"validate", other, "-o", "json"})
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	if res := decodeValidate(t, stdout); res.Pipeline != "other-pipe" {
		t.Fatalf("pipeline = %q, want other-pipe (positional arg ignored?)", res.Pipeline)
	}
}

func TestValidate_PositionalMissingFileFails(t *testing.T) {
	isolateEnv(t)
	cwd := t.TempDir()
	writeFile(t, cwd, "pipeline.yaml", strings.ReplaceAll(pipeTmpl, "NAME", "cwd-pipe"))
	t.Chdir(cwd)
	_, _, err := runRoot(t, []string{"validate", filepath.Join(cwd, "nope.yaml")})
	if got := exitcode.From(err); got != exitcode.Validate {
		t.Fatalf("exit = %d, want %d (same as a missing -f); err=%v", got, exitcode.Validate, err)
	}
}

func TestValidate_PositionalDirIsUsageError(t *testing.T) {
	isolateEnv(t)
	_, _, err := runRoot(t, []string{"validate", t.TempDir()})
	if got := exitcode.From(err); got != exitcode.Usage {
		t.Fatalf("exit = %d, want %d; err=%v", got, exitcode.Usage, err)
	}
}

func TestRun_PositionalMissingFileFailsBeforeRun(t *testing.T) {
	root := isolateEnv(t)
	_, _, err := runRoot(t, []string{"run", filepath.Join(root, "nope.yaml")})
	if got := exitcode.From(err); got != exitcode.Validate {
		t.Fatalf("exit = %d, want %d; err=%v", got, exitcode.Validate, err)
	}
}

func TestResolveInputFiles_MergesFlagAndPositional(t *testing.T) {
	dir := t.TempDir()
	a := writeFile(t, dir, "a.yaml", "x")
	b := writeFile(t, dir, "b.yaml", "x")
	got, err := resolveInputFiles([]string{a}, []string{b}, "")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, []string{a, b}) {
		t.Fatalf("got %v", got)
	}
}

const tasksYAML = `apiVersion: tekton.dev/v1
kind: Task
metadata: {name: t}
spec:
  steps: [{name: s, image: alpine, script: 'true'}]
`

func TestValidate_PositionalAndFlagMerged(t *testing.T) {
	isolateEnv(t)
	dir := t.TempDir()
	tasks := writeFile(t, dir, "tasks.yaml", tasksYAML)
	pipe := writeFile(t, dir, "pipe.yaml", `apiVersion: tekton.dev/v1
kind: Pipeline
metadata: {name: p}
spec:
  tasks: [{name: a, taskRef: {name: t}}]
`)
	stdout, _, err := runRoot(t, []string{"validate", "-f", tasks, pipe, "-o", "json"})
	if err != nil {
		t.Fatalf("validate: %v\n%s", err, stdout)
	}
	if res := decodeValidate(t, stdout); !res.OK || res.Pipeline != "p" {
		t.Fatalf("res = %+v", res)
	}
}

const clusterResolverPipe = `apiVersion: tekton.dev/v1
kind: Pipeline
metadata: {name: p}
spec:
  tasks:
    - name: a
      taskRef:
        resolver: cluster
        params: [{name: name, value: t}]
`

// E2 + E4: validate applies the same resolver allow-list as run, and
// the documented --cluster-resolver-* flags opt `cluster` in.
func TestValidate_ClusterResolverOptIn(t *testing.T) {
	isolateEnv(t)
	f := writeFile(t, t.TempDir(), "p.yaml", clusterResolverPipe)

	_, _, err := runRoot(t, []string{"validate", "-f", f})
	if got := exitcode.From(err); got != exitcode.Validate {
		t.Fatalf("no opt-in: exit = %d, want %d (validate must apply --resolver-allow like run)", got, exitcode.Validate)
	}
	for _, flag := range []string{"--cluster-resolver-context=kind-dev", "--cluster-resolver-kubeconfig=/tmp/kc"} {
		if _, _, err := runRoot(t, []string{"validate", "-f", f, flag}); err != nil {
			t.Errorf("%s: want exit 0, got %v", flag, err)
		}
	}
}

func TestEffectiveResolverAllow(t *testing.T) {
	saved := gf
	t.Cleanup(func() { gf = saved })
	gf.resolverAllow = []string{"git"}
	gf.clusterResolverContext, gf.clusterResolverKubeconfig = "", ""
	if got := effectiveResolverAllow(); !reflect.DeepEqual(got, []string{"git"}) {
		t.Errorf("no opt-in: %v", got)
	}
	gf.clusterResolverKubeconfig = "/kc"
	if got := effectiveResolverAllow(); !reflect.DeepEqual(got, []string{"git", "cluster"}) {
		t.Errorf("kubeconfig opt-in: %v", got)
	}
	gf.resolverAllow = []string{"cluster"}
	if got := effectiveResolverAllow(); !reflect.DeepEqual(got, []string{"cluster"}) {
		t.Errorf("no duplicate: %v", got)
	}
	if !reflect.DeepEqual(gf.resolverAllow, []string{"cluster"}) {
		t.Errorf("flag slice mutated: %v", gf.resolverAllow)
	}
}

// E4: --offline cache misses are rejected by validate too.
func TestValidate_OfflineCacheMiss(t *testing.T) {
	isolateEnv(t)
	f := writeFile(t, t.TempDir(), "p.yaml", `apiVersion: tekton.dev/v1
kind: Pipeline
metadata: {name: p}
spec:
  tasks:
    - name: a
      taskRef:
        resolver: git
        params: [{name: url, value: https://example.com/r.git}, {name: pathInRepo, value: t.yaml}]
`)
	_, _, err := runRoot(t, []string{"validate", "-f", f, "--offline", "--resolver-cache-dir", t.TempDir()})
	if got := exitcode.From(err); got != exitcode.Validate {
		t.Fatalf("exit = %d, want %d", got, exitcode.Validate)
	}
}

const requiredParamPipe = `apiVersion: tekton.dev/v1
kind: Pipeline
metadata: {name: p}
spec:
  params: [{name: rev}]
  tasks:
    - name: a
      taskSpec:
        steps: [{name: s, image: alpine, script: 'true'}]
`

// E3: a missing required pipeline param is a validation error (exit 4)
// reported as an `error` event in -o json, and no run record is made.
func TestRun_MissingRequiredParamIsValidationError(t *testing.T) {
	root := isolateEnv(t)
	f := writeFile(t, t.TempDir(), "p.yaml", requiredParamPipe)
	stdout, _, err := runRoot(t, []string{"run", "-f", f, "-o", "json"})
	if got := exitcode.From(err); got != exitcode.Validate {
		t.Fatalf("exit = %d, want %d; err=%v", got, exitcode.Validate, err)
	}
	sc := bufio.NewScanner(strings.NewReader(stdout))
	found := false
	for sc.Scan() {
		var ev map[string]any
		if json.Unmarshal(sc.Bytes(), &ev) != nil {
			t.Fatalf("non-JSON line on stdout: %q", sc.Text())
		}
		if ev["kind"] == "error" && strings.Contains(ev["message"].(string), `missing required param "rev"`) {
			found = true
		}
	}
	if !found {
		t.Fatalf("no error event for missing param in stdout:\n%s", stdout)
	}
	if _, err := os.Stat(filepath.Join(root, "state", "index.json")); !os.IsNotExist(err) {
		t.Errorf("validation failure must not create a run record (stat err=%v)", err)
	}
}

func TestValidate_ParamFlagChecksRequiredParams(t *testing.T) {
	isolateEnv(t)
	f := writeFile(t, t.TempDir(), "p.yaml", requiredParamPipe)
	if _, _, err := runRoot(t, []string{"validate", "-f", f}); err != nil {
		t.Fatalf("validate without --param must skip the required-param check: %v", err)
	}
	_, _, err := runRoot(t, []string{"validate", "-f", f, "--param", "other=x"})
	if got := exitcode.From(err); got != exitcode.Validate {
		t.Fatalf("exit = %d, want %d", got, exitcode.Validate)
	}
	if _, _, err := runRoot(t, []string{"validate", "-f", f, "--param", "rev=main"}); err != nil {
		t.Fatalf("validate with rev: %v", err)
	}
	_, _, err = runRoot(t, []string{"validate", "-f", f, "--param", "noequals"})
	if got := exitcode.From(err); got != exitcode.Usage {
		t.Fatalf("bad --param: exit = %d, want %d", got, exitcode.Usage)
	}
}

func TestSelectPipeline_MultipleSortedUsage(t *testing.T) {
	isolateEnv(t)
	dir := t.TempDir()
	a := writeFile(t, dir, "a.yaml", strings.ReplaceAll(pipeTmpl, "NAME", "zeta"))
	b := writeFile(t, dir, "b.yaml", strings.ReplaceAll(pipeTmpl, "NAME", "alpha"))
	_, _, err := runRoot(t, []string{"validate", a, b})
	if got := exitcode.From(err); got != exitcode.Usage {
		t.Fatalf("exit = %d, want %d", got, exitcode.Usage)
	}
	if !strings.Contains(err.Error(), "[alpha zeta]") {
		t.Errorf("candidates should be sorted: %v", err)
	}
}
