package resolver_test

import (
	"testing"

	"github.com/dfbmorinigo/tkn-act/internal/resolver"
)

func TestSubstituteParam(t *testing.T) {
	got, err := resolver.Substitute("hello $(params.who)", resolver.Context{
		Params: map[string]string{"who": "world"},
	})
	if err != nil {
		t.Fatalf("sub: %v", err)
	}
	if got != "hello world" {
		t.Errorf("got %q", got)
	}
}

func TestSubstituteResult(t *testing.T) {
	got, err := resolver.Substitute("v=$(tasks.build.results.version)", resolver.Context{
		Results: map[string]map[string]string{"build": {"version": "1.2.3"}},
	})
	if err != nil {
		t.Fatalf("sub: %v", err)
	}
	if got != "v=1.2.3" {
		t.Errorf("got %q", got)
	}
}

func TestSubstituteContext(t *testing.T) {
	got, err := resolver.Substitute("$(context.taskRun.name)", resolver.Context{
		ContextVars: map[string]string{"taskRun.name": "tr-1"},
	})
	if err != nil {
		t.Fatalf("sub: %v", err)
	}
	if got != "tr-1" {
		t.Errorf("got %q", got)
	}
}

func TestUnknownReferenceErrors(t *testing.T) {
	_, err := resolver.Substitute("hi $(params.missing)", resolver.Context{})
	if err == nil {
		t.Fatal("expected error for missing param")
	}
}

func TestEscapedDollar(t *testing.T) {
	got, err := resolver.Substitute("price $$5", resolver.Context{})
	if err != nil {
		t.Fatalf("sub: %v", err)
	}
	if got != "price $5" {
		t.Errorf("got %q", got)
	}
}

func TestArrayParamStarExpansion(t *testing.T) {
	args, err := resolver.SubstituteArgs([]string{"$(params.flags[*])"}, resolver.Context{
		ArrayParams: map[string][]string{"flags": {"-a", "-b", "-c"}},
	})
	if err != nil {
		t.Fatalf("sub: %v", err)
	}
	want := []string{"-a", "-b", "-c"}
	// expansion should yield only the array itself (Tekton convention: $(params.x[*]) replaces the entire arg)
	if len(args) != 3 || args[0] != want[0] || args[1] != want[1] || args[2] != want[2] {
		t.Errorf("got %v", args)
	}
}
