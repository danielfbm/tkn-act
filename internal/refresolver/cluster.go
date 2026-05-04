package refresolver

import (
	"context"
	"errors"
	"fmt"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/yaml"
)

// ErrClusterContextRequired fires when the cluster resolver is asked to
// resolve without a kubeconfig context — either explicitly named via
// --cluster-resolver-context or implied by KUBECONFIG plus a
// current-context. The resolver refuses by default to avoid silently
// reading from whatever KUBECONFIG happens to point at.
var ErrClusterContextRequired = errors.New("refresolver: cluster resolver requires a kubeconfig context (set --cluster-resolver-context or add `cluster` to --resolver-allow with KUBECONFIG explicitly set)")

// ClusterResolverOptions configures a cluster resolver. All fields are
// optional; the production CLI plumbs values from
// --cluster-resolver-context and --cluster-resolver-kubeconfig.
type ClusterResolverOptions struct {
	// Context names the kubeconfig context to read from. Empty means
	// "use the kubeconfig's current-context."
	Context string

	// Kubeconfig overrides the kubeconfig file path. Empty falls back to
	// $KUBECONFIG / ~/.kube/config per `clientcmd.NewDefaultClientConfigLoadingRules`.
	Kubeconfig string

	// Dynamic, when non-nil, replaces the dynamic client constructed
	// from the kubeconfig. Tests use this to inject a fake client.
	Dynamic dynamic.Interface
}

// ClusterResolver implements Resolver for taskRef.resolver: cluster.
//
// Resolver params:
//
//	name       required. metadata.name of the resource to read.
//	kind       optional, default "task". One of "task", "pipeline".
//	namespace  optional, default "default".
//
// The resolver reads via a kube dynamic client built from
// --cluster-resolver-kubeconfig (or KUBECONFIG / ~/.kube/config) at the
// context named by --cluster-resolver-context. The result is serialized
// back to YAML so the engine can feed it through the standard loader
// path.
//
// SECURITY: this resolver is OFF BY DEFAULT in NewDefaultRegistry.
// KUBECONFIG may point at a production cluster; we require the user to
// opt in explicitly via --resolver-allow=...,cluster or by setting
// --cluster-resolver-context.
type ClusterResolver struct {
	opts ClusterResolverOptions
	dyn  dynamic.Interface
}

// NewClusterResolver constructs a cluster resolver. Returns an error if
// neither opts.Dynamic nor a usable kubeconfig+context can be found —
// that's distinct from ErrClusterContextRequired (which fires at
// resolve-time when the registry decides whether to dispatch).
func NewClusterResolver(opts ClusterResolverOptions) (*ClusterResolver, error) {
	if opts.Dynamic != nil {
		return &ClusterResolver{opts: opts, dyn: opts.Dynamic}, nil
	}
	loadingRules := clientcmd.NewDefaultClientConfigLoadingRules()
	if opts.Kubeconfig != "" {
		loadingRules.ExplicitPath = opts.Kubeconfig
	}
	overrides := &clientcmd.ConfigOverrides{}
	if opts.Context != "" {
		overrides.CurrentContext = opts.Context
	}
	cfg, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(loadingRules, overrides).ClientConfig()
	if err != nil {
		return nil, fmt.Errorf("cluster: build kube client config: %w", err)
	}
	dyn, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("cluster: build dynamic client: %w", err)
	}
	return &ClusterResolver{opts: opts, dyn: dyn}, nil
}

// Name implements Resolver.
func (c *ClusterResolver) Name() string { return "cluster" }

// Resolve implements Resolver. See type-level docs for the param shape.
func (c *ClusterResolver) Resolve(ctx context.Context, req Request) (Resolved, error) {
	resourceName := strings.TrimSpace(req.Params["name"])
	if resourceName == "" {
		return Resolved{}, errors.New("cluster: name param is required")
	}
	kind := strings.TrimSpace(req.Params["kind"])
	if kind == "" {
		kind = "task"
	}
	kind = strings.ToLower(kind)

	namespace := strings.TrimSpace(req.Params["namespace"])
	if namespace == "" {
		namespace = "default"
	}

	gvr, err := gvrForKind(kind)
	if err != nil {
		return Resolved{}, err
	}

	if c.dyn == nil {
		return Resolved{}, ErrClusterContextRequired
	}
	got, err := c.dyn.Resource(gvr).Namespace(namespace).Get(ctx, resourceName, metav1.GetOptions{})
	if err != nil {
		return Resolved{}, fmt.Errorf("cluster: %s %q in ns %q: %w", kind, resourceName, namespace, err)
	}

	// Strip server-side bookkeeping that doesn't belong in YAML the
	// loader will consume. Mirrors what `kubectl get -o yaml` typically
	// shows after `--export` (deprecated) or what tknctl-export does.
	stripServerSideFields(got.Object)

	out, err := yaml.Marshal(got.Object)
	if err != nil {
		return Resolved{}, fmt.Errorf("cluster: marshal %s/%s: %w", kind, resourceName, err)
	}

	source := fmt.Sprintf("cluster: %s %s/%s", kind, namespace, resourceName)
	if c.opts.Context != "" {
		source = fmt.Sprintf("cluster[%s]: %s %s/%s", c.opts.Context, kind, namespace, resourceName)
	}

	return Resolved{
		Bytes:  out,
		Source: source,
	}, nil
}

// gvrForKind maps the user-facing kind string to the Tekton GVR the
// dynamic client needs. Only `task` and `pipeline` are supported — the
// cluster resolver is intentionally narrower than the bundles resolver.
func gvrForKind(kind string) (schema.GroupVersionResource, error) {
	switch kind {
	case "task":
		return schema.GroupVersionResource{Group: "tekton.dev", Version: "v1", Resource: "tasks"}, nil
	case "pipeline":
		return schema.GroupVersionResource{Group: "tekton.dev", Version: "v1", Resource: "pipelines"}, nil
	default:
		return schema.GroupVersionResource{}, fmt.Errorf("cluster: unsupported kind %q (want task/pipeline)", kind)
	}
}

// stripServerSideFields removes fields the API server populates that
// the loader doesn't need (and may reject as unknown depending on
// strict-decode settings).
func stripServerSideFields(obj map[string]interface{}) {
	delete(obj, "status")
	if md, ok := obj["metadata"].(map[string]interface{}); ok {
		delete(md, "uid")
		delete(md, "resourceVersion")
		delete(md, "generation")
		delete(md, "creationTimestamp")
		delete(md, "managedFields")
		delete(md, "selfLink")
		// Annotations like kubectl.kubernetes.io/last-applied-configuration
		// are noise but not harmful; leave them for fidelity with what
		// the user has on the cluster.
	}
}

// clusterResolverStub records a constructor error and re-emits it on
// every Resolve call. NewDefaultRegistry installs this when the user
// opts the cluster resolver into the registry but kube client setup
// fails, so the dispatch path produces a clear diagnostic instead of
// ErrResolverNotRegistered.
type clusterResolverStub struct{ err error }

func newClusterResolverStub(err error) *clusterResolverStub {
	return &clusterResolverStub{err: err}
}

func (s *clusterResolverStub) Name() string { return "cluster" }

func (s *clusterResolverStub) Resolve(_ context.Context, _ Request) (Resolved, error) {
	return Resolved{}, s.err
}
