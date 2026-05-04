package refresolver

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"

	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/tools/clientcmd"
)

// Mode B (Track 1 #9 Phase 5): submit a `ResolutionRequest` CRD to a
// remote Tekton cluster and use its rendered output. Tekton's
// resolution controller fetches the bytes (with whatever credentials
// it has) and writes them back on `status.data`.
//
// The remote driver is wire-compatible with both
// resolution.tekton.dev/v1beta1 (preferred) and v1alpha1 (fallback for
// older Tekton Resolution installs). Both shapes share the fields we
// touch: spec.params, status.conditions[Succeeded], status.data.

// gvrResolutionRequestV1Beta1 is the preferred GVR.
var gvrResolutionRequestV1Beta1 = schema.GroupVersionResource{
	Group:    "resolution.tekton.dev",
	Version:  "v1beta1",
	Resource: "resolutionrequests",
}

// gvrResolutionRequestV1Alpha1 is the fallback served by older clusters
// (long-term support channels of OpenShift Pipelines, older Tekton
// Resolution installs).
var gvrResolutionRequestV1Alpha1 = schema.GroupVersionResource{
	Group:    "resolution.tekton.dev",
	Version:  "v1alpha1",
	Resource: "resolutionrequests",
}

const (
	// Default poll interval between status reads. Smaller than the
	// upstream Tekton controller's reconcile loop (~1s) so we don't
	// trail it by more than one tick.
	defaultRemotePollInterval = 500 * time.Millisecond

	// Default per-request budget. Matches spec §8 (--remote-resolver-timeout).
	defaultRemoteTimeout = 60 * time.Second

	// Label key + value the upstream Tekton resolution controller uses
	// to route a ResolutionRequest to a particular resolver type. Setting
	// it makes the request observable via `kubectl get rr -l ...`.
	resolverTypeLabel = "resolution.tekton.dev/type"
)

// RemoteResolverOptions configures a RemoteResolver. The CLI plumbs
// these from --remote-resolver-context / --remote-resolver-namespace /
// --remote-resolver-timeout. Tests inject Dynamic directly.
type RemoteResolverOptions struct {
	// Dynamic, when non-nil, replaces the dynamic client constructed
	// from the kubeconfig. Tests use this to bypass kubeconfig loading.
	Dynamic dynamic.Interface

	// Kubeconfig overrides the kubeconfig file path. Empty falls back
	// to KUBECONFIG / ~/.kube/config per
	// clientcmd.NewDefaultClientConfigLoadingRules.
	Kubeconfig string

	// Context names the kubeconfig context to load. Empty means "use
	// the kubeconfig's current-context."
	Context string

	// Namespace where ResolutionRequests are submitted. Default "default".
	Namespace string

	// Timeout is the per-request wall-clock budget. Default 60s.
	Timeout time.Duration

	// PollInterval gates how often status is polled when no Watch event
	// arrives. Default 500ms.
	PollInterval time.Duration
}

// RemoteResolver is the Mode B implementation: it submits a
// ResolutionRequest to a remote Tekton cluster and waits for the
// controller to fill in status.data.
//
// SECURITY: the remote resolver speaks to whichever cluster the
// supplied kubeconfig context points at, under that context's
// service-account RBAC. tkn-act never elevates privileges and never
// stores credentials; the user's kubectl identity is the only thing
// the remote cluster sees.
type RemoteResolver struct {
	dyn       dynamic.Interface
	namespace string
	timeout   time.Duration
	poll      time.Duration

	// useV1Alpha1 is set lazily after a NoKindMatchError on v1beta1.
	// It's racy to share without locking but we only flip once per
	// process, on first use; subsequent reads are idempotent.
	useV1Alpha1 bool
}

// NewRemoteResolver builds a RemoteResolver, loading the kube client
// from kubeconfig if opts.Dynamic is unset. Returns an error if the
// kubeconfig context can't be loaded.
func NewRemoteResolver(opts RemoteResolverOptions) (*RemoteResolver, error) {
	if opts.Dynamic != nil {
		return NewRemoteResolverFromOptions(opts), nil
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
		return nil, fmt.Errorf("remote: build kube client config: %w", err)
	}
	dyn, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("remote: build dynamic client: %w", err)
	}
	opts.Dynamic = dyn
	return NewRemoteResolverFromOptions(opts), nil
}

