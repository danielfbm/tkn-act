package resolver_test

import (
	"reflect"
	"strings"
	"testing"

	"github.com/danielfbm/tkn-act/internal/resolver"
)

// C1.1: shell command substitution `$(cmd)` is not a Tekton reference.
// Tekton leaves any $(...) whose prefix isn't a recognised namespace
// untouched; tkn-act must too (before, lookup errored and the engine
// silently replaced the whole field with "").
func TestUnknownNamespaceLeftLiteral(t *testing.T) {
	cases := []string{
		"touch /tmp/$(date).log",
		"cd $(pwd) && make",
		"$(hostname)",
		"a=$(whoami) b=$(params.p)",
	}
	ctx := resolver.Context{Params: map[string]string{"p": "P"}}
	for _, in := range cases {
		want := strings.ReplaceAll(in, "$(params.p)", "P")
		for name, fn := range map[string]func(string, resolver.Context) (string, error){
			"strict":        resolver.Substitute,
			"allowStepRefs": resolver.SubstituteAllowStepRefs,
			"deferStepRefs": resolver.SubstituteDeferStepRefs,
		} {
			got, err := fn(in, ctx)
			if err != nil {
				t.Errorf("%s(%q): unexpected error %v", name, in, err)
				continue
			}
			if got != want {
				t.Errorf("%s(%q) = %q, want %q", name, in, got, want)
			}
		}
	}
	args, err := resolver.SubstituteArgs([]string{"sh", "-c", "cd $(pwd) && make"}, ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(args, []string{"sh", "-c", "cd $(pwd) && make"}) {
		t.Errorf("args = %q", args)
	}
}

// Recognised namespaces still error on unknown names: a typo in
// $(params.x) must not silently survive.
func TestKnownNamespaceStillErrors(t *testing.T) {
	for _, in := range []string{"$(params.nope)", "$(context.nope)", "$(tasks.a.results.b)", "$(workspaces.w.nope)"} {
		if _, err := resolver.Substitute(in, resolver.Context{}); err == nil {
			t.Errorf("Substitute(%q): expected error", in)
		}
	}
}

// SubstituteDeferStepRefs defers ONLY step-scoped refs; everything else
// resolves (or errors) exactly like Substitute.
func TestSubstituteDeferStepRefs(t *testing.T) {
	ctx := resolver.Context{Params: map[string]string{"p": "P"}}
	got, err := resolver.SubstituteDeferStepRefs("$(params.p) $(steps.s.results.r) $(step.results.x.path)", ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got != "P $(steps.s.results.r) $(step.results.x.path)" {
		t.Errorf("got %q", got)
	}
	if _, err := resolver.SubstituteDeferStepRefs("$(params.missing)", ctx); err == nil {
		t.Error("expected error for unknown param in defer-step-refs mode")
	}
	args, err := resolver.SubstituteArgsDeferStepRefs([]string{"$(params.a[*])", "$(steps.s.results.r)"}, resolver.Context{ArrayParams: map[string][]string{"a": {"1", "2"}}})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(args, []string{"1", "2", "$(steps.s.results.r)"}) {
		t.Errorf("args = %q", args)
	}
	if _, err := resolver.SubstituteArgsDeferStepRefs([]string{"$(params.nope[*])"}, resolver.Context{}); err == nil {
		t.Error("expected error for unknown array param")
	}
}

func TestWorkspaceBoundClaimVolume(t *testing.T) {
	ctx := resolver.Context{BoundWorkspaces: map[string]bool{"src": true, "cache": false}}
	got, err := resolver.Substitute("$(workspaces.src.bound) $(workspaces.cache.bound) [$(workspaces.src.claim)] $(workspaces.src.volume)", ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got != "true false [] src" {
		t.Errorf("got %q", got)
	}
	// Without binding info: strict errors, allow-defer leaves intact.
	if _, err := resolver.Substitute("$(workspaces.src.bound)", resolver.Context{}); err == nil {
		t.Error("expected error without binding info")
	}
	got, err = resolver.SubstituteAllowStepRefs("$(workspaces.src.bound)", resolver.Context{})
	if err != nil || got != "$(workspaces.src.bound)" {
		t.Errorf("allow-defer: got %q, %v", got, err)
	}
}

// C1.3: $(tasks.status) and $(tasks.<t>.status).
func TestTasksStatus(t *testing.T) {
	ctx := resolver.Context{
		TasksStatus:  "Failed",
		TaskStatuses: map[string]string{"build": "Succeeded", "test": "Failed"},
	}
	got, err := resolver.Substitute("$(tasks.status) $(tasks.build.status) $(tasks.test.status)", ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got != "Failed Succeeded Failed" {
		t.Errorf("got %q", got)
	}
	if _, err := resolver.Substitute("$(tasks.nope.status)", ctx); err == nil {
		t.Error("expected error for unknown pipeline task")
	}
	// Outside finally (TaskStatuses nil) both forms error clearly.
	for _, in := range []string{"$(tasks.status)", "$(tasks.build.status)"} {
		_, err := resolver.Substitute(in, resolver.Context{})
		if err == nil || !strings.Contains(err.Error(), "finally") {
			t.Errorf("Substitute(%q) err = %v, want finally-only error", in, err)
		}
	}
	// A task literally named "status" still resolves its results.
	got, err = resolver.Substitute("$(tasks.status.results.r)", resolver.Context{Results: map[string]map[string]string{"status": {"r": "v"}}})
	if err != nil || got != "v" {
		t.Errorf("got %q, %v", got, err)
	}
}

// C1.4: [N] indexing and object .key for params and task results.
func TestIndexAndObjectKeyRefs(t *testing.T) {
	ctx := resolver.Context{
		ArrayParams:  map[string][]string{"arr": {"a", "b", "c"}},
		ObjectParams: map[string]map[string]string{"obj": {"k": "v"}},
		Results: map[string]map[string]string{
			"build": {
				"list":  `["x","y"]`,
				"o":     `{"url":"https://e","digest":"sha"}`,
				"plain": "p",
			},
		},
	}
	got, err := resolver.Substitute("$(params.arr[1]) $(params.obj.k) $(tasks.build.results.list[1]) $(tasks.build.results.o.digest) $(tasks.build.results.plain)", ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got != "b v y sha p" {
		t.Errorf("got %q", got)
	}
	for _, bad := range []string{
		"$(params.arr[3])",                // out of range
		"$(params.nope[0])",               // unknown array
		"$(tasks.build.results.list[5])",  // out of range
		"$(tasks.build.results.plain[0])", // not a JSON array
		"$(tasks.build.results.o.nokey)",  // missing key
		"$(tasks.build.results.plain.k)",  // not a JSON object
	} {
		if _, err := resolver.Substitute(bad, ctx); err == nil {
			t.Errorf("Substitute(%q): expected error", bad)
		}
	}
	// [*] embedded in a string is invalid Tekton; error clearly.
	for _, in := range []string{"pre $(params.arr[*])", "$(tasks.build.results.list[*]) post"} {
		_, err := resolver.Substitute(in, ctx)
		if err == nil || !strings.Contains(err.Error(), "whole") {
			t.Errorf("Substitute(%q) err = %v, want whole-array error", in, err)
		}
	}
}

func TestIsArrayStarRef(t *testing.T) {
	for in, want := range map[string]bool{
		"$(params.a[*])":                 true,
		"$(tasks.t.results.r[*])":        true,
		"$(params.a[*]) $(params.b[*])":  false,
		"x$(params.a[*])":                false,
		"$(params.a)":                    false,
		"$(context.a[*])":                false,
		"$(params.a[*]) and $(params.b)": false,
	} {
		if got := resolver.IsArrayStarRef(in); got != want {
			t.Errorf("IsArrayStarRef(%q) = %v, want %v", in, got, want)
		}
	}
}
