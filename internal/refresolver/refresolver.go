package refresolver

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"sync"

	"github.com/danielfbm/tkn-act/internal/debug"
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

	// AllowCluster opts the cluster resolver into the default registry.
	// The cluster resolver reads from the user's KUBECONFIG and is
	// disabled by default for safety (KUBECONFIG may point at
	// production). Set true via --resolver-allow=...,cluster on the
	// CLI; alternatively the engine flips this when --cluster-resolver-
	// context=<ctx> is set explicitly.
	AllowCluster bool

	// ClusterResolverContext, when non-empty, names the kubeconfig
	// context the cluster resolver reads from. Empty means "use the
	// kubeconfig's current-context."
	ClusterResolverContext string

	// ClusterResolverKubeconfig overrides the path the cluster resolver
	// loads its kubeconfig from. Empty falls back to the standard
	// $KUBECONFIG / ~/.kube/config resolution chain.
	ClusterResolverKubeconfig string
}

// Registry routes Requests to one of its registered Resolvers, applies
// the allow-list, and layers a per-run + on-disk cache on top.
type Registry struct {
	mu     sync.Mutex
	direct map[string]Resolver
	// remote is the Mode B driver. When non-nil, Resolve dispatches
	// every Request through remote regardless of name (the validator
	// already short-circuits the allow-list when remote is enabled).
	remote *RemoteResolver
	allow  map[string]struct{}
	opts   Options
	// PerRunCache is a small in-memory map[cacheKey]Resolved the engine
	// populates per run. Exposed so tests can inspect cache hits.
	perRun map[string]Resolved
	// disk is the on-disk cache layer (Phase 6). Survives across runs.
	// nil means "no on-disk cache" (per-run cache only).
	disk *DiskCache
	// offline rejects every cache miss with an error. Wired by
	// SetOffline; the engine surfaces the error on the resolver-end
	// event and as the task-end message.
	offline bool
	// dbg is the debug emitter. Always non-nil after NewRegistry();
	// SetDebug replaces it with a live emitter when --debug is on.
	dbg debug.Emitter
}

// NewRegistry returns an empty Registry. Tests that want a single stub
// resolver call this and then Register their stub.
func NewRegistry() *Registry {
	return &Registry{
		direct: map[string]Resolver{},
		perRun: map[string]Resolved{},
		dbg:    debug.Nop(),
	}
}

// SetDebug installs the debug emitter. The default is a Nop emitter
// (NewRegistry), so resolver code can call r.dbg.Emit unconditionally.
// Called by the engine at run-start when --debug is set.
func (r *Registry) SetDebug(d debug.Emitter) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if d == nil {
		r.dbg = debug.Nop()
		return
	}
	r.dbg = d
}

// Debug returns the currently-installed debug emitter (never nil).
// Resolvers read this lazily so a SetDebug call mid-run takes effect
// without re-registering each resolver.
func (r *Registry) Debug() debug.Emitter {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.dbg
}

// NewDefaultRegistry returns a Registry pre-populated with whatever
// resolvers ship by default. Phase 1 shipped just the inline stub; Phase
// 2 adds the git resolver. Phase 3-4 add hub/http/bundles/cluster here.
func NewDefaultRegistry(opts Options) *Registry {
	r := NewRegistry()
	r.opts = opts
	r.offline = opts.Offline
	if opts.CacheDir != "" {
		r.disk = NewDiskCache(opts.CacheDir)
	}
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

	// Phase 3 (Track 1 #9): hub + http direct resolvers ship by default
	// and only dispatch when their name is in opts.Allow (the CLI default
	// allow-list includes "hub" and "http"). Sibling phases register on
	// the same hook (Phase 4: bundles + cluster).
	r.Register(NewHubResolver(HubOptions{}))
	r.Register(NewHTTPResolver(HTTPOptions{AllowInsecureHTTP: opts.AllowInsecureHTTP}))

	// Phase 4 (Track 1 #9): bundles is registered by default; cluster is
	// off-by-default and only registers when opts.AllowCluster is set
	// OR the user explicitly added "cluster" to opts.Allow. The cluster
	// resolver reads from the user's KUBECONFIG which may point at
	// production, so the default --resolver-allow=git,hub,http,bundles
	// excludes it; an opt-in is required.
	r.Register(NewBundlesResolver(BundlesOptions{
		AllowInsecureHTTP: opts.AllowInsecureHTTP,
	}))
	if opts.AllowCluster || allowListIncludes(opts.Allow, "cluster") || opts.ClusterResolverContext != "" {
		cr, err := NewClusterResolver(ClusterResolverOptions{
			Kubeconfig: opts.ClusterResolverKubeconfig,
			Context:    opts.ClusterResolverContext,
		})
		if err == nil {
			r.Register(cr)
		} else {
			// Register a stub that always errors so dispatch surfaces a
			// clear, actionable diagnostic rather than ErrResolverNotRegistered.
			r.Register(newClusterResolverStub(err))
		}
	}
	return r
}