// NewRemoteResolverFromOptions returns a RemoteResolver with the
// supplied options, no kubeconfig loading. Tests use this to bypass
// the on-disk kubeconfig loader; the CLI uses NewRemoteResolver.
func NewRemoteResolverFromOptions(opts RemoteResolverOptions) *RemoteResolver {
	r := &RemoteResolver{
		dyn:       opts.Dynamic,
		namespace: opts.Namespace,
		timeout:   opts.Timeout,
		poll:      opts.PollInterval,
	}
	if r.namespace == "" {
		r.namespace = "default"
	}
	if r.timeout <= 0 {
		r.timeout = defaultRemoteTimeout
	}
	if r.poll <= 0 {
		r.poll = defaultRemotePollInterval
	}
	return r
}

// Name implements Resolver. The literal name is `remote`, but Registry
// routes any unknown name through Remote when SetRemote() has been
// called (Mode B short-circuits the direct allow-list).
func (r *RemoteResolver) Name() string { return "remote" }

// Resolve implements Resolver. The lifecycle is:
//
//  1. Build a ResolutionRequest with spec.params from req.Params.
//  2. Create() it via the dynamic client; pick v1beta1 first, fall
//     back to v1alpha1 on NoKindMatchError (older clusters).
//  3. Poll status.conditions[Succeeded] until True / False / timeout.
//  4. On True: base64-decode status.data and return as Resolved.Bytes.
//  5. On False or any error: surface a typed error including reason+message.
//  6. ALWAYS Delete() the ResolutionRequest on the way out — using
//     context.Background() so SIGINT mid-resolution still triggers
//     cleanup (Critical 5 invariant).
func (r *RemoteResolver) Resolve(ctx context.Context, req Request) (Resolved, error) {
	if r.dyn == nil {
		return Resolved{}, errors.New("remote: dynamic client is nil (build via NewRemoteResolver with a kubeconfig)")
	}

	gvr, created, err := r.create(ctx, req)
	if err != nil {
		return Resolved{}, err
	}
	// Cleanup discipline: defer-delete with a fresh context so that
	// SIGINT-style cancellation of the request context still runs
	// cleanup (per spec §6 + Phase 5 Critical 5).
	defer func() {
		// Use a short timeout to bound cleanup work in the rare case
		// the cluster is mid-rolling-restart. This is intentionally
		// `context.Background`-derived, not the request ctx.
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = r.dyn.Resource(gvr).Namespace(r.namespace).Delete(cleanupCtx, created.GetName(), metav1.DeleteOptions{})
	}()

	deadline := time.Now().Add(r.timeout)
	pollCtx, pollCancel := context.WithDeadline(ctx, deadline)
	defer pollCancel()

	for {
		// Fast-path: poll with a Get rather than a streaming Watch.
		// The fake client's watch stream is finicky and the upstream
		// Tekton resolution controller writes status in a tight loop,
		// so a small-interval poll is sufficient and easier to reason
		// about than the watch + retry-on-410 dance.
		got, gerr := r.dyn.Resource(gvr).Namespace(r.namespace).Get(pollCtx, created.GetName(), metav1.GetOptions{})
		if gerr == nil {
			done, payload, ferr := readSucceededStatus(got)
			if ferr != nil {
				return Resolved{}, ferr
			}
			if done {
				bytes, derr := base64.StdEncoding.DecodeString(payload)
				if derr != nil {
					return Resolved{}, fmt.Errorf("remote: status.data base64 decode: %w", derr)
				}
				source := fmt.Sprintf("remote: %s/%s (%s)", r.namespace, created.GetName(), gvr.Version)
				return Resolved{
					Bytes:  bytes,
					Source: source,
				}, nil
			}
		}

		select {
		case <-pollCtx.Done():
			cause := pollCtx.Err()
			if errors.Is(cause, context.DeadlineExceeded) {
				return Resolved{}, fmt.Errorf("remote: timeout after %s waiting for ResolutionRequest %s/%s", r.timeout, r.namespace, created.GetName())
			}
			return Resolved{}, fmt.Errorf("remote: %w", cause)
		case <-time.After(r.poll):
			// next iteration
		}
	}
}

