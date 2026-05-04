package refresolver

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"sync"
)

// Kind disambiguates whether a Request resolves to a Task or a Pipeline.
type Kind int

const (
	KindTask Kind = iota
	KindPipeline
)

// Request is the input shape resolvers consume. Params are already
// substituted (the engine ran the same $(...) substitution it runs on
// PipelineTask.Params before dispatching to the resolver), so resolver
// implementations see only literal values.
type Request struct {
	Kind     Kind
	Resolver string
	// Params hold resolver-specific keys. Always already substituted —
	// resolver implementations must never see "$(tasks.X.results.Y)".
	Params map[string]string
}

// Resolved is the output shape. Bytes is the raw YAML the loader
// consumes via loader.LoadBytes; Source is a human-readable origin
// string for the resolver-end event; SHA256 is the hex digest of the
// returned Bytes (used for the on-disk cache invalidation diagnostic).
type Resolved struct {
	Bytes  []byte
	Source string
	SHA256 string
	// Cached is true if the bytes came from a cache layer rather than
	// a fresh fetch. Surfaces in resolver-end events.
	Cached bool
}

// Resolver is the per-protocol fetcher. Implementations live in this
// package as files (git.go, hub.go, ...) but the interface is the only
// thing the engine sees.
type Resolver interface {
	Name() string
	Resolve(ctx context.Context, req Request) (Resolved, error)
}

// Sentinel errors. Engine code uses errors.Is to branch on these.
var (
	// ErrResolverNotRegistered fires when no Resolver is registered
	// for the requested name. In Phase 1, every name except "inline"
	// triggers this.
	ErrResolverNotRegistered = errors.New("refresolver: resolver not registered")

	// ErrResolverNotAllowed fires when the requested name is registered
	// but excluded by the active allow-list (--resolver-allow=...).
	ErrResolverNotAllowed = errors.New("refresolver: resolver not allowed")

	// ErrInlineNoData fires when the inline resolver is asked for a
	// name the test harness didn't preload. Distinct from
	// ErrResolverNotRegistered so tests can branch on harness misuse
	// vs Phase-1-not-implemented errors.
	ErrInlineNoData = errors.New("refresolver: inline resolver has no data for the requested key")
)

// Options configures a Registry built via NewDefaultRegistry. Phase 1
// only honors Allow + CacheDir + Offline; later phases plug concrete
// resolvers in here.
type Options struct {
	// Allow restricts which resolver names will dispatch. Empty
	// means "no allow-list applied" (every registered resolver
	// dispatches). The CLI default in Phase 1 is
	// {"git","hub","http","bundles"} — none of those are registered
	// yet, but the validator's allow-list checks already use this
	// list.
	Allow []string
	// CacheDir is the on-disk cache location. Phase 1 stores it but
	// does not yet read or write it (Phase 6 wires the cache).
	CacheDir string
	// Offline rejects any cache miss with an error. Phase 1 stores it
	// but does not yet enforce it (Phase 6 wires --offline).
	Offline bool
	// AllowInsecureHTTP opts the http resolver into plain http://. Phase
	// 1 stores it but does nothing with it (Phase 3 wires it).
	AllowInsecureHTTP bool
}

// Registry routes Requests to one of its registered Resolvers, applies
// the allow-list, and (in later phases) layers a per-run + on-disk
// cache on top.
type Registry struct {
	mu     sync.Mutex
	direct map[string]Resolver
	allow  map[string]struct{}
	opts   Options
	// PerRunCache is a small in-memory map[cacheKey]Resolved the engine
	// populates per run. Exposed so tests can inspect cache hits.
	perRun map[string]Resolved
}

// NewRegistry returns an empty Registry. Tests that want a single stub
// resolver call this and then Register their stub.
func NewRegistry() *Registry {
	return &Registry{
		direct: map[string]Resolver{},
		perRun: map[string]Resolved{},
	}
}

// NewDefaultRegistry returns a Registry pre-populated with whatever
// resolvers ship by default. Phase 1 shipped just the inline stub; Phase
// 2 adds the git resolver. Phase 3-4 add hub/http/bundles/cluster here.
func NewDefaultRegistry(opts Options) *Registry {
	r := NewRegistry()
	r.opts = opts
	if len(opts.Allow) > 0 {
		r.allow = map[string]struct{}{}
		for _, n := range opts.Allow {
			r.allow[n] = struct{}{}
		}
	}
	r.Register(NewInlineResolver())

	// Direct git resolver (Phase 2). The on-disk cache root is
	// <CacheDir>/git/<sha256(url+rev)>/repo/. CacheDir empty falls
	// back to per-call tempdirs (cache disabled).
	gitR := NewGit(opts.CacheDir)
	gitR.SetAllowInsecureHTTP(opts.AllowInsecureHTTP)
	r.Register(gitR)

	// Phase 3: hub direct resolver. Sibling phases register
	// on the same hook (Phase 4: bundles + cluster).
	r.Register(NewHubResolver(HubOptions{}))
	return r
}