func allowListIncludes(allow []string, name string) bool {
	for _, a := range allow {
		if a == name {
			return true
		}
	}
	return false
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

// SetRemote installs the Mode B remote-resolver driver. When non-nil,
// every dispatch goes through remote (the validator's
// RemoteResolverEnabled Option already short-circuits the allow-list
// for any resolver name in this mode). Setting nil restores direct
// dispatch.
func (r *Registry) SetRemote(rr *RemoteResolver) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.remote = rr
}

// Remote returns the currently-configured remote driver, or nil if
// Mode B is not active.
func (r *Registry) Remote() *RemoteResolver {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.remote
}

// SetCache installs the on-disk cache layer. Pass nil to disable. The
// cache is consulted between the per-run map and the registered
// Resolver: hits short-circuit the network call; misses fall through
// and Put the result on success.
func (r *Registry) SetCache(c *DiskCache) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.disk = c
}

// Cache returns the currently-installed disk cache, or nil.
func (r *Registry) Cache() *DiskCache {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.disk
}

// SetOffline toggles the --offline gate. When true, every cache miss
// surfaces an error before dispatching to a Resolver.
func (r *Registry) SetOffline(b bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.offline = b
}

// Offline reports whether the registry is in --offline mode.
func (r *Registry) Offline() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.offline
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
//
// Mode B (remote) routing: when SetRemote has installed a remote
// driver, EVERY Resolve goes through it regardless of the Request's
// Resolver name (custom names are the whole point of Mode B; the
// validator's RemoteResolverEnabled Option has already cleared
// arbitrary names). Direct registrations are bypassed in this mode.
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
	disk := r.disk
	offline := r.offline
	r.mu.Unlock()

	// Disk-cache layer (Phase 6). Hits are recorded on the per-run
	// map so a subsequent dispatch in the same run also short-circuits.
	if disk != nil {
		if hit, ok, err := disk.Get(req); err == nil && ok {
			r.mu.Lock()
			r.perRun[key] = hit
			r.mu.Unlock()
			return hit, nil
		}
	}

	// --offline gate: refuse to dispatch on a cache miss.
	if offline {
		return Resolved{}, fmt.Errorf("refresolver: cache miss for resolver %q while --offline is set", req.Resolver)
	}

	r.mu.Lock()
	// Mode B: remote takes precedence over direct + allow-list.
	if r.remote != nil {
		remote := r.remote
		r.mu.Unlock()
		out, err := remote.Resolve(ctx, req)
		if err != nil {
			return out, err
		}
		if out.SHA256 == "" {
			out.SHA256 = sha256Bytes(out.Bytes)
		}
		// Persist to disk cache for cross-run reuse.
		if disk != nil {
			_ = disk.Put(req, out)
		}
		r.mu.Lock()
		r.perRun[key] = out
		r.mu.Unlock()
		return out, nil
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
	// Persist to disk cache for cross-run reuse.
	if disk != nil {
		_ = disk.Put(req, out)
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