// create submits the ResolutionRequest and returns the GVR it landed
// on plus the created object. v1beta1 is tried first; on
// NoKindMatchError (older Tekton Resolution installs) the call retries
// on v1alpha1. The chosen GVR is cached on the receiver so subsequent
// resolves skip the v1beta1 probe.
func (r *RemoteResolver) create(ctx context.Context, req Request) (schema.GroupVersionResource, *unstructured.Unstructured, error) {
	rr := buildResolutionRequest(req, r.namespace)

	gvrPrimary := gvrResolutionRequestV1Beta1
	apiVersion := "resolution.tekton.dev/v1beta1"
	if r.useV1Alpha1 {
		gvrPrimary = gvrResolutionRequestV1Alpha1
		apiVersion = "resolution.tekton.dev/v1alpha1"
	}
	rr.SetAPIVersion(apiVersion)

	created, err := r.dyn.Resource(gvrPrimary).Namespace(r.namespace).Create(ctx, rr, metav1.CreateOptions{})
	if err == nil {
		return gvrPrimary, created, nil
	}
	if !apimeta.IsNoMatchError(err) {
		return gvrPrimary, nil, fmt.Errorf("remote: create ResolutionRequest: %w", err)
	}
	if r.useV1Alpha1 {
		// We were already on v1alpha1; nothing else to fall back to.
		return gvrPrimary, nil, fmt.Errorf("remote: create ResolutionRequest: %w", err)
	}
	// Fall back to v1alpha1.
	rr.SetAPIVersion("resolution.tekton.dev/v1alpha1")
	created, err = r.dyn.Resource(gvrResolutionRequestV1Alpha1).Namespace(r.namespace).Create(ctx, rr, metav1.CreateOptions{})
	if err != nil {
		return gvrResolutionRequestV1Alpha1, nil, fmt.Errorf("remote: create ResolutionRequest (v1alpha1 fallback): %w", err)
	}
	r.useV1Alpha1 = true
	return gvrResolutionRequestV1Alpha1, created, nil
}

// buildResolutionRequest constructs the unstructured CRD body submitted
// to the remote cluster. Mirrors upstream Tekton's
// `resolution.tekton.dev/v1beta1` `ResolutionRequest` shape.
func buildResolutionRequest(req Request, namespace string) *unstructured.Unstructured {
	params := make([]interface{}, 0, len(req.Params))
	// Stable order so two identical Resolves submit identical bodies
	// (helps server-side dedup if multiple controllers race).
	keys := sortedKeys(req.Params)
	for _, k := range keys {
		params = append(params, map[string]interface{}{
			"name":  k,
			"value": req.Params[k],
		})
	}
	return &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "resolution.tekton.dev/v1beta1",
		"kind":       "ResolutionRequest",
		"metadata": map[string]interface{}{
			"generateName": "tkn-act-",
			"namespace":    namespace,
			"labels": map[string]interface{}{
				resolverTypeLabel: req.Resolver,
			},
		},
		"spec": map[string]interface{}{
			"params": params,
		},
	}}
}

// readSucceededStatus inspects status.conditions[Succeeded].
//   - done=true, "<base64>": ready to decode and return.
//   - done=true, "":         not done yet (no conditions, or condition status="Unknown").
//   - error != nil:          Succeeded=False — surface to caller.
func readSucceededStatus(obj *unstructured.Unstructured) (done bool, base64Data string, err error) {
	conditions, found, _ := unstructured.NestedSlice(obj.Object, "status", "conditions")
	if !found {
		return false, "", nil
	}
	for _, c := range conditions {
		m, ok := c.(map[string]interface{})
		if !ok {
			continue
		}
		t, _ := m["type"].(string)
		if t != "Succeeded" {
			continue
		}
		status, _ := m["status"].(string)
		switch status {
		case "True":
			data, _, _ := unstructured.NestedString(obj.Object, "status", "data")
			return true, data, nil
		case "False":
			reason, _ := m["reason"].(string)
			message, _ := m["message"].(string)
			parts := []string{}
			if reason != "" {
				parts = append(parts, "reason="+reason)
			}
			if message != "" {
				parts = append(parts, "message="+message)
			}
			detail := strings.Join(parts, " ")
			if detail == "" {
				detail = "no reason/message"
			}
			return false, "", fmt.Errorf("remote: ResolutionRequest failed: %s", detail)
		}
	}
	return false, "", nil
}

// sortedKeys returns keys of a string-keyed map in lexicographic order.
// Avoids importing sort just for one call site (and matches how
// CacheKey already orders map keys).
func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	// insertion sort: resolver param maps are tiny (<10 entries).
	for i := 1; i < len(keys); i++ {
		for j := i; j > 0 && keys[j-1] > keys[j]; j-- {
			keys[j-1], keys[j] = keys[j], keys[j-1]
		}
	}
	return keys
}
