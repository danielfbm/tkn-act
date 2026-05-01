package dag_test

import (
	"reflect"
	"testing"

	"github.com/dfbmorinigo/tkn-act/internal/engine/dag"
)

func TestLevelsLinear(t *testing.T) {
	g := dag.New()
	g.AddNode("a")
	g.AddNode("b")
	g.AddNode("c")
	g.AddEdge("a", "b")
	g.AddEdge("b", "c")

	levels, err := g.Levels()
	if err != nil {
		t.Fatalf("levels: %v", err)
	}
	want := [][]string{{"a"}, {"b"}, {"c"}}
	if !equal(levels, want) {
		t.Errorf("got %v, want %v", levels, want)
	}
}

func TestLevelsParallel(t *testing.T) {
	g := dag.New()
	g.AddNode("a")
	g.AddNode("b1")
	g.AddNode("b2")
	g.AddNode("c")
	g.AddEdge("a", "b1")
	g.AddEdge("a", "b2")
	g.AddEdge("b1", "c")
	g.AddEdge("b2", "c")

	levels, err := g.Levels()
	if err != nil {
		t.Fatalf("levels: %v", err)
	}
	if len(levels) != 3 {
		t.Fatalf("levels = %d, want 3", len(levels))
	}
	if !sameSet(levels[1], []string{"b1", "b2"}) {
		t.Errorf("level 1 = %v, want b1,b2 in any order", levels[1])
	}
}

func TestCycleDetected(t *testing.T) {
	g := dag.New()
	g.AddNode("a")
	g.AddNode("b")
	g.AddEdge("a", "b")
	g.AddEdge("b", "a")

	_, err := g.Levels()
	if err == nil {
		t.Fatal("expected cycle error")
	}
}

func TestUnknownEdge(t *testing.T) {
	g := dag.New()
	g.AddNode("a")
	g.AddEdge("a", "ghost")
	_, err := g.Levels()
	if err == nil {
		t.Fatal("expected error for edge to unknown node")
	}
}

func TestDescendantsOf(t *testing.T) {
	g := dag.New()
	g.AddNode("a")
	g.AddNode("b")
	g.AddNode("c")
	g.AddNode("d")
	g.AddEdge("a", "b")
	g.AddEdge("b", "c")
	g.AddEdge("a", "d") // d is sibling of b, no further descendants
	got := g.Descendants("a")
	if !sameSet(got, []string{"b", "c", "d"}) {
		t.Errorf("descendants(a) = %v", got)
	}
	got = g.Descendants("b")
	if !sameSet(got, []string{"c"}) {
		t.Errorf("descendants(b) = %v", got)
	}
}

func equal(a, b [][]string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if !sameSet(a[i], b[i]) {
			return false
		}
	}
	return true
}

func sameSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	m := map[string]int{}
	for _, x := range a {
		m[x]++
	}
	for _, x := range b {
		m[x]--
	}
	for _, v := range m {
		if v != 0 {
			return false
		}
	}
	_ = reflect.DeepEqual
	return true
}
