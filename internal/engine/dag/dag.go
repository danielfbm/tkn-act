// Package dag implements a small directed acyclic graph: build, topological
// level grouping, cycle detection, descendant traversal. Nothing
// Tekton-specific lives here.
package dag

import (
	"fmt"
	"sort"
)

type Graph struct {
	nodes map[string]struct{}
	out   map[string]map[string]struct{} // adjacency: out[u][v] = edge u→v
	in    map[string]map[string]struct{}
}

func New() *Graph {
	return &Graph{
		nodes: map[string]struct{}{},
		out:   map[string]map[string]struct{}{},
		in:    map[string]map[string]struct{}{},
	}
}

func (g *Graph) AddNode(n string) {
	if _, ok := g.nodes[n]; ok {
		return
	}
	g.nodes[n] = struct{}{}
	g.out[n] = map[string]struct{}{}
	g.in[n] = map[string]struct{}{}
}

// AddEdge records u→v. Both endpoints must exist; missing endpoints surface as
// errors at Levels() time.
func (g *Graph) AddEdge(u, v string) {
	if _, ok := g.out[u]; !ok {
		g.out[u] = map[string]struct{}{}
	}
	if _, ok := g.in[v]; !ok {
		g.in[v] = map[string]struct{}{}
	}
	g.out[u][v] = struct{}{}
	g.in[v][u] = struct{}{}
}

// Levels groups nodes into execution levels: level 0 has no incoming edges;
// level i+1 has all nodes whose deps are all in levels 0..i. Returns an error
// if a cycle is detected or an edge references an unknown node.
func (g *Graph) Levels() ([][]string, error) {
	// validate edges
	for u, outs := range g.out {
		if _, ok := g.nodes[u]; !ok && len(outs) > 0 {
			return nil, fmt.Errorf("edge from unknown node %q", u)
		}
		for v := range outs {
			if _, ok := g.nodes[v]; !ok {
				return nil, fmt.Errorf("edge %q→%q to unknown node", u, v)
			}
		}
	}

	indeg := map[string]int{}
	for n := range g.nodes {
		indeg[n] = len(g.in[n])
	}

	var levels [][]string
	for len(indeg) > 0 {
		var ready []string
		for n, d := range indeg {
			if d == 0 {
				ready = append(ready, n)
			}
		}
		if len(ready) == 0 {
			remaining := make([]string, 0, len(indeg))
			for n := range indeg {
				remaining = append(remaining, n)
			}
			sort.Strings(remaining)
			return nil, fmt.Errorf("cycle detected among: %v", remaining)
		}
		sort.Strings(ready)
		levels = append(levels, ready)
		for _, n := range ready {
			delete(indeg, n)
			for v := range g.out[n] {
				if _, ok := indeg[v]; ok {
					indeg[v]--
				}
			}
		}
	}
	return levels, nil
}

// Descendants returns all nodes reachable from start (excluding start itself).
func (g *Graph) Descendants(start string) []string {
	seen := map[string]struct{}{}
	stack := []string{start}
	for len(stack) > 0 {
		u := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		for v := range g.out[u] {
			if _, ok := seen[v]; ok {
				continue
			}
			seen[v] = struct{}{}
			stack = append(stack, v)
		}
	}
	out := make([]string, 0, len(seen))
	for n := range seen {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// Nodes returns all node names, sorted.
func (g *Graph) Nodes() []string {
	out := make([]string, 0, len(g.nodes))
	for n := range g.nodes {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}
