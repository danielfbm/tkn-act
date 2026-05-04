package refresolver_test

import (
	"context"
	"errors"
	"testing"

	"github.com/danielfbm/tkn-act/internal/refresolver"
)

// stubResolver is a Resolver that returns a fixed payload and counts calls.
type stubResolver struct {
	name  string
	bytes []byte
	calls int
}

func (s *stubResolver) Name() string { return s.name }

func (s *stubResolver) Resolve(_ context.Context, req refresolver.Request) (refresolver.Resolved, error) {
	s.calls++
	return refresolver.Resolved{Bytes: s.bytes, Source: "stub:" + req.Resolver}, nil
}

// TestRegistryDispatchesByResolverName covers the basic path: a Registry
// with a single registered resolver dispatches Resolve to that resolver
// when the Request's Resolver name matches.
func TestRegistryDispatchesByResolverName(t *testing.T) {
	stub := &stubResolver{name: "stub", bytes: []byte("hello")}
	reg := refresolver.NewRegistry()
	reg.Register(stub)

	got, err := reg.Resolve(context.Background(), refresolver.Request{
		Kind:     refresolver.KindTask,
		Resolver: "stub",
	})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if string(got.Bytes) != "hello" {
		t.Errorf("bytes = %q, want hello", string(got.Bytes))
	}
	if stub.calls != 1 {
		t.Errorf("calls = %d, want 1", stub.calls)
	}
}

// TestRegistryRejectsUnknownResolver returns ErrResolverNotRegistered
// (or wraps it) when the requested resolver isn't in the registry. Phase
// 1 callers must produce the error string "resolver \"git\" not yet
// implemented in this release" / similar so users get a clear hint.
func TestRegistryRejectsUnknownResolver(t *testing.T) {
	reg := refresolver.NewRegistry()
	reg.Register(&stubResolver{name: "stub"})

	_, err := reg.Resolve(context.Background(), refresolver.Request{Resolver: "git"})
	if err == nil {
		t.Fatal("expected error for unknown resolver")
	}
	if !errors.Is(err, refresolver.ErrResolverNotRegistered) {
		t.Errorf("err = %v, want wrapping ErrResolverNotRegistered", err)
	}
}

// TestRegistryAllowList: a Registry configured with Allow=["stub"] must
// reject requests for any name not on the list, even if it's registered.
func TestRegistryAllowList(t *testing.T) {
	reg := refresolver.NewRegistry()
	reg.Register(&stubResolver{name: "stub"})
	// "git" isn't registered yet (Phase 2-4 land it); a hypothetical
	// future resolver would be blocked from dispatch by Allow.
	reg.Register(&stubResolver{name: "git"})
	reg.SetAllow([]string{"stub"})

	if _, err := reg.Resolve(context.Background(), refresolver.Request{Resolver: "stub"}); err != nil {
		t.Fatalf("stub allowed but resolve failed: %v", err)
	}
	_, err := reg.Resolve(context.Background(), refresolver.Request{Resolver: "git"})
	if err == nil {
		t.Fatal("expected error for non-allowed resolver")
	}
	if !errors.Is(err, refresolver.ErrResolverNotAllowed) {
		t.Errorf("err = %v, want wrapping ErrResolverNotAllowed", err)
	}
}

// TestRegistryEmptyResolverNameRejected: a Request whose Resolver is
// empty must fail loudly (the engine should never dispatch a
// resolver-backed taskRef whose Resolver string is "").
func TestRegistryEmptyResolverNameRejected(t *testing.T) {
	reg := refresolver.NewRegistry()
	_, err := reg.Resolve(context.Background(), refresolver.Request{})
	if err == nil {
		t.Fatal("expected error for empty resolver")
	}
}

// TestInlineResolverRegistered: NewDefaultRegistry pre-registers the
// "inline" stub resolver. The inline resolver is the magic name the
// test harness uses to feed bytes into the engine without touching the
// network. Phase 2 adds the git resolver; the remaining direct
// resolvers (hub/http/bundles/cluster) still return
// ErrResolverNotRegistered until their respective phases land.
func TestInlineResolverRegistered(t *testing.T) {
	reg := refresolver.NewDefaultRegistry(refresolver.Options{})
	if reg == nil {
		t.Fatal("nil registry")
	}
	// "inline" should be present and dispatch — but it has nothing
	// preloaded, so it returns ErrInlineNoData.
	_, err := reg.Resolve(context.Background(), refresolver.Request{Resolver: "inline"})
	if err == nil {
		t.Fatal("expected ErrInlineNoData with empty inline data")
	}
	if !errors.Is(err, refresolver.ErrInlineNoData) {
		t.Errorf("err = %v, want wrapping ErrInlineNoData", err)
	}

	// Phase-3+ resolvers must still be rejected with a "not yet
	// implemented" hint until their phase ships.
	_, err = reg.Resolve(context.Background(), refresolver.Request{Resolver: "hub"})
	if err == nil {
		t.Fatal("expected error for hub (not yet implemented)")
	}
	if !errors.Is(err, refresolver.ErrResolverNotRegistered) {
		t.Errorf("err = %v, want ErrResolverNotRegistered for hub", err)
	}
}

// TestInlineResolverServesPreloadedBytes covers the test-harness path:
// NewInlineResolver lets a test feed bytes keyed by ResolverParam
// "name" so the engine's lazy-dispatch can be exercised without any
// network or file IO.
func TestInlineResolverServesPreloadedBytes(t *testing.T) {
	inline := refresolver.NewInlineResolver()
	inline.Add("hello", []byte("apiVersion: tekton.dev/v1\nkind: Task"))

	reg := refresolver.NewRegistry()
	reg.Register(inline)

	res, err := reg.Resolve(context.Background(), refresolver.Request{
		Resolver: "inline",
		Params:   map[string]string{"name": "hello"},
	})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if string(res.Bytes) == "" {
		t.Errorf("expected preloaded bytes, got empty")
	}
	if res.SHA256 == "" {
		t.Errorf("expected SHA256 to be populated by inline resolver")
	}

	// Unknown name → ErrInlineNoData.
	_, err = reg.Resolve(context.Background(), refresolver.Request{
		Resolver: "inline",
		Params:   map[string]string{"name": "missing"},
	})
	if !errors.Is(err, refresolver.ErrInlineNoData) {
		t.Errorf("err = %v, want ErrInlineNoData", err)
	}
}

// TestCacheKey pins the substituted-params hashing rule from spec §3:
// cacheKey = sha256(resolver-name + "\x00" + sortedKVs(SUBSTITUTED-params)).
// Two requests with identical (resolver, params) yield the same key;
// any one-byte change in any value yields a different key.
func TestCacheKey(t *testing.T) {
	a := refresolver.CacheKey("git", map[string]string{"url": "u", "rev": "main"})
	b := refresolver.CacheKey("git", map[string]string{"rev": "main", "url": "u"})
	if a != b {
		t.Errorf("key differs by map iteration order: %s vs %s", a, b)
	}

	c := refresolver.CacheKey("git", map[string]string{"url": "u", "rev": "v2"})
	if a == c {
		t.Errorf("expected different keys for different params, got %s", a)
	}

	d := refresolver.CacheKey("hub", map[string]string{"url": "u", "rev": "main"})
	if a == d {
		t.Errorf("expected different keys for different resolver names, got %s", a)
	}

	if len(a) != 64 {
		t.Errorf("expected 64-char hex sha256, got %d chars (%s)", len(a), a)
	}
}