// Register adds a Resolver to the registry, keyed by its Name(). A
// repeat call with the same Name() replaces the prior entry.
func (r *Registry) Register(res Resolver) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.direct[res.Name()] = res
}

// SetAllow replaces the allow-list. Empty / nil means "no allow-list
// applied" (every registered resolver dispatches).
func (r *Registry) SetAllow(names []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(names) == 0 {
		r.allow = nil
		return
	}
	r.allow = map[string]struct{}{}
	for _, n := range names {
		r.allow[n] = struct{}{}
	}
}

// Inline returns the inline resolver registered with this Registry, or
// nil if none was registered. Used by tests to feed bytes in.
func (r *Registry) Inline() *InlineResolver {
	r.mu.Lock()
	defer r.mu.Unlock()
	if i, ok := r.direct["inline"].(*InlineResolver); ok {
		return i
	}
	return nil
}

// Resolve dispatches the Request to the appropriate Resolver. The
// per-run cache short-circuits repeated identical requests within a
// single run. Phase 1 does not yet consult the on-disk cache (Phase 6).
func (r *Registry) Resolve(ctx context.Context, req Request) (Resolved, error) {
	if req.Resolver == "" {
		return Resolved{}, fmt.Errorf("refresolver: empty Resolver name in Request")
	}

	key := CacheKey(req.Resolver, req.Params)

	r.mu.Lock()
	if cached, ok := r.perRun[key]; ok {
		c := cached
		c.Cached = true
		r.mu.Unlock()
		return c, nil
	}
	if r.allow != nil {
		if _, ok := r.allow[req.Resolver]; !ok {
			r.mu.Unlock()
			return Resolved{}, fmt.Errorf("%w: %q not in --resolver-allow", ErrResolverNotAllowed, req.Resolver)
		}
	}
	res, ok := r.direct[req.Resolver]
	r.mu.Unlock()
	if !ok {
		return Resolved{}, fmt.Errorf("%w: %q (not yet implemented in this release)", ErrResolverNotRegistered, req.Resolver)
	}

	out, err := res.Resolve(ctx, req)
	if err != nil {
		return out, err
	}
	if out.SHA256 == "" {
		out.SHA256 = sha256Bytes(out.Bytes)
	}
	r.mu.Lock()
	r.perRun[key] = out
	r.mu.Unlock()
	return out, nil
}

// CacheKey computes the per-(resolver, substituted-params) cache key
// per spec §3:
//
//	sha256(resolver-name + "\x00" + sortedKVs(SUBSTITUTED-params))
//
// The hash is computed AFTER the engine has substituted each param's
// $(...) references against the dispatch-time resolver.Context, so two
// PipelineTasks resolving the same upstream Pipeline / Task via the
// same resolver but whose `resolver.params` substitute to different
// values yield different cache keys (and miss the per-run cache
// independently). This invariant is exercised by
// TestRunOneDoesNotCacheAcrossDifferentSubstitutedParams.
func CacheKey(resolverName string, params map[string]string) string {
	keys := make([]string, 0, len(params))
	for k := range params {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	h := sha256.New()
	h.Write([]byte(resolverName))
	h.Write([]byte{0})
	for i, k := range keys {
		if i > 0 {
			h.Write([]byte{0})
		}
		h.Write([]byte(k))
		h.Write([]byte{'='})
		h.Write([]byte(params[k]))
	}
	return hex.EncodeToString(h.Sum(nil))
}

func sha256Bytes(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// InlineResolver is a magic resolver used by the test harness. Tests
// preload (key, bytes) pairs via Add; the engine's lazy-dispatch path
// invokes Resolve with the same key in Request.Params["name"]. Real
// resolvers (git, hub, ...) land in Phase 2-4.
type InlineResolver struct {
	mu      sync.Mutex
	entries map[string][]byte
}

// NewInlineResolver returns an empty InlineResolver. Use Add to load
// bytes the engine will look up by Request.Params["name"].
func NewInlineResolver() *InlineResolver {
	return &InlineResolver{entries: map[string][]byte{}}
}

// Add registers bytes for the given key. The engine's resolver Request
// must carry Params["name"] equal to key for the inline lookup to fire.
func (i *InlineResolver) Add(key string, bytes []byte) {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.entries[key] = bytes
}

// Name implements Resolver.
func (i *InlineResolver) Name() string { return "inline" }

// Resolve implements Resolver. The lookup key is Request.Params["name"];
// missing or unknown names produce ErrInlineNoData.
func (i *InlineResolver) Resolve(_ context.Context, req Request) (Resolved, error) {
	name := req.Params["name"]
	i.mu.Lock()
	defer i.mu.Unlock()
	bytes, ok := i.entries[name]
	if !ok {
		return Resolved{}, fmt.Errorf("%w: name=%q", ErrInlineNoData, name)
	}
	return Resolved{
		Bytes:  bytes,
		Source: fmt.Sprintf("inline:%s", name),
		SHA256: sha256Bytes(bytes),
	}, nil
}
